package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

type scriptedEinoModel struct{}

func (scriptedEinoModel) Generate(_ context.Context, messages []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	options := model.GetCommonOptions(nil, opts...)
	name := ""
	for _, candidate := range options.Tools {
		if candidate.Name == "submit_evidence_plan" {
			name = candidate.Name
		}
	}
	for _, message := range messages {
		if message.Role == schema.User && strings.Contains(message.Content, `"task":"correlation"`) {
			name = "submit_correlation_decision"
		}
	}
	if name == "" && len(options.Tools) == 1 {
		name = options.Tools[0].Name
	}
	arguments := map[string]any{}
	switch name {
	case "submit_evidence_plan":
		arguments = map[string]any{"window_start": time.Now().Add(-time.Minute), "window_end": time.Now(), "sources": []map[string]any{{"source": "metric"}, {"source": "log"}, {"source": "trace"}, {"source": "kubernetes"}}}
	case "submit_correlation_decision":
		arguments = map[string]any{"merge": false, "confidence": .4, "reason": "insufficient operational linkage"}
	case "submit_diagnosis":
		evidenceID := firstEvidenceID(messages)
		arguments = map[string]any{"reasoning_type": "hypothesis_verification", "root_cause": "CPU saturation", "category": "cpu", "variant": "busy_loop", "service": "gateway-service", "resource": "gateway-service", "confidence": .91, "evidence_ids": []string{evidenceID}, "hypotheses": []map[string]any{{"id": "h1", "cause": "CPU saturation", "probability": .91, "supporting_evidence": []string{evidenceID}, "falsification_conditions": []string{"CPU is normal"}}}}
	case "submit_recovery_proposal":
		arguments = map[string]any{"action": "restart_pod", "target": "gateway-service", "parameters": map[string]any{}, "reason": "restore service", "risk": "brief disruption", "diff": "restart workload", "rollback": "wait for prior replica", "confidence": .9}
	default:
		return nil, fmt.Errorf("unexpected tool %s", name)
	}
	raw, _ := json.Marshal(arguments)
	return &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "call-" + name, Type: "function", Function: schema.FunctionCall{Name: name, Arguments: string(raw)}}}}, nil
}

func (m scriptedEinoModel) Stream(ctx context.Context, messages []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, messages, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func firstEvidenceID(messages []*schema.Message) string {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role != schema.User {
			continue
		}
		var payload struct {
			Evidence []struct {
				ID string `json:"id"`
			} `json:"evidence"`
		}
		if json.Unmarshal([]byte(messages[index].Content), &payload) == nil && len(payload.Evidence) > 0 {
			return payload.Evidence[0].ID
		}
	}
	return "missing"
}

type fixedCollector struct{ source string }

func (c fixedCollector) Collect(_ context.Context, in *domain.Incident) ([]domain.Evidence, error) {
	return []domain.Evidence{{Source: c.source, Kind: c.source + "_evidence", Summary: c.source + " evidence", ObservedAt: time.Now().UTC(), Namespace: in.Namespace, Service: in.Service, Resource: in.Resource}}, nil
}

type parallelCollectorGroup struct {
	count   atomic.Int32
	release chan struct{}
	once    sync.Once
}

type parallelCollector struct {
	source string
	group  *parallelCollectorGroup
}

func (c parallelCollector) Collect(ctx context.Context, in *domain.Incident) ([]domain.Evidence, error) {
	if c.group.count.Add(1) == 4 {
		c.group.once.Do(func() { close(c.group.release) })
	}
	select {
	case <-c.group.release:
		return fixedCollector{source: c.source}.Collect(ctx, in)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type graphExecutor struct{ executed bool }

func (e *graphExecutor) DryRun(_ context.Context, proposal *domain.RecoveryProposal) (*domain.DryRunResult, error) {
	proposal.TargetUID, proposal.ResourceVersion = "uid-1", "rv-1"
	return &domain.DryRunResult{Success: true, Action: proposal.Action, Target: proposal.Target, MutationSpecHash: "mutation-1", ValidatedAt: time.Now().UTC()}, nil
}
func (e *graphExecutor) Execute(_ context.Context, _ *domain.Incident, _ domain.RecoveryProposal) error {
	e.executed = true
	return nil
}
func (e *graphExecutor) Verify(_ context.Context, _ *domain.Incident) (domain.Verification, error) {
	return domain.Verification{Success: true, Checks: map[string]bool{"ready": true}, CompletedAt: time.Now().UTC()}, nil
}

type memoryEinoCheckpoint struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (m *memoryEinoCheckpoint) Get(_ context.Context, id string) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.data[id]
	return append([]byte(nil), value...), ok, nil
}
func (m *memoryEinoCheckpoint) Set(_ context.Context, id string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[id] = append([]byte(nil), value...)
	return nil
}
func (m *memoryEinoCheckpoint) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, id)
	return nil
}

func TestEinoGraphInterruptAndResume(t *testing.T) {
	ctx := context.Background()
	registry, err := NewAgentRegistry(ctx, scriptedEinoModel{})
	if err != nil {
		t.Fatal(err)
	}
	if names := registry.Names(); len(names) != 3 || names[0] != SupervisorAgentName || names[1] != DiagnosisAgentName || names[2] != RecoveryAgentName {
		t.Fatalf("unexpected ADK agents: %v", names)
	}
	checkpoints := &memoryEinoCheckpoint{data: map[string][]byte{}}
	executor := &graphExecutor{}
	collectors := map[string]Collector{}
	for _, source := range []string{"metric", "log", "trace", "kubernetes"} {
		collectors[source] = fixedCollector{source: source}
	}
	supervisor, err := NewSupervisor(ctx, SupervisorDeps{Collectors: collectors, Historical: fixedCollector{source: "historical"}, Agents: registry, Executor: executor, Checkpoints: checkpoints})
	if err != nil {
		t.Fatal(err)
	}
	incident := &domain.Incident{ID: "incident-eino", Status: domain.StatusReceived, Namespace: "kubepilot-demo", Service: "gateway-service", Resource: "gateway-service", Summary: "CPU alert", CreatedAt: time.Now().Add(-time.Minute), UpdatedAt: time.Now().Add(-time.Minute)}
	_, runErr := supervisor.Run(ctx, incident)
	interrupt, ok := compose.ExtractInterruptInfo(runErr)
	if !ok || len(interrupt.InterruptContexts) != 1 {
		t.Fatalf("expected one Eino interrupt, got %v", runErr)
	}
	if incident.Status != domain.StatusAwaitingApproval || incident.DryRun == nil || incident.Proposal == nil {
		t.Fatalf("incident did not reach approval: %#v", incident)
	}
	resume := &ApprovalResumeData{Approved: true, Context: domain.ExecutionContext{NamespaceAllowlist: []string{"kubepilot-demo"}, IncidentID: incident.ID, ProposalID: incident.Proposal.ID, ApprovalID: "approval-1", IdempotencyKey: "key-1", Operator: "test", TargetUID: "uid-1", ResourceVersion: "rv-1", MutationSpecHash: "mutation-1", ApprovedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Minute)}}
	state, err := supervisor.Resume(ctx, incident.ID, interrupt.InterruptContexts[0].ID, resume)
	if err != nil {
		t.Fatal(err)
	}
	if state.Incident.Status != domain.StatusResolved || !executor.executed {
		t.Fatalf("status=%s executed=%v", state.Incident.Status, executor.executed)
	}
	if _, exists, _ := checkpoints.Get(ctx, "incident:"+incident.ID); exists {
		t.Fatal("completed workflow checkpoint was not deleted")
	}
}

func TestEvidenceToolsNodeExecutesAllFourSourcesInParallel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	registry, err := NewAgentRegistry(ctx, scriptedEinoModel{})
	if err != nil {
		t.Fatal(err)
	}
	group := &parallelCollectorGroup{release: make(chan struct{})}
	collectors := map[string]Collector{}
	for _, source := range []string{"metric", "log", "trace", "kubernetes"} {
		collectors[source] = parallelCollector{source: source, group: group}
	}
	supervisor, err := NewSupervisor(ctx, SupervisorDeps{Collectors: collectors, Historical: fixedCollector{source: "historical"}, Agents: registry, Executor: &graphExecutor{}, Checkpoints: &memoryEinoCheckpoint{data: map[string][]byte{}}})
	if err != nil {
		t.Fatal(err)
	}
	incident := &domain.Incident{ID: "incident-parallel", Status: domain.StatusReceived, Namespace: "kubepilot-demo", Service: "gateway-service", Resource: "gateway-service", Summary: "CPU alert", CreatedAt: time.Now().Add(-time.Minute), UpdatedAt: time.Now().Add(-time.Minute)}
	_, runErr := supervisor.Run(ctx, incident)
	if _, ok := compose.ExtractInterruptInfo(runErr); !ok {
		t.Fatalf("parallel evidence collection did not reach approval: %v", runErr)
	}
	if group.count.Load() != 4 {
		t.Fatalf("executed collectors=%d", group.count.Load())
	}
}

func TestEvidenceFusionRequiresUsableKubernetesAndTelemetryRecords(t *testing.T) {
	now := time.Now().UTC()
	state := &WorkflowState{
		Incident:     &domain.Incident{ID: "fusion", Status: domain.StatusCollecting, Namespace: "production", Service: "gateway", Resource: "gateway"},
		EvidencePlan: EvidencePlan{WindowStart: now.Add(-time.Minute), WindowEnd: now},
	}
	messages := make([]*schema.Message, 0, 4)
	for _, source := range []string{"metric", "log", "trace", "kubernetes"} {
		payload, err := json.Marshal(evidenceToolResult{Source: source, Evidence: []domain.Evidence{{Source: source, Type: source + "_evidence", Timestamp: now, Summary: source}}})
		if err != nil {
			t.Fatal(err)
		}
		messages = append(messages, &schema.Message{Content: string(payload)})
	}
	if err := mergeEvidenceToolMessages(state, messages); err != nil {
		t.Fatal(err)
	}
}
