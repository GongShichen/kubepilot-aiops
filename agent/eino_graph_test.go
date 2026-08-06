package agent

import (
	"context"
	"encoding/json"
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

type scriptedEinoModel struct{ reverseEvidence bool }

func (m scriptedEinoModel) Generate(_ context.Context, messages []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	options := model.GetCommonOptions(nil, opts...)
	available := map[string]bool{}
	for _, candidate := range options.Tools {
		available[candidate.Name] = true
	}
	name := ""
	for _, message := range messages {
		if message.Role == schema.User && strings.Contains(message.Content, `"task":"correlation"`) {
			name = "submit_correlation_decision"
		}
	}
	called := calledTools(messages)
	if name == "" {
		switch {
		case available[DiagnosisAgentName]:
			name = DiagnosisAgentName
		case available[RecoveryAgentName]:
			name = RecoveryAgentName
		case available["submit_supervisor_outcome"]:
			name = "submit_supervisor_outcome"
		case available["query_prometheus_evidence"] && !called["query_prometheus_evidence"]:
			calls := []string{"query_prometheus_evidence", "query_loki_evidence", "query_trace_evidence", "query_kubernetes_evidence"}
			if m.reverseEvidence {
				calls = []string{"query_kubernetes_evidence", "query_trace_evidence", "query_loki_evidence", "query_prometheus_evidence"}
			}
			return withMockUsage(toolCalls(calls, func(string) any { return map[string]any{"window_minutes": 5} })), nil
		case available["rank_incident_evidence"] && !called["rank_incident_evidence"]:
			name = "rank_incident_evidence"
		case available["build_incident_features"] && !called["build_incident_features"]:
			name = "build_incident_features"
		case available["retrieve_semantic_incidents"] && !called["retrieve_semantic_incidents"]:
			return withMockUsage(toolCalls([]string{"retrieve_semantic_incidents", "retrieve_lexical_incidents", "retrieve_topology_incidents"}, func(string) any { return map[string]any{"limit": 10} })), nil
		case available["fuse_incident_candidates"] && !called["fuse_incident_candidates"]:
			name = "fuse_incident_candidates"
		case available["rerank_incident_candidates"] && !called["rerank_incident_candidates"]:
			name = "rerank_incident_candidates"
		case available["match_causal_patterns"] && !called["match_causal_patterns"]:
			name = "match_causal_patterns"
		case available["submit_hypotheses"] && !called["submit_hypotheses"]:
			name = "submit_hypotheses"
		case available["verify_incident_hypotheses"] && !called["verify_incident_hypotheses"]:
			name = "verify_incident_hypotheses"
		case available["submit_diagnosis"] && !called["submit_diagnosis"]:
			name = "submit_diagnosis"
		case available["submit_recovery_proposal"] && !called["submit_recovery_proposal"]:
			name = "submit_recovery_proposal"
		case available["dry_run_recovery_proposal"] && !called["dry_run_recovery_proposal"]:
			name = "dry_run_recovery_proposal"
		case available["accept_recovery_proposal"] && !called["accept_recovery_proposal"]:
			name = "accept_recovery_proposal"
		default:
			if len(options.Tools) > 0 {
				name = options.Tools[0].Name
			} else {
				return withMockUsage(schema.AssistantMessage("structured terminal outcome completed", nil)), nil
			}
		}
	}
	arguments := map[string]any{}
	switch name {
	case "submit_correlation_decision":
		arguments = map[string]any{"merge": false, "confidence": .4, "reason": "insufficient operational linkage"}
	case "submit_hypotheses":
		evidenceIDs := firstEvidenceIDs(messages, 2)
		nodeIDs := []string(nil)
		if len(evidenceIDs) > 0 {
			nodeIDs = []string{"obs:" + evidenceIDs[0]}
		}
		arguments = map[string]any{"reasoning_type": "hypothesis_verification", "hypotheses": []map[string]any{{"id": "h1", "category": "cpu", "variant": "busy_loop", "cause": "CPU saturation", "service": "gateway-service", "resource": "gateway-service", "prior_probability": 1.0, "supporting_evidence_ids": evidenceIDs, "expected_causal_node_ids": nodeIDs, "falsification_conditions": []string{"CPU is normal"}}}}
	case "submit_diagnosis":
		arguments = map[string]any{"hypothesis_id": "h1"}
	case "submit_recovery_proposal":
		arguments = map[string]any{"action": "restart_pod", "target": "gateway-service", "parameters": map[string]any{}, "reason": "restore service", "risk": "brief disruption", "diff": "restart workload", "rollback": "wait for prior replica", "confidence": .9}
	case DiagnosisAgentName, RecoveryAgentName:
		arguments = map[string]any{"request": "complete the bounded specialist task for the current Incident"}
	case "submit_supervisor_outcome":
		arguments = map[string]any{"status": "AWAITING_APPROVAL", "reason": "specialist outputs satisfy the deterministic handoff"}
	default:
		arguments = map[string]any{}
	}
	raw, _ := json.Marshal(arguments)
	return withMockUsage(&schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "call-" + name, Type: "function", Function: schema.FunctionCall{Name: name, Arguments: string(raw)}}}}), nil
}

func withMockUsage(message *schema.Message) *schema.Message {
	message.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 80, CompletionTokens: 20, TotalTokens: 100}}
	return message
}

func toolCalls(names []string, arguments func(string) any) *schema.Message {
	calls := make([]schema.ToolCall, 0, len(names))
	for _, name := range names {
		raw, _ := json.Marshal(arguments(name))
		calls = append(calls, schema.ToolCall{ID: "call-" + name, Type: "function", Function: schema.FunctionCall{Name: name, Arguments: string(raw)}})
	}
	return &schema.Message{Role: schema.Assistant, ToolCalls: calls}
}
func calledTools(messages []*schema.Message) map[string]bool {
	out := map[string]bool{}
	for _, message := range messages {
		if message == nil {
			continue
		}
		for _, call := range message.ToolCalls {
			out[call.Function.Name] = true
		}
		if message.Role == schema.Tool && message.ToolName != "" {
			out[message.ToolName] = true
		}
	}
	return out
}

type supplementalCollector struct {
	source string
	calls  *atomic.Int32
}

func (c supplementalCollector) Collect(_ context.Context, in *domain.Incident, _ domain.EvidenceRequest) ([]domain.Evidence, error) {
	summary := c.source + " evidence CPU throttling timeout"
	if c.calls.Add(1) > 4 {
		summary += " supplemental_signal"
	}
	return []domain.Evidence{{Source: c.source, Kind: c.source + "_evidence", Summary: summary, Content: anomalousFixtureFacts(c.source), ObservedAt: time.Now().UTC(), Namespace: in.Namespace, Service: in.Service, Resource: in.Resource}}, nil
}

func (m scriptedEinoModel) Stream(ctx context.Context, messages []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, messages, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func firstEvidenceIDs(messages []*schema.Message, limit int) []string {
	for index := len(messages) - 1; index >= 0; index-- {
		var payload struct {
			Evidence []struct {
				ID string `json:"id"`
			} `json:"evidence"`
		}
		if json.Unmarshal([]byte(messages[index].Content), &payload) == nil && len(payload.Evidence) > 0 {
			out := make([]string, 0, limit)
			for _, item := range payload.Evidence {
				out = append(out, item.ID)
				if len(out) == limit {
					break
				}
			}
			return out
		}
	}
	return []string{"missing"}
}

type fixedCollector struct{ source string }

func (c fixedCollector) Collect(_ context.Context, in *domain.Incident, _ domain.EvidenceRequest) ([]domain.Evidence, error) {
	return []domain.Evidence{{Source: c.source, Kind: c.source + "_evidence", Summary: c.source + " evidence CPU throttling timeout", Content: anomalousFixtureFacts(c.source), ObservedAt: time.Now().UTC(), Namespace: in.Namespace, Service: in.Service, Resource: in.Resource}}, nil
}

func anomalousFixtureFacts(source string) map[string]any {
	switch source {
	case "prometheus":
		return map[string]any{"current_value": .99, "baseline_value": .20}
	case "loki":
		return map[string]any{"level": "error", "occurrence_count": 8}
	case "jaeger":
		return map[string]any{"error_service": "gateway-service", "failed_operation": "GET /"}
	case "kubernetes":
		return map[string]any{"pods": []any{map[string]any{"ready": false, "restart_count": 2}}}
	default:
		return map[string]any{"status": "failed"}
	}
}

type fixedHistoricalRetriever struct{}

func (fixedHistoricalRetriever) Semantic(_ context.Context, f domain.IncidentFeatures, _ int) ([]domain.RetrievalCandidate, error) {
	return []domain.RetrievalCandidate{{IncidentID: "history-1", Namespace: f.Namespace, Service: f.Service, Resource: f.Resource, Category: "cpu", RootCause: "CPU saturation", Features: domain.IncidentFeatures{Namespace: f.Namespace, Service: f.Service, Resource: f.Resource, Terms: f.Terms, TopologyServices: f.TopologyServices}}}, nil
}
func (fixedHistoricalRetriever) Lexical(_ context.Context, _ domain.IncidentFeatures, _ int) ([]domain.RetrievalCandidate, error) {
	return []domain.RetrievalCandidate{{IncidentID: "history-1", Category: "cpu", RootCause: "CPU saturation", SourceScores: map[string]float64{"lexical": .8}}}, nil
}
func (fixedHistoricalRetriever) Topology(_ context.Context, f domain.IncidentFeatures, _ int) ([]domain.RetrievalCandidate, error) {
	return []domain.RetrievalCandidate{{IncidentID: "history-1", Namespace: f.Namespace, Service: f.Service, Resource: f.Resource, Category: "cpu", RootCause: "CPU saturation", Features: domain.IncidentFeatures{Namespace: f.Namespace, Service: f.Service, Resource: f.Resource, Terms: f.Terms, TopologyServices: f.TopologyServices}}}, nil
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

func (c parallelCollector) Collect(ctx context.Context, in *domain.Incident, request domain.EvidenceRequest) ([]domain.Evidence, error) {
	if c.group.count.Add(1) == 4 {
		c.group.once.Do(func() { close(c.group.release) })
	}
	select {
	case <-c.group.release:
		return fixedCollector{source: c.source}.Collect(ctx, in, request)
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
	if names := registry.Names(); len(names) != 10 || names[0] != SupervisorAgentName || names[1] != PlannerAgentName || names[6] != DiagnosisAgentName || names[8] != CriticAgentName || names[9] != RecoveryAgentName {
		t.Fatalf("unexpected ADK agents: %v", names)
	}
	checkpoints := &memoryEinoCheckpoint{data: map[string][]byte{}}
	executor := &graphExecutor{}
	collectors := map[string]Collector{}
	for _, source := range []string{"metric", "log", "trace", "kubernetes"} {
		collectors[source] = fixedCollector{source: source}
	}
	supervisor, err := NewSupervisor(ctx, SupervisorDeps{Collectors: collectors, HistoricalCandidates: fixedHistoricalRetriever{}, Agents: registry, Executor: executor, Checkpoints: checkpoints, VerificationInterval: time.Millisecond, VerificationTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	incident := &domain.Incident{ID: "incident-eino", Status: domain.StatusReceived, Namespace: "kubepilot-demo", Service: "gateway-service", Resource: "gateway-service", Summary: "CPU alert", CreatedAt: time.Now().Add(-time.Minute), UpdatedAt: time.Now().Add(-time.Minute)}
	_, runErr := supervisor.Run(ctx, incident)
	interrupt, ok := compose.ExtractInterruptInfo(runErr)
	if !ok || len(interrupt.InterruptContexts) != 1 {
		t.Fatalf("expected one Eino interrupt, got %v; budget=%+v ledger=%+v", runErr, incident.AgentBudget, incident.DiagnosisLedger)
	}
	if incident.Status != domain.StatusAwaitingApproval || incident.DryRun == nil || incident.Proposal == nil {
		t.Fatalf("incident did not reach approval: %#v", incident)
	}
	if incident.Investigation == nil || len(incident.Investigation.ModelUsage) == 0 {
		t.Fatal("constrained ReAct model usage was not captured")
	}
	for _, usage := range incident.Investigation.ModelUsage {
		if usage.InputTokens <= 0 || usage.OutputTokens <= 0 || usage.DurationMS < 0 {
			t.Fatalf("incomplete per-Agent model usage: %+v", usage)
		}
	}
	resume := &ApprovalResumeData{Approved: true, Context: domain.ExecutionContext{NamespaceAllowlist: []string{"kubepilot-demo"}, IncidentID: incident.ID, ProposalID: incident.Proposal.ID, ApprovalID: "approval-1", IdempotencyKey: "key-1", Operator: "test", TargetUID: "uid-1", ResourceVersion: "rv-1", MutationSpecHash: "mutation-1", ApprovedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Minute)}}
	state, err := supervisor.Resume(ctx, incident.ID, interrupt.InterruptContexts[0].ID, resume)
	if err != nil {
		t.Fatal(err)
	}
	if state.Incident.Status != domain.StatusResolved || !executor.executed {
		t.Fatalf("status=%s executed=%v", state.Incident.Status, executor.executed)
	}
	if state.Incident.RecoveryExecution == nil || state.Incident.RecoveryExecution.Attempts != 1 || state.Incident.RecoveryExecution.ConfirmedMutations != 1 || state.Incident.RecoveryExecution.Namespace != incident.Namespace || state.Incident.RecoveryExecution.Outcome != "succeeded" {
		t.Fatalf("deterministic recovery audit was not recorded: %+v", state.Incident.RecoveryExecution)
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
	supervisor, err := NewSupervisor(ctx, SupervisorDeps{Collectors: collectors, HistoricalCandidates: fixedHistoricalRetriever{}, Agents: registry, Executor: &graphExecutor{}, Checkpoints: &memoryEinoCheckpoint{data: map[string][]byte{}}, VerificationInterval: time.Millisecond, VerificationTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	incident := &domain.Incident{ID: "incident-parallel", Status: domain.StatusReceived, Namespace: "kubepilot-demo", Service: "gateway-service", Resource: "gateway-service", Summary: "CPU alert", CreatedAt: time.Now().Add(-time.Minute), UpdatedAt: time.Now().Add(-time.Minute)}
	_, runErr := supervisor.Run(ctx, incident)
	if _, ok := compose.ExtractInterruptInfo(runErr); !ok {
		t.Fatalf("parallel evidence collection did not reach approval: %v incident=%#v", runErr, incident)
	}
	if group.count.Load() != 4 {
		t.Fatalf("executed collectors=%d", group.count.Load())
	}
}

func TestConstrainedAgentCanChooseDifferentEvidenceToolOrder(t *testing.T) {
	ctx := context.Background()
	registry, err := NewAgentRegistry(ctx, scriptedEinoModel{reverseEvidence: true})
	if err != nil {
		t.Fatal(err)
	}
	calls := &atomic.Int32{}
	collectors := map[string]Collector{}
	for _, source := range []string{"metric", "log", "trace", "kubernetes"} {
		collectors[source] = supplementalCollector{source: source, calls: calls}
	}
	supervisor, err := NewSupervisor(ctx, SupervisorDeps{Collectors: collectors, HistoricalCandidates: fixedHistoricalRetriever{}, Agents: registry, Executor: &graphExecutor{}, Checkpoints: &memoryEinoCheckpoint{data: map[string][]byte{}}, VerificationInterval: time.Millisecond, VerificationTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	incident := &domain.Incident{ID: "incident-supplement", Status: domain.StatusReceived, Namespace: "kubepilot-demo", Service: "gateway-service", Resource: "gateway-service", Summary: "CPU alert", CreatedAt: time.Now().Add(-time.Minute), UpdatedAt: time.Now().Add(-time.Minute)}
	state, runErr := supervisor.Run(ctx, incident)
	if _, ok := compose.ExtractInterruptInfo(runErr); !ok {
		t.Fatalf("supplemental collection did not reach approval interrupt: err=%v state=%#v incident=%#v", runErr, state, incident)
	}
	if calls.Load() != 4 || incident.Status != domain.StatusAwaitingApproval {
		t.Fatalf("autonomous collection calls=%d status=%s", calls.Load(), incident.Status)
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
