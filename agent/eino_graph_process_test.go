package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/cloudwego/eino/compose"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/internal/retrieval/reranker"
	checkpointstore "github.com/kubepilot-aiops/kubepilot/internal/store"
)

// runCheckpointResumeE2E runs the real Supervisor Graph to its
// Approval Interrupt, then starts a second OS process which reconstructs the
// same Agent registry and resumes from the Redis checkpoint. The test is
// opt-in because it requires an isolated TEST_REDIS_URL.
func runCheckpointResumeE2E(t *testing.T) {
	redisURL := os.Getenv("TEST_REDIS_URL")
	if redisURL == "" {
		t.Skip("TEST_REDIS_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	redis, err := checkpointstore.NewRedis(redisURL)
	if err != nil {
		t.Fatal(err)
	}
	defer redis.Close()
	checkpoints := checkpointstore.EinoCheckpointStore{Redis: redis, TTL: 5 * time.Minute}
	registry, err := NewAgentRegistry(ctx, scriptedEinoModel{})
	if err != nil {
		t.Fatal(err)
	}
	collectors := map[string]Collector{}
	incidentID := incidentIDFromEnvOrNew()
	for _, source := range []string{"metric", "log", "trace", "kubernetes"} {
		collectors[source] = crossProcessCollector{store: redis, incidentID: incidentID, source: source}
	}
	executor := &crossProcessExecutor{store: redis, key: "mutation-count:" + incidentID}
	supervisor, err := NewSupervisor(ctx, SupervisorDeps{
		Collectors:           collectors,
		HistoricalCandidates: fixedHistoricalRetriever{},
		Agents:               registry,
		Executor:             executor,
		Checkpoints:          checkpoints,
		Reranker:             resumeReranker{},
		VerificationInterval: time.Millisecond,
		VerificationTimeout:  time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	incident := &domain.Incident{ID: incidentID, Status: domain.StatusReceived, Namespace: "kubepilot-demo", Service: "gateway-service", Resource: "gateway-service", Summary: "CPU alert", ModelConfigHash: "model-config-hash-e2e", ModelProtocol: "openai-compatible", ModelName: "checkpoint-test-model", CreatedAt: time.Now().Add(-time.Minute), UpdatedAt: time.Now().Add(-time.Minute)}
	_, runErr := supervisor.Run(ctx, incident)
	interrupt, ok := compose.ExtractInterruptInfo(runErr)
	if !ok || len(interrupt.InterruptContexts) != 1 {
		t.Fatalf("graph did not reach one approval interrupt: %v", runErr)
	}
	if incident.Status != domain.StatusAwaitingApproval || incident.Proposal == nil || incident.DryRun == nil {
		t.Fatalf("graph did not persist approval state: status=%s proposal=%+v dryrun=%+v", incident.Status, incident.Proposal, incident.DryRun)
	}
	if incident.SkillSnapshotHash == "" || incident.ModelConfigHash == "" || incident.RerankerConfigHash == "" || incident.AgentBudget == nil {
		t.Fatalf("checkpoint identity or budget was not captured: skill=%q model=%q reranker=%q budget=%+v", incident.SkillSnapshotHash, incident.ModelConfigHash, incident.RerankerConfigHash, incident.AgentBudget)
	}
	for source := range collectors {
		if count := loadCount(ctx, redis, "evidence-count:"+incident.ID+":"+source); count != 1 {
			t.Fatalf("evidence source %s executed %d times before interrupt", source, count)
		}
	}
	expectedBudget, err := json.Marshal(incident.AgentBudget)
	if err != nil {
		t.Fatal(err)
	}
	expectedSkill, expectedModel, expectedReranker := incident.SkillSnapshotHash, incident.ModelConfigHash, incident.RerankerConfigHash
	expectedProposal := incident.Proposal.ID
	resume := &ApprovalResumeData{Approved: true, Context: domain.ExecutionContext{
		NamespaceAllowlist: []string{"kubepilot-demo"}, IncidentID: incident.ID, ProposalID: incident.Proposal.ID, ApprovalID: "approval-cross-process", IdempotencyKey: "idempotency-cross-process", Operator: "test", TargetUID: incident.Proposal.TargetUID, ResourceVersion: incident.Proposal.ResourceVersion, MutationSpecHash: incident.DryRun.MutationSpecHash, ApprovedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Minute),
	}}
	resumeRaw, err := json.Marshal(resume)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestCheckpointResumeE2E")
	cmd.Env = append(os.Environ(),
		"TEST_REDIS_URL="+redisURL,
		"KUBEPILOT_AGENT_RESUME_HELPER=resume",
		"KUBEPILOT_RESUME_INCIDENT_ID="+incident.ID,
		"KUBEPILOT_RESUME_INTERRUPT_ID="+interrupt.InterruptContexts[0].ID,
		"KUBEPILOT_RESUME_DATA="+string(resumeRaw),
		"KUBEPILOT_EXPECTED_SKILL_HASH="+expectedSkill,
		"KUBEPILOT_EXPECTED_MODEL_HASH="+expectedModel,
		"KUBEPILOT_EXPECTED_RERANKER_HASH="+expectedReranker,
		"KUBEPILOT_EXPECTED_PROPOSAL_ID="+expectedProposal,
		"KUBEPILOT_EXPECTED_BUDGET="+string(expectedBudget),
	)
	if output, runErr := cmd.CombinedOutput(); runErr != nil {
		t.Fatalf("resume process failed: %v\n%s", runErr, output)
	}
	mutationCount, err := redis.Load(ctx, executor.key)
	if err != nil || string(mutationCount) != "1" {
		t.Fatalf("mutation was not executed exactly once across processes: count=%q err=%v", mutationCount, err)
	}
	for source := range collectors {
		if count := loadCount(ctx, redis, "evidence-count:"+incident.ID+":"+source); count != 1 {
			t.Fatalf("evidence source %s was recollected during resume: count=%d", source, count)
		}
	}
	_ = redis.Delete(ctx, executor.key)
	if _, exists, err := checkpoints.Get(ctx, "incident:"+incident.ID); err != nil || exists {
		t.Fatalf("completed cross-process checkpoint was not removed: exists=%v err=%v", exists, err)
	}
}

func runResumeHelper() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	redis, err := checkpointstore.NewRedis(os.Getenv("TEST_REDIS_URL"))
	if err != nil {
		panic(err)
	}
	defer redis.Close()
	checkpoints := checkpointstore.EinoCheckpointStore{Redis: redis, TTL: 5 * time.Minute}
	registry, err := NewAgentRegistry(ctx, scriptedEinoModel{})
	if err != nil {
		panic(err)
	}
	collectors := map[string]Collector{}
	for _, source := range []string{"metric", "log", "trace", "kubernetes"} {
		collectors[source] = crossProcessCollector{store: redis, incidentID: os.Getenv("KUBEPILOT_RESUME_INCIDENT_ID"), source: source}
	}
	executor := &crossProcessExecutor{store: redis, key: "mutation-count:" + os.Getenv("KUBEPILOT_RESUME_INCIDENT_ID")}
	supervisor, err := NewSupervisor(ctx, SupervisorDeps{
		Collectors:           collectors,
		HistoricalCandidates: fixedHistoricalRetriever{},
		Agents:               registry,
		Executor:             executor,
		Checkpoints:          checkpoints,
		Reranker:             resumeReranker{},
		VerificationInterval: time.Millisecond,
		VerificationTimeout:  time.Second,
	})
	if err != nil {
		panic(err)
	}
	var resume ApprovalResumeData
	if err := json.Unmarshal([]byte(os.Getenv("KUBEPILOT_RESUME_DATA")), &resume); err != nil {
		panic(err)
	}
	state, err := supervisor.Resume(ctx, os.Getenv("KUBEPILOT_RESUME_INCIDENT_ID"), os.Getenv("KUBEPILOT_RESUME_INTERRUPT_ID"), &resume)
	if err != nil || state == nil || state.Incident == nil || state.Incident.Status != domain.StatusResolved {
		panic("cross-process resume did not resolve the Incident")
	}
	if state.Incident.SkillSnapshotHash != os.Getenv("KUBEPILOT_EXPECTED_SKILL_HASH") || state.Incident.ModelConfigHash != os.Getenv("KUBEPILOT_EXPECTED_MODEL_HASH") || state.Incident.RerankerConfigHash != os.Getenv("KUBEPILOT_EXPECTED_RERANKER_HASH") {
		panic("checkpoint runtime identity changed during resume")
	}
	if state.Incident.Proposal == nil || state.Incident.Proposal.ID != os.Getenv("KUBEPILOT_EXPECTED_PROPOSAL_ID") {
		panic("recovery proposal was regenerated during resume")
	}
	budgetRaw, err := json.Marshal(state.Incident.AgentBudget)
	if err != nil || string(budgetRaw) != os.Getenv("KUBEPILOT_EXPECTED_BUDGET") {
		panic("agent tool or correction budget was reset during resume")
	}
	os.Exit(0)
}

type crossProcessCollector struct {
	store      *checkpointstore.RedisStore
	incidentID string
	source     string
}

func (c crossProcessCollector) Collect(ctx context.Context, incident *domain.Incident, request domain.EvidenceRequest) ([]domain.Evidence, error) {
	// Verification uses the same read-only collectors, but it is deliberately
	// excluded from this counter: the test is proving Evidence was not
	// recollected while post-approval health checks may still run.
	if incident.Status != domain.StatusVerifying {
		key := "evidence-count:" + c.incidentID + ":" + c.source
		count := loadCount(ctx, c.store, key) + 1
		if err := c.store.Save(ctx, key, []byte(strconv.Itoa(count)), time.Minute); err != nil {
			return nil, err
		}
	}
	return fixedCollector{source: c.source}.Collect(ctx, incident, request)
}

func loadCount(ctx context.Context, store *checkpointstore.RedisStore, key string) int {
	value, err := store.Load(ctx, key)
	if err != nil || len(value) == 0 {
		return 0
	}
	count, err := strconv.Atoi(string(value))
	if err != nil {
		return 0
	}
	return count
}

type resumeReranker struct{}

func (resumeReranker) Enabled() bool { return true }
func (resumeReranker) Rerank(_ context.Context, _ string, documents []string, _ int) ([]reranker.Result, error) {
	results := make([]reranker.Result, len(documents))
	for index := range documents {
		results[index] = reranker.Result{Index: index, Score: .5}
	}
	return results, nil
}
func (resumeReranker) Probe(context.Context) error { return nil }
func (resumeReranker) ConfigHash() string          { return "reranker-config-hash-e2e" }
func (resumeReranker) Health() map[string]any      { return map[string]any{"configured": true} }

// crossProcessExecutor is a test-only executor. It records the mutation count
// in Redis so the parent process can prove that resume did not replay a
// side-effect. Production executors remain unchanged.
type crossProcessExecutor struct {
	store *checkpointstore.RedisStore
	key   string
}

func (e *crossProcessExecutor) DryRun(_ context.Context, proposal *domain.RecoveryProposal) (*domain.DryRunResult, error) {
	proposal.TargetUID, proposal.ResourceVersion = "uid-1", "rv-1"
	return &domain.DryRunResult{Success: true, Action: proposal.Action, Target: proposal.Target, MutationSpecHash: "mutation-1", ValidatedAt: time.Now().UTC()}, nil
}

func (e *crossProcessExecutor) Execute(ctx context.Context, _ *domain.Incident, _ domain.RecoveryProposal) error {
	current, err := e.store.Load(ctx, e.key)
	if err != nil && err != checkpointstore.ErrNotFound {
		return err
	}
	count := 0
	if len(current) > 0 {
		count, err = strconv.Atoi(string(current))
		if err != nil {
			return err
		}
	}
	return e.store.Save(ctx, e.key, []byte(strconv.Itoa(count+1)), time.Minute)
}

func (e *crossProcessExecutor) Verify(_ context.Context, _ *domain.Incident) (domain.Verification, error) {
	return domain.Verification{Success: true, Checks: map[string]bool{"ready": true}, CompletedAt: time.Now().UTC()}, nil
}

func incidentIDFromEnvOrNew() string {
	return "incident-cross-process-" + time.Now().UTC().Format("20060102150405.000000000")
}
