package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/kubepilot-aiops/kubepilot/internal/brainruntime"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	captools "github.com/kubepilot-aiops/kubepilot/tools"
)

type brainGraphModel struct {
	mu      sync.Mutex
	calls   int
	history []string
}

type staticBrainSkillRetriever struct{}

func (staticBrainSkillRetriever) Search(_ context.Context, query domain.SkillRetrievalQuery) (domain.SkillRetrievalResult, error) {
	result := domain.SkillRetrievalResult{QueryHash: "query", SnapshotHash: "snapshot", RetrievedAt: time.Now().UTC()}
	for _, document := range query.Documents {
		result.Results = append(result.Results, domain.SkillSearchResult{ID: document.ID, Version: document.Version, ContentHash: document.ContentHash, Description: document.Description, PhaseScore: 1, FinalScore: 1})
	}
	return result, nil
}

func seedRetrievedBrainSkills(state *WorkflowState, ids ...string) {
	result := domain.SkillRetrievalResult{
		QueryHash:    "test-query",
		SnapshotHash: "test-snapshot",
		RetrievedAt:  time.Now().UTC(),
	}
	for _, id := range ids {
		result.Results = append(result.Results, domain.SkillSearchResult{
			ID:          id,
			Version:     "test",
			ContentHash: "test:" + id,
			FinalScore:  1,
		})
	}
	state.SkillRetrievals = []domain.SkillRetrievalResult{result}
}

type recordingBrainHybridRetriever struct{ calls int }

func (r *recordingBrainHybridRetriever) Retrieve(_ context.Context, query domain.HybridRetrievalQuery) (domain.HybridRetrievalResult, error) {
	r.calls++
	return domain.HybridRetrievalResult{Channels: []domain.HybridRetrievalChannelResult{{Channel: domain.RetrievalBM25, Available: true}}, Final: []domain.RetrievalCandidate{{IncidentID: "history-a", Summary: "bounded historical context"}}, SnapshotHash: "hybrid-snapshot", RetrievedAt: time.Now().UTC()}, nil
}

// brainTurnBudgetModel deliberately keeps issuing valid native tool calls so
// the domain MaxTurns boundary, rather than an Eino graph-step guard or the
// structured-output correction budget, must end the Workflow Attempt.
type brainTurnBudgetModel struct {
	mu    sync.Mutex
	calls int
}

type failingBrainModel struct {
	mu    sync.Mutex
	calls int
}

func (m *failingBrainModel) Generate(_ context.Context, _ []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.calls > 1 {
		return nil, fmt.Errorf("synthetic provider failure")
	}
	available := map[string]bool{}
	for _, info := range model.GetCommonOptions(nil, opts...).Tools {
		available[info.Name] = true
	}
	if !available["submit_incident_understanding"] {
		return nil, fmt.Errorf("understanding tool is unavailable")
	}
	raw, _ := json.Marshal(map[string]any{
		"intent": "record bounded impact", "expected_observation": []string{"persisted understanding"},
		"summary": "gateway degraded", "affected_targets": []any{map[string]any{"namespace": "kubepilot-demo", "service": "gateway-service", "resource": "gateway-service", "kind": "Service"}},
		"possible_domains": []string{"application"}, "unknowns": []string{"mechanism"},
	})
	return withMockUsage(&schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "failure-understanding", Type: "function", Function: schema.FunctionCall{Name: "submit_incident_understanding", Arguments: string(raw)}}}}), nil
}

func (m *failingBrainModel) Stream(ctx context.Context, messages []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, messages, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func (m *brainTurnBudgetModel) Generate(_ context.Context, messages []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	options := model.GetCommonOptions(nil, opts...)
	available := map[string]bool{}
	for _, info := range options.Tools {
		available[info.Name] = true
	}
	var payload struct {
		Phase domain.BrainPhase `json:"phase"`
	}
	for _, message := range messages {
		if message.Role == schema.User {
			_ = json.Unmarshal([]byte(message.Content), &payload)
		}
	}
	call := func(name string, arguments map[string]any) (*schema.Message, error) {
		if !available[name] {
			return nil, fmt.Errorf("budget model requested unavailable tool %s in phase %s", name, payload.Phase)
		}
		raw, _ := json.Marshal(arguments)
		return withMockUsage(&schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: fmt.Sprintf("budget-call-%02d", m.calls), Type: "function", Function: schema.FunctionCall{Name: name, Arguments: string(raw)}}}}), nil
	}
	target := map[string]any{"namespace": "kubepilot-demo", "service": "gateway-service", "resource": "gateway-service", "kind": "Service"}
	switch payload.Phase {
	case domain.BrainPhaseIntake:
		return call("submit_incident_understanding", map[string]any{"intent": "record bounded impact", "expected_observation": []string{"persisted understanding"}, "summary": "gateway degraded", "affected_targets": []any{target}, "possible_domains": []string{"application"}, "unknowns": []string{"mechanism"}})
	case domain.BrainPhasePlanning:
		return call("submit_investigation_plan", map[string]any{"intent": "plan bounded investigation", "expected_observation": []string{"persisted plan"}, "objective": "exercise the configured Brain turn boundary", "goals": []string{"keep native structured actions auditable"}, "stop_conditions": []string{"Brain turn budget exhausted"}})
	default:
		return call("request_skills", map[string]any{"intent": fmt.Sprintf("retain a native structured action at turn %d", m.calls), "expected_observation": []string{"audited Skill activation status"}, "skill_ids": []string{"revise-hypotheses"}, "reason": fmt.Sprintf("exercise-turn-%d", m.calls), "trigger": "BUDGET_BOUNDARY_TEST"})
	}
}

func (m *brainTurnBudgetModel) Stream(ctx context.Context, messages []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, messages, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func (m *brainGraphModel) Generate(_ context.Context, messages []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	options := model.GetCommonOptions(nil, opts...)
	available := map[string]bool{}
	for _, info := range options.Tools {
		available[info.Name] = true
	}
	var payload struct {
		Phase      domain.BrainPhase          `json:"phase"`
		Evidence   []domain.BrainEvidenceView `json:"evidence_view"`
		Hypotheses []struct {
			ID              string  `json:"id"`
			ModelConfidence float64 `json:"model_confidence"`
		} `json:"hypotheses"`
		Groundings []domain.HypothesisGrounding `json:"groundings"`
		Diagnosis  *domain.AgentDiagnosis       `json:"diagnosis"`
		Recovery   *domain.AgentRecoveryPlan    `json:"recovery_plan"`
	}
	system := ""
	for _, message := range messages {
		if message.Role == schema.System {
			system += message.Content
		}
		if message.Role == schema.User {
			_ = json.Unmarshal([]byte(message.Content), &payload)
		}
	}
	target := map[string]any{"namespace": "kubepilot-demo", "service": "gateway-service", "resource": "gateway-service", "kind": "Service"}
	call := func(name string, arguments map[string]any) (*schema.Message, error) {
		if !available[name] {
			return nil, fmt.Errorf("script requested unavailable Brain tool %s in phase %s", name, payload.Phase)
		}
		m.history = append(m.history, string(payload.Phase)+":"+name)
		raw, _ := json.Marshal(arguments)
		message := &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: fmt.Sprintf("brain-call-%02d", m.calls), Type: "function", Function: schema.FunctionCall{Name: name, Arguments: string(raw)}}}}
		return withMockUsage(message), nil
	}
	hypothesisIDs := []string{}
	confidence := map[string]float64{}
	for _, hypothesis := range payload.Hypotheses {
		hypothesisIDs = append(hypothesisIDs, hypothesis.ID)
		confidence[hypothesis.ID] = hypothesis.ModelConfidence
	}
	evidenceIDs := make([]string, 0, len(payload.Evidence))
	for _, item := range payload.Evidence {
		evidenceIDs = append(evidenceIDs, item.ID)
	}
	sort.Strings(evidenceIDs)
	causalNodeIDs := []string{}
	for _, item := range payload.Evidence {
		causalNodeIDs = append(causalNodeIDs, item.CausalNodeIDs...)
	}
	switch payload.Phase {
	case domain.BrainPhaseIntake:
		return call("submit_incident_understanding", map[string]any{"intent": "establish the affected operational scope", "expected_observation": []string{"persisted structured incident understanding"}, "summary": "gateway degradation requires grounded investigation", "affected_targets": []any{target}, "possible_domains": []string{"application", "infrastructure"}, "unknowns": []string{"mechanism"}})
	case domain.BrainPhasePlanning:
		return call("submit_investigation_plan", map[string]any{"intent": "create a falsifiable investigation plan", "expected_observation": []string{"persisted investigation goals"}, "objective": "separate resource pressure from deployment regression", "goals": []string{"collect independent runtime and Kubernetes facts", "validate two competing hypotheses"}, "stop_conditions": []string{"two hypotheses validated against current evidence"}})
	case domain.BrainPhaseReflection:
		// Exercise the real repair path used when the Brain selected an Evidence
		// category before loading its phase-compatible Skill. The Reflection must
		// request that Skill, return to INVESTIGATION, activate it, and only then
		// select/execute the Evidence category.
		if len(payload.Evidence) == 0 && !strings.Contains(system, `<skill id="investigate-metrics"`) {
			return call("request_skills", map[string]any{"intent": "repair the denied evidence category with its bounded metric procedure", "expected_observation": []string{"phase-compatible metric Skill activation"}, "skill_ids": []string{"investigate-metrics"}, "reason": "resource pressure is the leading discriminating observation", "trigger": "CONSTRAINT_FAILURE"})
		}
		selected := ""
		for _, grounding := range payload.Groundings {
			if grounding.Level == domain.GroundingRefuted {
				selected = grounding.HypothesisRevisionID
			}
		}
		if selected == "" && len(hypothesisIDs) > 0 {
			selected = hypothesisIDs[0]
		}
		return call("commit_belief_delta", map[string]any{"intent": fmt.Sprintf("revise belief after %d current evidence records", len(payload.Evidence)), "expected_observation": []string{"audited subjective belief delta"}, "hypothesis_id": selected, "new_confidence": maxFloat(.05, confidence[selected]-.1), "direction": "decrease", "evidence_ids": evidenceIDs, "revision_required": false})
	case domain.BrainPhaseDiagnosis:
		if payload.Diagnosis != nil {
			return call("validate_diagnosis", map[string]any{"intent": "append Runtime grounding and snapshot validation", "expected_observation": []string{"validated or provisional diagnosis status"}, "diagnosis_id": payload.Diagnosis.ID})
		}
		grounding := payload.Groundings[0]
		return call("submit_diagnosis", map[string]any{"intent": "select the best grounded hypothesis", "expected_observation": []string{"persisted LLM diagnosis with separate Runtime grounding"}, "hypothesis_id": grounding.HypothesisRevisionID, "statement": "gateway is degraded by local resource pressure", "category": "resource", "mechanism": "resource pressure", "targets": []any{target}, "model_confidence": confidence[grounding.HypothesisRevisionID], "evidence_ids": grounding.Evidence.SupportingEvidenceIDs, "validation_result_ids": []string{grounding.ID}})
	case domain.BrainPhaseRecovery:
		if payload.Recovery == nil {
			return call("submit_recovery_plan", map[string]any{"intent": "propose a registered reversible recovery", "expected_observation": []string{"Safety Kernel validation of the frozen recovery plan"}, "goal": "restore gateway availability", "primary_action": map[string]any{"action": "restart_pod", "target": "gateway-service", "reason": "replace the unhealthy workload instance", "parameters": map[string]any{}}, "alternatives": []any{}, "expected_outcome": "gateway readiness and request health recover", "rollback_plan": "stop and escalate if replacement readiness does not converge", "verification_plan": "require three successful Kubernetes and telemetry verification rounds", "risk_reason": "brief workload disruption"})
		}
		return call("finish_investigation", map[string]any{"intent": "handoff grounded diagnosis and recovery plan to the Safety Kernel", "expected_observation": []string{"Runtime recovery eligibility decision"}, "reason": "DIAGNOSIS_CONFIDENT", "hypothesis_id": payload.Diagnosis.HypothesisRevisionID})
	case domain.BrainPhaseInvestigation:
		if len(hypothesisIDs) == 0 {
			return call("submit_hypotheses", map[string]any{"intent": "form two falsifiable competing hypotheses", "expected_observation": []string{"server admission decisions for both hypotheses"}, "hypotheses": []any{
				map[string]any{"statement": "gateway is degraded by local resource pressure", "category": "resource", "mechanism": "resource pressure", "targets": []any{target}, "evidence_needs": []string{"resource pressure signal"}, "falsification_conditions": []string{"runtime and Kubernetes resource facts remain normal"}, "model_confidence": .7},
				map[string]any{"statement": "gateway is degraded by a deployment regression", "category": "deployment", "mechanism": "deployment regression", "targets": []any{target}, "evidence_needs": []string{"deployment failure signal"}, "falsification_conditions": []string{"no current deployment or workload failure evidence"}, "model_confidence": .45},
			}})
		}
		hasMetric, hasKubernetes := false, false
		for _, item := range payload.Evidence {
			hasMetric = hasMetric || item.Source == "metric" || item.Source == "prometheus"
			hasKubernetes = hasKubernetes || item.Source == "kubernetes"
		}
		if !hasMetric {
			if available["query_metrics"] {
				return call("query_metrics", map[string]any{"intent": "test local resource pressure", "expected_observation": []string{"current resource saturation facts"}, "targets": []any{target}, "hypothesis_ids": hypothesisIDs, "evidence_need": []string{"resource pressure signal"}, "signal_kinds": []string{"cpu", "memory"}, "window_minutes": 5})
			}
			return call("select_tool_category", map[string]any{"intent": "activate the metric evidence boundary", "expected_observation": []string{"metric Skill activation and Evidence ToolsNode selection"}, "category": "EVIDENCE", "skill_ids": []string{"investigate-metrics"}, "reason": "resource pressure is a leading discriminating observation", "trigger": "HYPOTHESIS_CONFLICT"})
		}
		if !hasKubernetes {
			if available["inspect_kubernetes"] {
				return call("inspect_kubernetes", map[string]any{"intent": "obtain an independent Kubernetes source", "expected_observation": []string{"current workload readiness and restart facts"}, "targets": []any{target}, "hypothesis_ids": hypothesisIDs, "evidence_need": []string{"resource pressure signal"}, "signal_kinds": []string{"workload", "pod"}, "window_minutes": 5})
			}
			return call("select_tool_category", map[string]any{"intent": "activate the Kubernetes evidence boundary", "expected_observation": []string{"Kubernetes Skill activation and Evidence ToolsNode selection"}, "category": "EVIDENCE", "skill_ids": []string{"inspect-kubernetes"}, "reason": "automatic recovery requires an independent Kubernetes source", "trigger": "GROUNDING_GAP"})
		}
		grounded := map[string]domain.HypothesisGrounding{}
		for _, grounding := range payload.Groundings {
			grounded[grounding.HypothesisRevisionID] = grounding
		}
		if _, ok := grounded[hypothesisIDs[0]]; !ok {
			return call("validate_hypothesis", map[string]any{"intent": "validate resource pressure against both independent sources", "expected_observation": []string{"SUPPORTED or PARTIAL grounding"}, "hypothesis_id": hypothesisIDs[0], "supporting_evidence_ids": evidenceIDs, "expected_causal_node_ids": causalNodeIDs})
		}
		if _, ok := grounded[hypothesisIDs[1]]; !ok {
			return call("validate_hypothesis", map[string]any{"intent": "falsify deployment regression with current evidence", "expected_observation": []string{"REFUTED grounding for the competing hypothesis"}, "hypothesis_id": hypothesisIDs[1], "contradicting_evidence_ids": []string{evidenceIDs[0]}, "missing_observations": []string{"deployment failure signal"}})
		}
		return call("advance_brain_phase", map[string]any{"intent": "move to diagnosis after validating two competing hypotheses", "expected_observation": []string{"diagnosis synthesis Skill activation"}, "next_phase": "DIAGNOSIS"})
	default:
		return nil, fmt.Errorf("unexpected Brain phase %s", payload.Phase)
	}
}

func (m *brainGraphModel) Stream(ctx context.Context, messages []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, messages, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func TestKubePilotUsesSelfReflectiveBrainGraphAndFrozenGroundingChain(t *testing.T) {
	ctx := context.Background()
	brainModel := &brainGraphModel{}
	registry, err := NewAgentRegistry(ctx, brainModel)
	if err != nil {
		t.Fatal(err)
	}
	collectors := map[string]Collector{"metric": fixedCollector{source: "metric"}, "kubernetes": fixedCollector{source: "kubernetes"}}
	checkpoints := &memoryEinoCheckpoint{data: map[string][]byte{}}
	executor := &graphExecutor{}
	supervisor, err := NewSupervisor(ctx, SupervisorDeps{Collectors: collectors, Agents: registry, Executor: executor, Checkpoints: checkpoints, VerificationInterval: time.Millisecond, VerificationTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	incident := &domain.Incident{ID: "incident-brain", DiagnosisMethod: domain.DiagnosisMethodKubePilot, Status: domain.StatusReceived, Namespace: "kubepilot-demo", Service: "gateway-service", Resource: "gateway-service", Summary: "gateway degraded", CreatedAt: time.Now().Add(-time.Minute), UpdatedAt: time.Now().Add(-time.Minute)}
	state, runErr := supervisor.Run(ctx, incident)
	interrupt, ok := compose.ExtractInterruptInfo(runErr)
	if !ok || len(interrupt.InterruptContexts) != 1 {
		t.Fatalf("Brain workflow did not reach approval: err=%v state=%+v history=%v", runErr, state, brainModel.history)
	}
	if incident.Investigation == nil || incident.Investigation.Architecture != "eino-native-self-reflective-brain" {
		t.Fatalf("kubepilot did not persist the Brain architecture audit: %+v", incident.Investigation)
	}
	if incident.Investigation.Arbitration != nil {
		t.Fatalf("KubePilot persisted deterministic baseline arbitration: %+v", incident.Investigation.Arbitration)
	}
	inv := incident.Investigation
	if len(inv.AgentHypotheses) != 2 || len(inv.HypothesisGroundings) != 2 || len(inv.Reflections) == 0 || inv.AgentDiagnosis == nil || inv.AgentDiagnosis.Provisional {
		t.Fatalf("incomplete Brain audit chain: hypotheses=%d groundings=%d reflections=%d diagnosis=%+v", len(inv.AgentHypotheses), len(inv.HypothesisGroundings), len(inv.Reflections), inv.AgentDiagnosis)
	}
	if len(inv.AssistantTurns) != len(inv.BrainTurns) {
		t.Fatalf("Assistant provider audit did not follow Brain turns: assistant=%d brain=%d", len(inv.AssistantTurns), len(inv.BrainTurns))
	}
	for _, turn := range inv.AssistantTurns {
		if turn.TurnID == "" || turn.ObservedAt.IsZero() || !turn.Persisted || !turn.ToolCallPresent {
			t.Fatalf("incomplete Assistant turn audit: %+v", turn)
		}
	}
	if inv.Termination == nil || inv.Termination.Reason != domain.TerminationDiagnosisConfident || inv.WorkflowAttempt == nil || inv.WorkflowAttempt.Status != domain.WorkflowAttemptInterrupted {
		t.Fatalf("unexpected pre-approval termination/attempt: termination=%+v attempt=%+v history=%v", inv.Termination, inv.WorkflowAttempt, brainModel.history)
	}
	for _, execution := range inv.ToolExecutions {
		if execution.Result.Provenance.ToolCallID == "" || execution.Result.Provenance.ToolName == "" || execution.Result.Provenance.ToolSchemaHash == "" || execution.Result.Provenance.ObservedAt.IsZero() || len(execution.Result.Provenance.TargetRefs) == 0 {
			t.Fatalf("incomplete Tool Result provenance: %+v", execution.Result.Provenance)
		}
	}
	resume := &ApprovalResumeData{Approved: true, Context: domain.ExecutionContext{NamespaceAllowlist: []string{incident.Namespace}, IncidentID: incident.ID, ProposalID: incident.Proposal.ID, ApprovalID: "brain-approval", IdempotencyKey: "brain-idempotency", Operator: "test", TargetUID: incident.Proposal.TargetUID, ResourceVersion: incident.Proposal.ResourceVersion, MutationSpecHash: incident.DryRun.MutationSpecHash, ApprovedAt: time.Now().UTC(), ExpiresAt: incident.Proposal.ExpiresAt}}
	final, err := supervisor.Resume(ctx, incident.ID, interrupt.InterruptContexts[0].ID, resume)
	if err != nil {
		t.Fatal(err)
	}
	if final.Incident.Status != domain.StatusResolved || final.Termination == nil || final.Termination.Reason != domain.TerminationRecoverySucceeded || final.WorkflowAttempt.Status != domain.WorkflowAttemptCompleted {
		t.Fatalf("unexpected final Brain state: status=%s termination=%+v attempt=%+v", final.Incident.Status, final.Termination, final.WorkflowAttempt)
	}
	if final.DiagnosisRuntime != nil || len(final.Candidates) != 0 || len(final.HypothesisDrafts) != 0 || len(final.VerifiedHypotheses) != 0 || final.Incident.Investigation.Arbitration != nil || final.Incident.DiagnosisLedger != nil {
		t.Fatalf("KubePilot entered a deterministic baseline path: runtime=%+v candidates=%d drafts=%d verified=%d arbitration=%+v", final.DiagnosisRuntime, len(final.Candidates), len(final.HypothesisDrafts), len(final.VerifiedHypotheses), final.Incident.Investigation.Arbitration)
	}
}

func TestBrainGraphStepBudgetAllowsDomainMaxTurnsToTerminate(t *testing.T) {
	ctx := context.Background()
	brainModel := &brainTurnBudgetModel{}
	registry, err := NewAgentRegistry(ctx, brainModel)
	if err != nil {
		t.Fatal(err)
	}
	supervisor, err := NewSupervisor(ctx, SupervisorDeps{Agents: registry, Executor: &graphExecutor{}, Checkpoints: &memoryEinoCheckpoint{data: map[string][]byte{}}})
	if err != nil {
		t.Fatal(err)
	}
	incident := &domain.Incident{ID: "incident-turn-boundary", DiagnosisMethod: domain.DiagnosisMethodKubePilot, Status: domain.StatusReceived, Namespace: "kubepilot-demo", Service: "gateway-service", Resource: "gateway-service", Summary: "gateway degraded", CreatedAt: time.Now().Add(-time.Minute), UpdatedAt: time.Now().Add(-time.Minute)}
	state, runErr := supervisor.Run(ctx, incident)
	if runErr != nil {
		t.Fatalf("Eino graph guard preempted the domain Brain budget: %v", runErr)
	}
	if state == nil || state.Termination == nil || state.Termination.Reason != domain.TerminationBudgetExhausted {
		t.Fatalf("domain MaxTurns did not create the termination event: %+v", state)
	}
	if state.BrainBudget.Usage.Turns != brainruntime.DefaultMaxTurns || len(state.BrainTurns) != brainruntime.DefaultMaxTurns {
		t.Fatalf("unexpected Brain turn usage at termination: usage=%+v turns=%d", state.BrainBudget.Usage, len(state.BrainTurns))
	}
	if state.Incident.Investigation == nil || state.Incident.Investigation.CompletedAt.IsZero() || state.WorkflowAttempt == nil || state.WorkflowAttempt.Status != domain.WorkflowAttemptCompleted || state.WorkflowAttempt.CompletedAt.IsZero() {
		t.Fatalf("MaxTurns termination did not persist a complete audit: investigation=%+v attempt=%+v", state.Incident.Investigation, state.WorkflowAttempt)
	}
}

func TestFatalGraphFailureFinalizesBrainAuditWithoutChangingDiagnosis(t *testing.T) {
	now := time.Now().UTC()
	diagnosis := &domain.AgentDiagnosis{ID: "diagnosis-partial", Statement: "LLM-owned partial diagnosis", ModelConfidence: .4}
	attempt := &domain.WorkflowAttempt{ID: "attempt-failure", IncidentID: "incident-failure", Sequence: 1, Status: domain.WorkflowAttemptActive, StartedAt: now.Add(-time.Minute)}
	state := &WorkflowState{
		Incident: &domain.Incident{ID: "incident-failure", DiagnosisMethod: domain.DiagnosisMethodKubePilot, Investigation: &domain.Investigation{Architecture: "eino-native-self-reflective-brain", StartedAt: now.Add(-time.Minute)}},
		BrainState: BrainState{
			AgentDiagnosis:       diagnosis,
			WorkflowAttempt:      attempt,
			BrainBudget:          domain.BrainBudgetState{Limits: brainruntime.DefaultBudget(), Usage: domain.BrainBudgetUsage{Turns: 7, ToolCalls: 6}},
			EvidenceSnapshotHash: "evidence-snapshot",
			ExecutionSnapshot:    domain.ExecutionSnapshot{SkillSnapshotHash: "skills", ModelConfigHash: "model", ToolSchemaHash: "tools", PolicyHash: "policy"},
		},
	}
	(&brainGraphRuntime{}).finalizeGraphFailure(state)
	if state.Termination == nil || state.Termination.Reason != domain.TerminationFatalInfrastructure {
		t.Fatalf("fatal graph failure missing explicit termination: %+v", state.Termination)
	}
	if state.AgentDiagnosis != diagnosis || state.AgentDiagnosis.Statement != "LLM-owned partial diagnosis" || state.AgentDiagnosis.ModelConfidence != .4 {
		t.Fatalf("Runtime rewrote the LLM diagnosis while recording failure: %+v", state.AgentDiagnosis)
	}
	if state.Incident.Investigation.CompletedAt.IsZero() || state.Incident.Investigation.Termination == nil || state.Incident.Investigation.Termination.Reason != domain.TerminationFatalInfrastructure {
		t.Fatalf("fatal graph failure left Investigation audit incomplete: %+v", state.Incident.Investigation)
	}
	if attempt.Status != domain.WorkflowAttemptCompleted || attempt.CompletedAt.IsZero() || state.Incident.WorkflowAttempt != attempt {
		t.Fatalf("fatal graph failure left Workflow Attempt active: %+v", attempt)
	}
}

func TestSupervisorReturnsFinalizedBrainStateOnGraphFailure(t *testing.T) {
	ctx := context.Background()
	registry, err := NewAgentRegistry(ctx, &failingBrainModel{})
	if err != nil {
		t.Fatal(err)
	}
	supervisor, err := NewSupervisor(ctx, SupervisorDeps{Agents: registry, Executor: &graphExecutor{}, Checkpoints: &memoryEinoCheckpoint{data: map[string][]byte{}}})
	if err != nil {
		t.Fatal(err)
	}
	incident := &domain.Incident{ID: "incident-runtime-failure", DiagnosisMethod: domain.DiagnosisMethodKubePilot, Status: domain.StatusReceived, Namespace: "kubepilot-demo", Service: "gateway-service", Resource: "gateway-service", Summary: "gateway degraded", CreatedAt: time.Now().Add(-time.Minute), UpdatedAt: time.Now().Add(-time.Minute)}
	state, runErr := supervisor.Run(ctx, incident)
	if runErr == nil || !strings.Contains(runErr.Error(), "synthetic provider failure") {
		t.Fatalf("expected provider failure, got %v", runErr)
	}
	if state == nil || state.Incident != incident {
		t.Fatalf("Supervisor discarded partial WorkflowState on graph failure: %+v", state)
	}
	if state.Termination == nil || state.Termination.Reason != domain.TerminationFatalInfrastructure || incident.Investigation == nil || incident.Investigation.CompletedAt.IsZero() || incident.Investigation.Termination == nil {
		t.Fatalf("graph failure audit is incomplete: termination=%+v investigation=%+v", state.Termination, incident.Investigation)
	}
	if state.WorkflowAttempt == nil || state.WorkflowAttempt.Status != domain.WorkflowAttemptCompleted || state.WorkflowAttempt.CompletedAt.IsZero() || incident.WorkflowAttempt != state.WorkflowAttempt {
		t.Fatalf("graph failure left the Workflow Attempt active: %+v", state.WorkflowAttempt)
	}
}

func TestNoReflectionAblationBypassesPendingReflection(t *testing.T) {
	trigger := domain.ReflectionHypothesisRefuted
	state := &WorkflowState{
		Incident: &domain.Incident{ID: "no-reflection", DiagnosisMethod: domain.DiagnosisMethodKubePilotNoReflection},
		BrainState: BrainState{
			PendingReflection: &trigger,
			BrainBudget:       domain.BrainBudgetState{Limits: brainruntime.DefaultBudget()},
		},
	}
	next, err := (&brainGraphRuntime{}).reflectionRoute(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if next != "brain_termination_router" || state.PendingReflection != nil || len(state.Reflections) != 0 || state.BrainBudget.Usage.ReflectionCostUnits != 0 {
		t.Fatalf("no-reflection ablation executed reflection: next=%s state=%+v", next, state)
	}
}

func TestGroundingConflictSchedulesReflectionWithoutChangingModelBelief(t *testing.T) {
	state := &WorkflowState{
		Incident: &domain.Incident{ID: "grounding-conflict", DiagnosisMethod: domain.DiagnosisMethodKubePilot, Investigation: &domain.Investigation{}},
		BrainState: BrainState{
			AgentHypotheses: []domain.AgentHypothesis{{ID: "h1", ModelConfidence: .8}},
			GroundingDeltas: []domain.GroundingDelta{{HypothesisRevisionID: "h1", PreviousLevel: domain.GroundingSupported, CurrentLevel: domain.GroundingPartial, ConflictDetected: true}},
			BrainBudget:     domain.BrainBudgetState{Limits: brainruntime.DefaultBudget()},
		},
	}
	if _, err := (&brainGraphRuntime{}).beliefUpdate(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	if state.PendingReflection == nil || *state.PendingReflection != domain.ReflectionCandidateConflict {
		t.Fatalf("objective grounding conflict did not schedule reflection: %+v", state.PendingReflection)
	}
	if state.AgentHypotheses[0].ModelConfidence != .8 {
		t.Fatalf("Runtime grounding changed subjective model confidence: %+v", state.AgentHypotheses[0])
	}
	next, err := (&brainGraphRuntime{}).reflectionRoute(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if next != "reflection_context_builder" || len(state.Reflections) != 1 || state.Reflections[0].Trigger != domain.ReflectionCandidateConflict {
		t.Fatalf("grounding conflict did not enter the explicit reflection boundary: next=%s records=%+v", next, state.Reflections)
	}
}

func TestReflectionRouteStopsAtBrainTurnBudget(t *testing.T) {
	trigger := domain.ReflectionGroundingFailure
	budget := brainruntime.DefaultBudget()
	state := &WorkflowState{
		Incident: &domain.Incident{ID: "reflection-budget", DiagnosisMethod: domain.DiagnosisMethodKubePilot, Investigation: &domain.Investigation{}},
		BrainState: BrainState{
			PendingReflection:    &trigger,
			BrainBudget:          domain.BrainBudgetState{Limits: budget, Usage: domain.BrainBudgetUsage{Turns: budget.MaxTurns}},
			ExecutionSnapshot:    domain.ExecutionSnapshot{SkillSnapshotHash: "skills", ModelConfigHash: "model", ToolSchemaHash: "tools", PolicyHash: "policy"},
			EvidenceSnapshotHash: "evidence",
		},
	}
	next, err := (&brainGraphRuntime{}).reflectionRoute(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if next != "brain_termination_router" || state.Termination == nil || state.Termination.Reason != domain.TerminationBudgetExhausted {
		t.Fatalf("reflection bypassed Brain turn budget: next=%s termination=%+v", next, state.Termination)
	}
	if state.PendingReflection != nil || len(state.Reflections) != 0 || state.BrainBudget.Usage.ReflectionCostUnits != 0 {
		t.Fatalf("budget-exhausted reflection still consumed state: %+v", state)
	}
}

func TestVerificationFailureSchedulesOneBudgetedBrainReflection(t *testing.T) {
	executor := &sequenceVerificationExecutor{samples: []bool{false}}
	state := &WorkflowState{
		Incident: &domain.Incident{ID: "verification-reflection", DiagnosisMethod: domain.DiagnosisMethodKubePilot, Status: domain.StatusVerifying, Investigation: &domain.Investigation{Architecture: "eino-native-self-reflective-brain"}},
		BrainState: BrainState{
			AgentHypotheses: []domain.AgentHypothesis{{ID: "h1", ModelConfidence: .8}},
			BrainBudget:     domain.BrainBudgetState{Limits: brainruntime.DefaultBudget()},
			Termination:     &domain.TerminationEvent{Reason: domain.TerminationDiagnosisConfident},
		},
		VerificationState: VerificationState{StartedAt: time.Now().UTC().Add(-time.Second)},
	}
	transition := func(_ context.Context, incident *domain.Incident, status domain.IncidentStatus) error {
		return domain.Transition(incident, status)
	}
	result, err := runVerificationController(context.Background(), state, SupervisorDeps{Executor: executor, VerificationInterval: time.Millisecond, VerificationTimeout: time.Millisecond}, transition)
	if err != nil {
		t.Fatal(err)
	}
	if result.PendingReflection == nil || *result.PendingReflection != domain.ReflectionVerificationFail || result.PendingTermination != domain.TerminationRecoveryFailed || result.Termination != nil {
		t.Fatalf("verification failure did not schedule a bounded Brain reflection: %+v", result)
	}
	result.BrainPhase = domain.BrainPhaseReflection
	result.Reflections = append(result.Reflections, domain.ReflectionRecord{ID: "reflection-1", Trigger: domain.ReflectionVerificationFail})
	(&brainGraphRuntime{}).applyCapabilityOutput(result, brainCapabilityOutput{Class: domain.ToolResultValidation, Summary: "belief updated after confirmed recovery failure", BeliefDelta: &domain.BeliefDelta{HypothesisRevisionID: "h1", PreviousConfidence: .8, NewConfidence: .2, Committed: true}})
	if _, err = (&brainGraphRuntime{}).beliefCommit(context.Background(), result); err != nil {
		t.Fatal(err)
	}
	if result.AgentHypotheses[0].ModelConfidence != .2 {
		t.Fatalf("explicit belief_commit did not update model confidence: %+v", result.AgentHypotheses[0])
	}
	if result.Termination == nil || result.Termination.Reason != domain.TerminationRecoveryFailed || result.PendingTermination != "" {
		t.Fatalf("post-failure reflection did not terminate safely: %+v", result.Termination)
	}
}

func TestUnresolvedHypothesisCannotAuthorizeRecovery(t *testing.T) {
	snapshot := domain.ExecutionSnapshot{SkillSnapshotHash: "skill", ModelConfigHash: "model", ToolSchemaHash: "tool", PolicyHash: "policy"}
	selected := domain.AgentHypothesis{ID: "h1", LastValidatedSnapshotHash: "evidence", Status: domain.HypothesisSupported}
	state := &WorkflowState{
		Incident: &domain.Incident{ID: "unresolved", DiagnosisMethod: domain.DiagnosisMethodKubePilot, Status: domain.StatusCollecting, Namespace: "team-a", Resource: "api", Investigation: &domain.Investigation{}},
		BrainState: BrainState{
			AgentHypotheses: []domain.AgentHypothesis{selected, {ID: "h2", LastValidatedSnapshotHash: "evidence", Status: domain.HypothesisInvestigating}},
			HypothesisAdmissions: []domain.HypothesisAdmission{
				{HypothesisRevisionID: "h1", Decision: "ADMITTED", GroundingLevel: domain.AdmissionUnresolved},
				{HypothesisRevisionID: "h2", Decision: "ADMITTED", GroundingLevel: domain.AdmissionDirect},
			},
			HypothesisGroundings: []domain.HypothesisGrounding{
				{HypothesisRevisionID: "h1", Level: domain.GroundingSupported, EvidenceSnapshotHash: "evidence", Evidence: domain.GroundingEvidence{EvidenceSupport: .9, IndependentSourceCount: 2}, Coverage: domain.GroundingCoverage{EvidenceNeedCoverage: 1, TargetScopeCoverage: 1, TemporalCoverage: 1}},
				{HypothesisRevisionID: "h2", Level: domain.GroundingPartial, EvidenceSnapshotHash: "evidence", Evidence: domain.GroundingEvidence{EvidenceSupport: .6}},
			},
			AgentDiagnosis:       &domain.AgentDiagnosis{ID: "d1", HypothesisRevisionID: "h1", EvidenceSnapshotHash: "evidence", ExecutionSnapshot: snapshot},
			AgentRecoveryPlan:    &domain.AgentRecoveryPlan{ID: "r1"},
			ExecutionSnapshot:    snapshot,
			EvidenceSnapshotHash: "evidence",
		},
	}
	transition := func(_ context.Context, incident *domain.Incident, status domain.IncidentStatus) error {
		return domain.Transition(incident, status)
	}
	result, err := brainRecoveryPermissionNode(context.Background(), state, transition)
	if err != nil {
		t.Fatal(err)
	}
	permission := result.Incident.Investigation.RecoveryPermission
	if permission == nil || permission.Allowed || result.Incident.Proposal != nil || !strings.Contains(permission.Reason, "hypothesis_admission_not_recovery_eligible") {
		t.Fatalf("UNRESOLVED hypothesis crossed the recovery boundary: permission=%+v proposal=%+v", permission, result.Incident.Proposal)
	}
}

func TestBrainCannotForgeRuntimeOwnedTerminationOutcome(t *testing.T) {
	state := &WorkflowState{Incident: &domain.Incident{ID: "termination", DiagnosisMethod: domain.DiagnosisMethodKubePilot}, BrainState: BrainState{BrainToolPolicy: brainruntime.DefaultToolCallingPolicy()}}
	if code, _ := validateBrainTerminationRequest(state, finishBrainInput{Reason: domain.TerminationRecoverySucceeded}); code != "runtime_owned_termination_reason" {
		t.Fatalf("model-forged recovery outcome was not rejected: %q", code)
	}
	state.AgentDiagnosis = &domain.AgentDiagnosis{HypothesisRevisionID: "h1", Provisional: false}
	if code, reason := validateBrainTerminationRequest(state, finishBrainInput{Reason: domain.TerminationDiagnosisConfident, HypothesisID: "h1"}); code != "" {
		t.Fatalf("valid diagnosis termination rejected: %s %s", code, reason)
	}
	if code, _ := validateBrainTerminationRequest(state, finishBrainInput{Reason: domain.TerminationEvidenceSaturated}); code != "evidence_not_saturated" {
		t.Fatalf("premature evidence saturation was not rejected: %q", code)
	}
}

func TestToolCallBudgetExhaustionRoutesToBrainFinalization(t *testing.T) {
	budget := brainruntime.DefaultBudget()
	trigger := domain.ReflectionCriticalEvidence
	state := &WorkflowState{
		Incident: &domain.Incident{ID: "tool-budget", DiagnosisMethod: domain.DiagnosisMethodKubePilot, Investigation: &domain.Investigation{}},
		BrainState: BrainState{
			BrainPhase:         domain.BrainPhaseInvestigation,
			ActiveToolCategory: domain.BrainToolEvidence,
			BrainToolPolicy:    brainruntime.DefaultToolCallingPolicy(),
			BrainBudget: domain.BrainBudgetState{
				Limits: budget,
				Usage:  domain.BrainBudgetUsage{Turns: 10, ToolCalls: budget.MaxToolCalls},
			},
			PendingReflection: &trigger,
		},
	}
	message := &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "over-budget", Type: "function", Function: schema.FunctionCall{Name: "query_metrics", Arguments: `{}`}}}}
	result, err := (&brainGraphRuntime{}).actionGateway(withBrainWorkflowState(context.Background(), state), message)
	if err != nil {
		t.Fatal(err)
	}
	if state.Termination != nil || !state.BrainBudget.ToolCallsExhausted || len(result.ToolCalls) != 1 {
		t.Fatalf("ToolCall exhaustion terminated or erased the Brain action: state=%+v message=%+v", state.Termination, result)
	}
	decision := authorizeBrainTool(state, domain.AgentActionEnvelope{
		ActionID: "over-budget", IncidentID: state.Incident.ID, ToolName: "query_metrics", ToolCategory: domain.BrainToolEvidence,
		Intent: domain.AgentActionIntent{Intent: "collect more metrics", ExpectedObservation: []string{"metric evidence"}},
	})
	if decision == nil || decision.ConstraintCode != "tool_call_budget_exhausted" {
		t.Fatalf("post-budget investigation call was not explicitly constrained: %+v", decision)
	}
	route, err := (&brainGraphRuntime{}).reflectionRoute(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if route != "brain_termination_router" || state.Termination != nil || state.PendingReflection != nil || state.BrainPhase != domain.BrainPhaseDiagnosis || state.ActiveToolCategory != domain.BrainToolReasoning {
		t.Fatalf("ToolCall exhaustion did not route to diagnosis finalization: route=%s state=%+v", route, state)
	}
}

func TestToolCallBudgetAllowsAuditedClosingActionsWithoutOverflow(t *testing.T) {
	budget := brainruntime.DefaultBudget()
	state := &WorkflowState{
		Incident: &domain.Incident{ID: "tool-budget-close", DiagnosisMethod: domain.DiagnosisMethodKubePilot, Investigation: &domain.Investigation{}},
		BrainState: BrainState{
			BrainPhase:            domain.BrainPhaseDiagnosis,
			ActiveToolCategory:    domain.BrainToolReasoning,
			ActiveSkillCategories: []domain.BrainToolCategory{domain.BrainToolReasoning},
			BrainToolPolicy:       brainruntime.DefaultToolCallingPolicy(),
			BrainBudget: domain.BrainBudgetState{
				Limits:             budget,
				Usage:              domain.BrainBudgetUsage{Turns: 10, ToolCalls: budget.MaxToolCalls},
				ToolCallsExhausted: true,
			},
			ExecutionSnapshot: domain.ExecutionSnapshot{ToolSchemaHash: "schema-hash"},
		},
	}
	envelope := domain.AgentActionEnvelope{
		ActionID: "close", IncidentID: state.Incident.ID, ToolName: "submit_diagnosis", ToolCategory: domain.BrainToolReasoning,
		Intent: domain.AgentActionIntent{Intent: "finalize from existing evidence", ExpectedObservation: []string{"persisted diagnosis"}},
	}
	if denied := authorizeBrainTool(state, envelope); denied != nil {
		t.Fatalf("closing diagnosis action was rejected after ToolCall exhaustion: %+v", denied)
	}
	output := brainCapabilityOutput{Class: domain.ToolResultValidation, Status: "OK", Summary: "diagnosis persisted", Provenance: domain.ToolResultProvenance{Collector: "brain-runtime", ObservedAt: time.Now().UTC()}}
	raw, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	message := &schema.Message{Role: schema.Tool, ToolCallID: envelope.ActionID, ToolName: envelope.ToolName, Content: string(raw)}
	if _, err = (&brainGraphRuntime{}).classifyToolResults(withBrainWorkflowState(context.Background(), state), []*schema.Message{message}); err != nil {
		t.Fatal(err)
	}
	if state.BrainBudget.Usage.ToolCalls != budget.MaxToolCalls || len(state.ToolExecutions) != 1 {
		t.Fatalf("closing action overflowed ToolCall usage or was not audited: budget=%+v executions=%+v", state.BrainBudget, state.ToolExecutions)
	}
}

func TestToolCallBudgetFinalizationContextUsesExistingState(t *testing.T) {
	resolver, err := LoadDefaultBrainSkillResolver()
	if err != nil {
		t.Fatal(err)
	}
	budget := brainruntime.DefaultBudget()
	state := &WorkflowState{
		Incident: &domain.Incident{
			ID: "tool-budget-context", DiagnosisMethod: domain.DiagnosisMethodKubePilot,
			Investigation: &domain.Investigation{Architecture: "eino-native-self-reflective-brain"},
		},
		BrainState: BrainState{
			BrainPhase:         domain.BrainPhaseInvestigation,
			ActiveToolCategory: domain.BrainToolEvidence,
			BrainBudget: domain.BrainBudgetState{
				Limits: budget,
				Usage:  domain.BrainBudgetUsage{Turns: 10, ToolCalls: budget.MaxToolCalls},
			},
		},
	}
	runtime := &brainGraphRuntime{resolver: resolver, toolHash: "tool-schema", policyHash: "policy"}
	messages, err := runtime.contextBuilder(context.Background(), state, false)
	if err != nil {
		t.Fatal(err)
	}
	if state.BrainPhase != domain.BrainPhaseDiagnosis || state.ActiveToolCategory != domain.BrainToolReasoning || !state.BrainBudget.ToolCallsExhausted {
		t.Fatalf("context did not enter diagnosis finalization: %+v", state)
	}
	if len(messages) < 2 || !strings.Contains(messages[0].Content, "ToolCall budget is exhausted") || !strings.Contains(messages[len(messages)-1].Content, `"tool_calls_exhausted":true`) {
		t.Fatalf("model was not told to conclude from existing state: %+v", messages)
	}
}

func TestBrainContextBindsMandatorySkillActivationsToAllocatedTurn(t *testing.T) {
	resolver, err := LoadDefaultBrainSkillResolver()
	if err != nil {
		t.Fatal(err)
	}
	state := &WorkflowState{
		Incident:   &domain.Incident{ID: "mandatory-skill-turn", DiagnosisMethod: domain.DiagnosisMethodKubePilot, Namespace: "team-a", Service: "api", Resource: "api", Investigation: &domain.Investigation{}},
		BrainState: BrainState{BrainBudget: domain.BrainBudgetState{Limits: brainruntime.DefaultBudget()}},
	}
	runtime := &brainGraphRuntime{resolver: resolver, toolHash: "tool-schema", policyHash: "policy"}
	if _, err = runtime.contextBuilder(context.Background(), state, false); err != nil {
		t.Fatal(err)
	}
	if len(state.BrainTurns) != 1 || state.BrainTurns[0].ID == "" {
		t.Fatalf("context did not allocate exactly one Brain Turn: %+v", state.BrainTurns)
	}
	turnID := state.BrainTurns[0].ID
	mandatory := 0
	for _, activation := range state.SkillActivations {
		if activation.RequestedBy != "RUNTIME" {
			continue
		}
		mandatory++
		if activation.RequestedTurn != turnID || activation.Version == "" || activation.ContentHash == "" || activation.Status != "ACTIVATED" {
			t.Fatalf("mandatory Skill activation is not replayable from its Brain Turn: turn=%s activation=%+v", turnID, activation)
		}
	}
	if mandatory == 0 {
		t.Fatalf("context produced no mandatory Skill activation audit: %+v", state.SkillActivations)
	}
}

func TestBrainContextUsesEvidenceViewWithoutCanonicalFacts(t *testing.T) {
	resolver, err := LoadDefaultBrainSkillResolver()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	evidence := domain.Evidence{
		ID: "evidence-1", Source: "kubernetes", Type: "kubernetes_evidence", Namespace: "team-a", Service: "api", Resource: "api",
		ObservedAt: now, Summary: "api workload is not ready",
		Content: map[string]any{"raw_secret_fact": "must-not-enter-model-context"},
		Facts:   map[string]any{"canonical_internal_fact": "runtime-only"},
		Signals: []domain.EvidenceSignal{{ID: "signal-1", EvidenceID: "evidence-1", Source: "kubernetes", Signal: "workload_unavailable", Direction: "abnormal", ObservedAt: now}},
	}
	state := &WorkflowState{
		Incident: &domain.Incident{ID: "evidence-view-context", DiagnosisMethod: domain.DiagnosisMethodKubePilot, Namespace: "team-a", Service: "api", Resource: "api", Evidence: []domain.Evidence{evidence}, Investigation: &domain.Investigation{Architecture: "eino-native-self-reflective-brain"}},
		BrainState: BrainState{
			BrainBudget:          domain.BrainBudgetState{Limits: brainruntime.DefaultBudget()},
			ToolExecutions:       []domain.BrainToolExecution{{Result: domain.ToolResultRecord{Provenance: domain.ToolResultProvenance{ToolCallID: "call-1", RawArtifactHash: "artifact-1", EvidenceIDs: []string{"evidence-1"}}}}},
			HypothesisGroundings: []domain.HypothesisGrounding{{HypothesisRevisionID: "hypothesis-1", Evidence: domain.GroundingEvidence{SupportingEvidenceIDs: []string{"evidence-1"}}}},
		},
	}
	runtime := &brainGraphRuntime{resolver: resolver, toolHash: "tool-schema", policyHash: "policy"}
	messages, err := runtime.contextBuilder(context.Background(), state, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) < 2 {
		t.Fatalf("Brain context is incomplete: %+v", messages)
	}
	payload := messages[len(messages)-1].Content
	if strings.Contains(payload, "must-not-enter-model-context") || strings.Contains(payload, "runtime-only") || strings.Contains(payload, `"facts"`) || strings.Contains(payload, `"content"`) || strings.Contains(payload, `"data"`) {
		t.Fatalf("canonical or raw evidence leaked into Brain context: %s", payload)
	}
	if !strings.Contains(payload, `"evidence_view"`) || !strings.Contains(payload, `"tool_call_ids":["call-1"]`) || !strings.Contains(payload, `"hypothesis_revision_ids":["hypothesis-1"]`) || !strings.Contains(payload, "workload_unavailable") {
		t.Fatalf("Evidence View omitted required grounding/provenance links: %s", payload)
	}
}

func TestClassifiedEvidenceToolResultPersistsOnlyEvidenceView(t *testing.T) {
	now := time.Now().UTC()
	output := brainCapabilityOutput{
		Class: domain.ToolResultEvidence, Status: "SUCCEEDED", NewInformation: true,
		Evidence:   []domain.Evidence{{ID: "evidence-1", Source: "metric", Type: "metric_evidence", ObservedAt: now, Summary: "current CPU pressure", Facts: map[string]any{"raw_runtime_fact": "private"}, Signals: []domain.EvidenceSignal{{ID: "signal-1", EvidenceID: "evidence-1", Signal: "cpu_pressure", Direction: "abnormal"}}}},
		Provenance: domain.ToolResultProvenance{ToolCallID: "call-1", ToolName: "query_metrics", EvidenceIDs: []string{"evidence-1"}, RawArtifactHash: "artifact-1", ObservedAt: now},
	}
	projection := modelFacingBrainCapabilityOutput(&WorkflowState{}, output)
	if len(projection.Evidence) != 0 || len(projection.EvidenceView) != 1 {
		t.Fatalf("model-facing Tool result retained canonical Evidence: %+v", projection)
	}
	raw, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(raw)
	if strings.Contains(encoded, "private") || strings.Contains(encoded, `"facts"`) || !strings.Contains(encoded, "cpu_pressure") || !strings.Contains(encoded, `"evidence_view"`) {
		t.Fatalf("classified Tool result projection is unsafe or incomplete: %s", encoded)
	}
}

func TestDiagnosisCannotRewriteCommittedHypothesis(t *testing.T) {
	now := time.Now().UTC()
	target := domain.ResourceRef{Namespace: "team-a", Service: "api", Resource: "api", Kind: "Service"}
	hypothesis := domain.AgentHypothesis{
		ID:              "h1",
		Statement:       "api latency is caused by application contention",
		Category:        "application",
		Mechanism:       "contention",
		TargetRefs:      []domain.ResourceRef{target},
		ModelConfidence: .72,
	}
	evidence := domain.Evidence{ID: "e1", Source: "kubernetes", ObservedAt: now}
	grounding := domain.HypothesisGrounding{ID: "g1", HypothesisRevisionID: hypothesis.ID, Level: domain.GroundingPartial, EvidenceSnapshotHash: "snapshot"}
	state := &WorkflowState{
		Incident: &domain.Incident{ID: "diagnosis-boundary", DiagnosisMethod: domain.DiagnosisMethodKubePilot, Evidence: []domain.Evidence{evidence}},
		BrainState: BrainState{
			BrainPhase:            domain.BrainPhaseDiagnosis,
			AgentHypotheses:       []domain.AgentHypothesis{hypothesis},
			HypothesisGroundings:  []domain.HypothesisGrounding{grounding},
			EvidenceSnapshotHash:  "snapshot",
			BrainToolPolicy:       brainruntime.DefaultToolCallingPolicy(),
			BrainBudget:           domain.BrainBudgetState{Limits: brainruntime.DefaultBudget()},
			ActiveSkillCategories: []domain.BrainToolCategory{domain.BrainToolReasoning},
			ActiveToolCategory:    domain.BrainToolReasoning,
		},
	}
	input := submitBrainDiagnosisInput{
		Intent:              "persist the selected grounded diagnosis",
		ExpectedObservation: []string{"immutable diagnosis projection"},
		HypothesisID:        hypothesis.ID,
		Statement:           "api latency is caused by a database failure",
		Category:            hypothesis.Category,
		Mechanism:           hypothesis.Mechanism,
		Targets:             hypothesis.TargetRefs,
		ModelConfidence:     hypothesis.ModelConfidence,
		EvidenceIDs:         []string{evidence.ID},
		ValidationResultIDs: []string{grounding.ID},
	}
	output, err := runBrainSubmitDiagnosis(withBrainWorkflowState(context.Background(), state), input)
	if err != nil {
		t.Fatal(err)
	}
	if output.Class != domain.ToolResultConstraint || output.ConstraintCode != "diagnosis_requires_hypothesis_revision" || output.Diagnosis != nil {
		t.Fatalf("diagnosis rewrote immutable hypothesis semantics: %+v", output)
	}
}

func TestBrainHistoryKeepsCompleteVisibleToolTransactions(t *testing.T) {
	assistant := &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "call-1", Type: "function", Function: schema.FunctionCall{Name: "inspect_kubernetes", Arguments: `{"intent":"inspect readiness","expected_observation":["current pod state"]}`}}}}
	tool := &schema.Message{Role: schema.Tool, ToolCallID: "call-1", ToolName: "inspect_kubernetes", Content: `{"class":"EVIDENCE","status":"OK","summary":"pod is not ready","provenance":{"evidence_ids":["e1"]}}`}
	history := boundedBrainMessageHistory([]*schema.Message{assistant, tool}, 8, 64<<10)
	if len(history) != 2 || strings.TrimSpace(history[0].Content) == "" || !strings.Contains(history[0].Content, "inspect readiness") || !strings.Contains(history[1].Content, "pod is not ready") {
		t.Fatalf("tool transaction was blank or incomplete: %+v", history)
	}
	if orphaned := boundedBrainMessageHistory([]*schema.Message{assistant, tool}, 1, 64<<10); len(orphaned) != 0 {
		t.Fatalf("history budget retained an orphaned tool request/result: %+v", orphaned)
	}
}

func TestBrainHistoryDropsReasoningOnlyAssistantTurn(t *testing.T) {
	message := &schema.Message{Role: schema.Assistant, ReasoningContent: "hidden provider reasoning"}
	history := boundedBrainMessageHistory([]*schema.Message{message}, 8, 64<<10)
	if len(history) != 0 {
		t.Fatalf("reasoning-only Assistant turn entered conversation history: %+v", history)
	}
	normalized := normalizeBrainModelMessages([]*schema.Message{message})
	if len(normalized) != 0 {
		t.Fatalf("provider input still contained a reasoning-only Assistant message: %+v", normalized)
	}
	sanitized, record, persisted := normalizeBrainAssistantOutput(message, "turn-12", time.Now().UTC())
	if persisted || record.Persisted || !record.ReasoningPresent || record.ContentPresent || record.ToolCallPresent || record.TurnID != "turn-12" {
		t.Fatalf("reasoning-only Assistant audit was incorrect: %+v", record)
	}
	if sanitized.ReasoningContent != "" || strings.TrimSpace(sanitized.Content) != "" || len(sanitized.ToolCalls) != 0 {
		t.Fatalf("hidden reasoning leaked through the provider-normalized output: %+v", sanitized)
	}
}

func TestBrainAssistantToolCallIsPersistedWithVisibleSummary(t *testing.T) {
	message := &schema.Message{Role: schema.Assistant, ReasoningContent: "hidden", ToolCalls: []schema.ToolCall{{ID: "call-1", Type: "function", Function: schema.FunctionCall{Name: "inspect_kubernetes", Arguments: `{"intent":"inspect readiness","expected_observation":["current pod state"]}`}}}}
	sanitized, record, persisted := normalizeBrainAssistantOutput(message, "turn-13", time.Now().UTC())
	if !persisted || !record.Persisted || !record.ToolCallPresent || record.ContentPresent || !record.ReasoningPresent {
		t.Fatalf("ToolCall Assistant audit was incorrect: %+v", record)
	}
	if sanitized.ReasoningContent != "" || !strings.Contains(sanitized.Content, `"type":"assistant_tool_calls"`) || !strings.Contains(sanitized.Content, "inspect readiness") {
		t.Fatalf("ToolCall Assistant did not receive a visible provider-neutral summary: %+v", sanitized)
	}
}

func TestBrainProviderHistoryRepairsMalformedToolArgumentsWithoutChangingAuditState(t *testing.T) {
	assistant := &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{
		ID:   "call-truncated",
		Type: "function",
		Function: schema.FunctionCall{
			Name:      "select_tool_category",
			Arguments: `{"intent":"select reasoning"`,
		},
	}}}
	toolResult := schema.ToolMessage(
		`{"class":"ERROR","status":"REJECTED","summary":"tool arguments were rejected before execution","constraint_code":"invalid_tool_arguments"}`,
		"call-truncated",
		schema.WithToolName("select_tool_category"),
	)

	normalized := normalizeBrainModelMessages([]*schema.Message{assistant, toolResult})
	if len(normalized) != 2 {
		t.Fatalf("provider history lost the closed Tool transaction: %+v", normalized)
	}
	if got := normalized[0].ToolCalls[0].Function.Arguments; got != `{}` {
		t.Fatalf("provider history retained malformed Tool arguments: %q", got)
	}
	if got := assistant.ToolCalls[0].Function.Arguments; got != `{"intent":"select reasoning"` {
		t.Fatalf("provider normalization modified the authoritative audit state: %q", got)
	}
	if normalized[1].Role != schema.Tool || normalized[1].ToolCallID != "call-truncated" || strings.TrimSpace(normalized[1].Content) == "" {
		t.Fatalf("provider history lost the explicit Tool execution status: %+v", normalized[1])
	}
}

func TestBrainContextAdvertisesResumePhaseOptionalSkills(t *testing.T) {
	resolver, err := LoadDefaultBrainSkillResolver()
	if err != nil {
		t.Fatal(err)
	}
	runtime := &brainGraphRuntime{resolver: resolver, toolHash: "tool-hash", policyHash: "policy-hash", deps: brainRuntimeDeps{SkillRetrieval: staticBrainSkillRetriever{}}}
	state := &WorkflowState{
		Incident: &domain.Incident{ID: "skill-catalog-context", DiagnosisMethod: domain.DiagnosisMethodKubePilot, Namespace: "team-a", Service: "api", Resource: "api", Investigation: &domain.Investigation{}},
		BrainState: BrainState{
			BrainPhase:         domain.BrainPhaseReflection,
			ResumeBrainPhase:   domain.BrainPhaseInvestigation,
			ActiveToolCategory: domain.BrainToolReasoning,
			BrainBudget:        domain.BrainBudgetState{Limits: brainruntime.DefaultBudget()},
		},
	}
	messages, err := runtime.contextBuilder(context.Background(), state, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) < 2 || !strings.Contains(messages[0].Content, "available_optional_skills") || !strings.Contains(messages[0].Content, "must never be emitted as Assistant text") {
		t.Fatalf("Brain system contract did not explain native Skill selection: %+v", messages)
	}
	var payload struct {
		Phase          domain.BrainPhase          `json:"phase"`
		SelectionPhase domain.BrainPhase          `json:"optional_skill_selection_phase"`
		Skills         []domain.SkillSearchResult `json:"available_optional_skills"`
	}
	if err = json.Unmarshal([]byte(messages[len(messages)-1].Content), &payload); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range payload.Skills {
		found = found || entry.ID == "investigate-metrics"
	}
	if payload.Phase != domain.BrainPhaseReflection || payload.SelectionPhase != domain.BrainPhaseInvestigation || !found {
		t.Fatalf("reflection did not expose exact resume-phase Skills: %+v", payload)
	}
	if len(state.RequestedSkills) != 0 {
		t.Fatalf("catalog discovery activated a Skill without a Brain request: %+v", state.RequestedSkills)
	}
}

func TestStructuredOutputGuardPersistsCompleteConstraintProvenance(t *testing.T) {
	state := &WorkflowState{
		Incident: &domain.Incident{ID: "guard-audit", Namespace: "team-a", Service: "api", Resource: "api", Investigation: &domain.Investigation{}},
		BrainState: BrainState{
			BrainPhase:           domain.BrainPhaseInvestigation,
			BrainTurns:           []domain.BrainTurn{{ID: "turn:guard", Sequence: 1}},
			BrainBudget:          domain.BrainBudgetState{Limits: brainruntime.DefaultBudget()},
			ExecutionSnapshot:    domain.ExecutionSnapshot{ToolSchemaHash: "tool-hash"},
			EvidenceSnapshotHash: "evidence-hash",
			ActiveToolCategory:   domain.BrainToolEvidence,
		},
	}
	message := &schema.Message{Role: schema.Assistant, Content: `{"tool":"collect-evidence"}`, ReasoningContent: "hidden"}
	if _, err := (&brainGraphRuntime{}).handleUnstructured(withBrainWorkflowState(context.Background(), state), message); err != nil {
		t.Fatal(err)
	}
	if len(state.ToolExecutions) != 1 || len(state.Observations) != 1 {
		t.Fatalf("structured guard did not persist one classified status: %+v", state.ToolExecutions)
	}
	execution := state.ToolExecutions[0]
	provenance := execution.Result.Provenance
	if execution.Envelope.ToolName != "structured_output_guard" || execution.Envelope.ActionID == "" || provenance.ToolCallID != execution.Envelope.ActionID || provenance.ToolName != execution.Envelope.ToolName || provenance.ToolSchemaHash != "tool-hash" || provenance.ObservedAt.IsZero() || len(provenance.TargetRefs) != 1 || provenance.ParserVersion != "structured-output-guard-v2" {
		t.Fatalf("structured guard provenance is incomplete: %+v", execution)
	}
	if execution.Result.Class != domain.ToolResultConstraint || execution.Result.Status != "REJECTED" || execution.Result.ConstraintCode != "structured_action_required" || execution.Result.Summary == "" || state.PendingReflection != nil {
		t.Fatalf("structured guard did not expose an explicit corrective status: %+v", execution.Result)
	}
	if len(state.BrainMessages) != 1 || state.BrainMessages[0].Role != schema.User || !strings.Contains(state.BrainMessages[0].Content, `"type":"runtime_structured_correction"`) || !strings.Contains(state.BrainMessages[0].Content, `"active_tool_category":"EVIDENCE"`) {
		t.Fatalf("structured guard did not inject a visible same-category correction: %+v", state.BrainMessages)
	}
	next, err := (&brainGraphRuntime{}).reflectionRoute(context.Background(), state)
	if err != nil || next != "brain_termination_router" || state.BrainPhase != domain.BrainPhaseInvestigation || state.ActiveToolCategory != domain.BrainToolEvidence {
		t.Fatalf("structured correction changed phase or category: next=%s err=%v state=%+v", next, err, state)
	}
	if message.ReasoningContent != "" {
		t.Fatal("structured guard retained hidden reasoning")
	}
}

func TestStructuredOutputGuardStopsAfterCorrectionBudget(t *testing.T) {
	budget := brainruntime.DefaultBudget()
	state := &WorkflowState{
		Incident: &domain.Incident{ID: "guard-budget", Namespace: "team-a", Service: "api", Resource: "api", Investigation: &domain.Investigation{}},
		BrainState: BrainState{
			BrainPhase:           domain.BrainPhaseInvestigation,
			BrainTurns:           []domain.BrainTurn{{ID: "turn:guard-budget", Sequence: 1}},
			BrainBudget:          domain.BrainBudgetState{Limits: budget},
			ExecutionSnapshot:    domain.ExecutionSnapshot{ToolSchemaHash: "tool-hash"},
			EvidenceSnapshotHash: "evidence-hash",
			ActiveToolCategory:   domain.BrainToolEvidence,
		},
	}
	runtime := &brainGraphRuntime{}
	for attempt := 0; attempt < budget.MaxStructuredCorrections; attempt++ {
		message := &schema.Message{Role: schema.Assistant, Content: "prose-only", ReasoningContent: "hidden"}
		if _, err := runtime.handleUnstructured(withBrainWorkflowState(context.Background(), state), message); err != nil {
			t.Fatal(err)
		}
		if message.ReasoningContent != "" {
			t.Fatal("structured guard retained hidden reasoning")
		}
	}
	if state.Termination != nil {
		t.Fatalf("structured guard terminated before granting the configured corrective retries: %+v", state.Termination)
	}
	if state.BrainBudget.Usage.StructuredCorrections != budget.MaxStructuredCorrections || len(state.ToolExecutions) != budget.MaxStructuredCorrections || len(state.BrainMessages) != budget.MaxStructuredCorrections {
		t.Fatalf("structured correction retries were not fully granted: usage=%+v executions=%d messages=%d", state.BrainBudget.Usage, len(state.ToolExecutions), len(state.BrainMessages))
	}
	finalMessage := &schema.Message{Role: schema.Assistant, Content: "still-prose-only", ReasoningContent: "hidden"}
	if _, err := runtime.handleUnstructured(withBrainWorkflowState(context.Background(), state), finalMessage); err != nil {
		t.Fatal(err)
	}
	if finalMessage.ReasoningContent != "" {
		t.Fatal("structured guard retained hidden reasoning on terminal rejection")
	}
	if state.Termination == nil || state.Termination.Reason != domain.TerminationBudgetExhausted {
		t.Fatalf("structured correction budget did not terminate explicitly: %+v", state.Termination)
	}
	if state.BrainBudget.Usage.StructuredCorrections != budget.MaxStructuredCorrections || len(state.ToolExecutions) != budget.MaxStructuredCorrections+1 || len(state.BrainMessages) != budget.MaxStructuredCorrections {
		t.Fatalf("structured correction accounting is inconsistent: usage=%+v executions=%d messages=%d", state.BrainBudget.Usage, len(state.ToolExecutions), len(state.BrainMessages))
	}
	for _, execution := range state.ToolExecutions {
		if execution.Result.Class != domain.ToolResultConstraint || execution.Result.Status == "" || execution.Result.Provenance.ToolCallID == "" {
			t.Fatalf("structured correction emitted an empty tool status: %+v", execution)
		}
	}
}

func TestToolArgumentGuardReturnsOneNonEmptyStatusPerCall(t *testing.T) {
	registry, err := buildBrainCapabilities(brainRuntimeDeps{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	invalidCall := schema.ToolCall{ID: "call-invalid", Type: "function", Function: schema.FunctionCall{Name: "submit_investigation_plan", Arguments: `{"intent":"plan" "expected_observation":["persisted"],"objective":"diagnose","goals":["inspect"],"stop_conditions":["grounded"]}`}}
	validCall := schema.ToolCall{ID: "call-valid-sibling", Type: "function", Function: schema.FunctionCall{Name: "submit_investigation_plan", Arguments: `{"intent":"plan","expected_observation":["persisted"],"objective":"diagnose","goals":["inspect"],"stop_conditions":["grounded"]}`}}
	assistant := schema.AssistantMessage("", []schema.ToolCall{invalidCall, validCall})
	ensureBrainAssistantToolCallContent(assistant)
	state := &WorkflowState{
		Incident: &domain.Incident{ID: "argument-guard", Namespace: "team-a", Service: "api", Resource: "api", Investigation: &domain.Investigation{}},
		BrainState: BrainState{
			BrainMessages:        []*schema.Message{assistant},
			BrainPhase:           domain.BrainPhasePlanning,
			BrainTurns:           []domain.BrainTurn{{ID: "turn:argument-guard", Sequence: 1}},
			BrainBudget:          domain.BrainBudgetState{Limits: brainruntime.DefaultBudget()},
			ExecutionSnapshot:    domain.ExecutionSnapshot{ToolSchemaHash: "tool-hash"},
			EvidenceSnapshotHash: "evidence-hash",
			ActiveToolCategory:   domain.BrainToolReasoning,
		},
	}
	runtime := &brainGraphRuntime{tools: registry}
	if _, err = runtime.handleInvalidToolArguments(withBrainWorkflowState(context.Background(), state), assistant); err != nil {
		t.Fatal(err)
	}
	if len(state.ToolExecutions) != 2 || state.BrainBudget.Usage.ToolCalls != 2 || state.BrainBudget.Usage.StructuredCorrections != 1 {
		t.Fatalf("invalid atomic batch accounting is incomplete: executions=%d usage=%+v", len(state.ToolExecutions), state.BrainBudget.Usage)
	}
	if state.PendingReflection != nil || state.Termination != nil {
		t.Fatalf("provider argument correction changed cognition or terminated early: reflection=%v termination=%+v", state.PendingReflection, state.Termination)
	}
	for index, execution := range state.ToolExecutions {
		if execution.Result.Class != domain.ToolResultError || execution.Result.Status != "REJECTED" || execution.Result.Summary == "" || execution.Result.Infrastructure || execution.Result.Provenance.ToolCallID == "" || execution.Result.Provenance.ToolName == "" || execution.Result.Provenance.RawArtifactHash == "" || execution.Result.Provenance.ParserVersion != "brain-tool-argument-guard-v1" {
			t.Fatalf("invalid ToolCall %d did not receive a complete status: %+v", index, execution)
		}
	}
	if state.ToolExecutions[0].Result.ConstraintCode != "invalid_tool_arguments" || state.ToolExecutions[1].Result.ConstraintCode != "atomic_tool_batch_invalid" {
		t.Fatalf("atomic rejection did not distinguish invalid and unexecuted siblings: %+v", state.ToolExecutions)
	}
	if len(state.BrainMessages) != 4 || state.BrainMessages[1].Role != schema.Tool || state.BrainMessages[1].ToolCallID != invalidCall.ID || strings.TrimSpace(state.BrainMessages[1].Content) == "" || state.BrainMessages[2].Role != schema.Tool || state.BrainMessages[2].ToolCallID != validCall.ID || strings.TrimSpace(state.BrainMessages[2].Content) == "" || state.BrainMessages[3].Role != schema.User || !strings.Contains(state.BrainMessages[3].Content, `"type":"runtime_tool_argument_correction"`) {
		t.Fatalf("invalid Assistant batch was not closed with paired non-empty Tool messages: %+v", state.BrainMessages)
	}
	units := completeBrainMessageUnits(state.BrainMessages)
	if len(units) != 2 || len(units[0]) != 3 {
		t.Fatalf("checkpoint history contains orphan ToolCalls: %+v", units)
	}
	providerHistory := normalizeBrainModelMessages(append([]*schema.Message(nil), units[0]...))
	if len(providerHistory) != 3 || providerHistory[0].ToolCalls[0].Function.Arguments != `{}` {
		t.Fatalf("invalid ToolCall could not be safely replayed to the provider: %+v", providerHistory)
	}
	if state.BrainMessages[0].ToolCalls[0].Function.Arguments != invalidCall.Function.Arguments {
		t.Fatalf("provider replay normalization modified the audited ToolCall: %+v", state.BrainMessages[0])
	}
}

func TestToolHistoryUsesServerClassifiedResultWithProvenance(t *testing.T) {
	assistant := &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "call-1", Type: "function", Function: schema.FunctionCall{Name: "inspect_kubernetes", Arguments: `{"intent":"inspect readiness","expected_observation":["current pod state"],"targets":[{"namespace":"team-a","service":"api"}]}`}}}}
	now := time.Now().UTC()
	output := brainCapabilityOutput{Class: domain.ToolResultConstraint, Status: "REJECTED", Summary: "scope denied", ConstraintCode: "cross_namespace_denied", Provenance: domain.ToolResultProvenance{Collector: "constraint-kernel", WindowStart: now, WindowEnd: now, ObservedAt: now, RawArtifactHash: "artifact", ParserVersion: "v1"}}
	raw, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	state := &WorkflowState{
		Incident: &domain.Incident{ID: "tool-result", Investigation: &domain.Investigation{}},
		BrainState: BrainState{
			BrainMessages:     []*schema.Message{assistant},
			ExecutionSnapshot: domain.ExecutionSnapshot{ToolSchemaHash: "schema-hash"},
			BrainBudget:       domain.BrainBudgetState{Limits: brainruntime.DefaultBudget()},
		},
	}
	message := &schema.Message{Role: schema.Tool, ToolCallID: "call-1", ToolName: "inspect_kubernetes", Content: string(raw)}
	if _, err = (&brainGraphRuntime{}).classifyToolResults(withBrainWorkflowState(context.Background(), state), []*schema.Message{message}); err != nil {
		t.Fatal(err)
	}
	if len(state.BrainMessages) != 2 {
		t.Fatalf("classified Tool result was not retained: %+v", state.BrainMessages)
	}
	classified, err := decodeBrainCapabilityOutput(state.BrainMessages[1].Content)
	if err != nil {
		t.Fatal(err)
	}
	if classified.Class != domain.ToolResultConstraint || classified.ConstraintCode != "cross_namespace_denied" || classified.Provenance.ToolCallID != "call-1" || classified.Provenance.ToolSchemaHash != "schema-hash" {
		t.Fatalf("Brain history did not contain the classified result and injected provenance: %+v", classified)
	}
}

func TestReflectionCanCorrectPreHypothesisConstraintBySubmittingHypotheses(t *testing.T) {
	target := domain.ResourceRef{Namespace: "team-a", Service: "api", Resource: "api", Kind: "Service"}
	state := &WorkflowState{
		Incident: &domain.Incident{ID: "reflection-admission", Namespace: "team-a", Service: "api", Resource: "api", Investigation: &domain.Investigation{}},
		BrainState: BrainState{
			BrainPhase:            domain.BrainPhaseReflection,
			ResumeBrainPhase:      domain.BrainPhaseInvestigation,
			BrainTurns:            []domain.BrainTurn{{ID: "turn:reflection-skill", Sequence: 1, Phase: domain.BrainPhaseReflection}},
			BrainToolPolicy:       brainruntime.DefaultToolCallingPolicy(),
			BrainBudget:           domain.BrainBudgetState{Limits: brainruntime.DefaultBudget()},
			ActiveToolCategory:    domain.BrainToolReasoning,
			ActiveSkillCategories: []domain.BrainToolCategory{domain.BrainToolReasoning},
		},
	}
	input := submitBrainHypothesesInput{
		Intent:              "correct the blocked investigation by forming a falsifiable hypothesis",
		ExpectedObservation: []string{"server admission decision"},
		Hypotheses: []proposedBrainHypothesis{{
			Statement:               "api degradation is caused by a local execution stall",
			Category:                "application",
			Mechanism:               "execution stall",
			Targets:                 []domain.ResourceRef{target},
			EvidenceNeeds:           []string{"workload execution evidence"},
			FalsificationConditions: []string{"workload execution is healthy"},
			ModelConfidence:         .5,
		}},
	}
	output, err := runBrainSubmitHypotheses(withBrainWorkflowState(context.Background(), state), brainRuntimeDeps{}, input)
	if err != nil {
		t.Fatal(err)
	}
	if output.Class == domain.ToolResultConstraint || len(output.Hypotheses) != 1 || len(output.Admissions) != 1 {
		t.Fatalf("Reflection could not produce the structured correction needed to leave the pre-hypothesis failure: %+v", output)
	}
}

func TestReflectionSkillRequestResolvesAgainstResumePhase(t *testing.T) {
	resolver, err := LoadDefaultBrainSkillResolver()
	if err != nil {
		t.Fatal(err)
	}
	state := &WorkflowState{
		Incident: &domain.Incident{ID: "reflection-skill", Namespace: "team-a", Service: "api", Resource: "api", Investigation: &domain.Investigation{}},
		BrainState: BrainState{
			BrainPhase:            domain.BrainPhaseReflection,
			ResumeBrainPhase:      domain.BrainPhaseInvestigation,
			BrainTurns:            []domain.BrainTurn{{ID: "turn:reflection-skill", Sequence: 1, Phase: domain.BrainPhaseReflection}},
			BrainToolPolicy:       brainruntime.DefaultToolCallingPolicy(),
			BrainBudget:           domain.BrainBudgetState{Limits: brainruntime.DefaultBudget()},
			ActiveToolCategory:    domain.BrainToolReasoning,
			ActiveSkillCategories: []domain.BrainToolCategory{domain.BrainToolReasoning, domain.BrainToolControl},
			Reflections:           []domain.ReflectionRecord{{ID: "reflection:constraint", Trigger: domain.ReflectionConstraintFailure}},
		},
	}
	seedRetrievedBrainSkills(state, "investigate-metrics")
	output, err := runBrainRequestSkills(withBrainWorkflowState(context.Background(), state), resolver, requestBrainSkillsInput{
		Intent:              "load the bounded metric procedure after a denied evidence category",
		ExpectedObservation: []string{"phase-compatible Skill activation"},
		SkillIDs:            []string{"investigate-metrics"},
		Reason:              "metrics can distinguish the admitted hypotheses",
		Trigger:             "CONSTRAINT_FAILURE",
	})
	if err != nil {
		t.Fatal(err)
	}
	if output.Class != domain.ToolResultValidation || len(output.RequestedSkills) != 1 || output.RequestedSkills[0].SkillID != "investigate-metrics" || output.SelectedCategory != domain.BrainToolEvidence {
		t.Fatalf("reflection Skill request was not admitted for the resume phase: %+v", output)
	}
	(&brainGraphRuntime{}).applyCapabilityOutput(state, output)
	if state.BrainPhase != domain.BrainPhaseInvestigation || state.ResumeBrainPhase != "" || len(state.RequestedSkills) != 1 || state.ActiveToolCategory != domain.BrainToolEvidence {
		t.Fatalf("reflection did not resume investigation with the admitted Skill: phase=%s resume=%s category=%s requests=%+v", state.BrainPhase, state.ResumeBrainPhase, state.ActiveToolCategory, state.RequestedSkills)
	}
	if !state.Reflections[0].Accepted {
		t.Fatalf("corrective Skill request was not recorded as an accepted reflection: %+v", state.Reflections[0])
	}
	resolved, err := resolver.Resolve(state.BrainPhase, state.RequestedSkills, state.BrainBudget.Limits.MaxOptionalSkillsPerTurn, currentBrainTurnID(state))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ref := range resolved.Refs {
		found = found || ref.ID == "investigate-metrics"
	}
	if !found || !resolved.AllowedCategories[domain.BrainToolEvidence] {
		t.Fatalf("resumed Skill bundle did not grant its bounded Evidence category: refs=%+v categories=%+v", resolved.Refs, resolved.AllowedCategories)
	}
}

func TestToolCategorySelectionAtomicallyActivatesBrainChosenSkill(t *testing.T) {
	resolver, err := LoadDefaultBrainSkillResolver()
	if err != nil {
		t.Fatal(err)
	}
	state := &WorkflowState{
		Incident: &domain.Incident{ID: "atomic-skill-category", Namespace: "team-a", Service: "api", Resource: "api", Investigation: &domain.Investigation{}},
		BrainState: BrainState{
			BrainPhase:            domain.BrainPhaseInvestigation,
			BrainTurns:            []domain.BrainTurn{{ID: "turn:atomic", Sequence: 1}},
			BrainToolPolicy:       brainruntime.DefaultToolCallingPolicy(),
			BrainBudget:           domain.BrainBudgetState{Limits: brainruntime.DefaultBudget()},
			ActiveToolCategory:    domain.BrainToolReasoning,
			ActiveSkillCategories: []domain.BrainToolCategory{domain.BrainToolReasoning},
		},
	}
	seedRetrievedBrainSkills(state, "investigate-metrics")
	output, err := runBrainSelectCategory(withBrainWorkflowState(context.Background(), state), resolver, selectBrainCategoryInput{
		Intent: "select bounded metric evidence", ExpectedObservation: []string{"metric Skill activation and category decision"}, Category: domain.BrainToolEvidence,
		SkillIDs: []string{"investigate-metrics"}, Reason: "metrics distinguish admitted hypotheses", Trigger: "HYPOTHESIS_CONFLICT",
	})
	if err != nil {
		t.Fatal(err)
	}
	if output.Class != domain.ToolResultValidation || output.Status != "OK" || output.SelectedCategory != domain.BrainToolEvidence || len(output.RequestedSkills) != 1 || output.RequestedSkills[0].SkillID != "investigate-metrics" {
		t.Fatalf("atomic Skill/category decision was not admitted: %+v", output)
	}
	(&brainGraphRuntime{}).applyCapabilityOutput(state, output)
	if state.ActiveToolCategory != domain.BrainToolEvidence || len(state.RequestedSkills) != 1 || state.PendingReflection != nil {
		t.Fatalf("atomic Skill/category decision did not update the next Brain boundary: %+v", state.BrainState)
	}
	resolved, err := resolver.Resolve(state.BrainPhase, state.RequestedSkills, state.BrainBudget.Limits.MaxOptionalSkillsPerTurn, currentBrainTurnID(state))
	if err != nil {
		t.Fatal(err)
	}
	if !resolved.AllowedCategories[domain.BrainToolEvidence] {
		t.Fatalf("Brain-chosen Skill did not grant the selected category: %+v", resolved.AllowedCategories)
	}
}

func TestToolCategorySelectionRejectsSkillCategoryMismatch(t *testing.T) {
	resolver, err := LoadDefaultBrainSkillResolver()
	if err != nil {
		t.Fatal(err)
	}
	state := &WorkflowState{
		Incident: &domain.Incident{ID: "skill-category-mismatch", Namespace: "team-a", Service: "api", Resource: "api", Investigation: &domain.Investigation{}},
		BrainState: BrainState{
			BrainPhase: domain.BrainPhaseInvestigation, BrainTurns: []domain.BrainTurn{{ID: "turn:mismatch", Sequence: 1}},
			BrainToolPolicy: brainruntime.DefaultToolCallingPolicy(), BrainBudget: domain.BrainBudgetState{Limits: brainruntime.DefaultBudget()},
			ActiveToolCategory: domain.BrainToolReasoning, ActiveSkillCategories: []domain.BrainToolCategory{domain.BrainToolReasoning},
		},
	}
	seedRetrievedBrainSkills(state, "investigate-metrics")
	output, err := runBrainSelectCategory(withBrainWorkflowState(context.Background(), state), resolver, selectBrainCategoryInput{
		Intent: "select retrieval with a metric-only Skill", ExpectedObservation: []string{"explicit category mismatch"}, Category: domain.BrainToolRetrieval,
		SkillIDs: []string{"investigate-metrics"}, Reason: "test mismatch", Trigger: "HYPOTHESIS_CONFLICT",
	})
	if err != nil {
		t.Fatal(err)
	}
	if output.Class != domain.ToolResultConstraint || output.Status != "REJECTED" || output.ConstraintCode != "tool_category_not_granted_by_requested_skill" || len(output.RequestedSkills) != 0 {
		t.Fatalf("Skill/category mismatch expanded authority: %+v", output)
	}
	if len(output.SkillActivations) != 1 {
		t.Fatalf("Skill/category mismatch did not retain one rejected activation: %+v", output.SkillActivations)
	}
	activation := output.SkillActivations[0]
	pkg := resolver.packages["investigate-metrics"]
	if activation.SkillID != pkg.Spec.ID || activation.Version != pkg.Spec.Version || activation.ContentHash != pkg.Hash || activation.Status != "REJECTED" || activation.RejectedReason != "requested_skill_does_not_grant_category" {
		t.Fatalf("rejected Skill/category decision lost frozen catalog identity: %+v", activation)
	}
}

func TestRejectedActivationForKnownSkillPreservesCatalogIdentity(t *testing.T) {
	resolver, err := LoadDefaultBrainSkillResolver()
	if err != nil {
		t.Fatal(err)
	}
	request := SkillRequest{
		SkillID:       "explore-resources",
		Reason:        "resolve an admitted target",
		Trigger:       "HYPOTHESIS_CONFLICT",
		RequestedBy:   "BRAIN",
		RequestedTurn: "turn:rejected-activation",
	}
	now := time.Now().UTC()
	activation := rejectedActivationFor(resolver, request, domain.BrainPhaseInvestigation, "activation_decision_missing", now)
	pkg := resolver.packages[request.SkillID]
	if activation.SkillID != pkg.Spec.ID || activation.Version != pkg.Spec.Version || activation.ContentHash != pkg.Hash {
		t.Fatalf("known rejected Skill lost catalog identity: %+v", activation)
	}
	if activation.Status != "REJECTED" || activation.RejectedReason != "activation_decision_missing" || activation.Phase != domain.BrainPhaseInvestigation || activation.ActivatedAt != now {
		t.Fatalf("known rejected Skill lost its decision audit: %+v", activation)
	}
}

func TestExploreResourcesSkillRoutesOnlyToEvidenceToolsNode(t *testing.T) {
	resolver, err := LoadDefaultBrainSkillResolver()
	if err != nil {
		t.Fatal(err)
	}
	pkg, ok := resolver.packages["explore-resources"]
	if !ok {
		t.Fatal("explore-resources Skill is missing")
	}
	if pkg.Spec.Version != "2" || len(pkg.Spec.AllowedToolCategories) != 1 || pkg.Spec.AllowedToolCategories[0] != domain.BrainToolEvidence {
		t.Fatalf("resource exploration grants a Tool Category without a current-resource tool: %+v", pkg.Spec)
	}
	registry, err := buildBrainCapabilities(brainRuntimeDeps{}, resolver)
	if err != nil {
		t.Fatal(err)
	}
	assertNodeContains := func(node, name string, wanted bool) {
		t.Helper()
		infos, loadErr := registry.ToolInfosForNode(context.Background(), node)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		found := false
		for _, info := range infos {
			found = found || info.Name == name
		}
		if found != wanted {
			t.Fatalf("node %s tool %s present=%t want=%t", node, name, found, wanted)
		}
	}
	assertNodeContains(captools.NodeBrainEvidence, "discover_resources", true)
	assertNodeContains(captools.NodeBrainRetrieval, "discover_resources", false)

	newState := func(id string) *WorkflowState {
		state := &WorkflowState{
			Incident: &domain.Incident{ID: id, Namespace: "team-a", Service: "api", Resource: "api", Investigation: &domain.Investigation{}},
			BrainState: BrainState{
				BrainPhase: domain.BrainPhaseInvestigation, BrainTurns: []domain.BrainTurn{{ID: "turn:" + id, Sequence: 1, ToolCategory: domain.BrainToolReasoning}},
				BrainToolPolicy: brainruntime.DefaultToolCallingPolicy(), BrainBudget: domain.BrainBudgetState{Limits: brainruntime.DefaultBudget()},
				ActiveToolCategory: domain.BrainToolReasoning, ActiveSkillCategories: []domain.BrainToolCategory{domain.BrainToolReasoning},
			},
		}
		seedRetrievedBrainSkills(state, "explore-resources")
		return state
	}
	retrieval, err := runBrainSelectCategory(withBrainWorkflowState(context.Background(), newState("resource-retrieval")), resolver, selectBrainCategoryInput{
		Intent: "resolve current resources", ExpectedObservation: []string{"typed resource identities"}, Category: domain.BrainToolRetrieval,
		SkillIDs: []string{"explore-resources"}, Reason: "resolve one-hop scope", Trigger: "CANDIDATE_CONFLICT",
	})
	if err != nil {
		t.Fatal(err)
	}
	if retrieval.Class != domain.ToolResultConstraint || retrieval.ConstraintCode != "tool_category_not_granted_by_requested_skill" {
		t.Fatalf("resource exploration incorrectly granted RETRIEVAL: %+v", retrieval)
	}
	evidence, err := runBrainSelectCategory(withBrainWorkflowState(context.Background(), newState("resource-evidence")), resolver, selectBrainCategoryInput{
		Intent: "resolve current resources", ExpectedObservation: []string{"typed resource identities"}, Category: domain.BrainToolEvidence,
		SkillIDs: []string{"explore-resources"}, Reason: "resolve one-hop scope", Trigger: "CANDIDATE_CONFLICT",
	})
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Class != domain.ToolResultValidation || evidence.Status != "OK" || evidence.SelectedCategory != domain.BrainToolEvidence {
		t.Fatalf("resource exploration did not grant its Evidence ToolsNode: %+v", evidence)
	}
}

func TestToolEnvelopeAuditsLogicalAndActuallyRoutedCategories(t *testing.T) {
	state := &WorkflowState{
		Incident: &domain.Incident{ID: "routed-category", Namespace: "team-a", Service: "api", Resource: "api"},
		BrainState: BrainState{
			BrainTurns: []domain.BrainTurn{{ID: "turn:routed", ToolCategory: domain.BrainToolRetrieval}},
			BrainMessages: []*schema.Message{{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{{
					ID: "call:routed", Type: "function",
					Function: schema.FunctionCall{Name: "discover_resources", Arguments: `{"intent":"resolve resources","expected_observation":["typed resources"]}`},
				}},
			}},
		},
	}
	envelope := envelopeFromToolCall(state, "call:routed", "discover_resources")
	if envelope.ToolCategory != domain.BrainToolEvidence || envelope.RoutedToolCategory != domain.BrainToolRetrieval {
		t.Fatalf("tool envelope lost logical or actual route category: %+v", envelope)
	}
}

func TestRejectedSkillRequestReturnsExplicitConstraintAndAudit(t *testing.T) {
	resolver, err := LoadDefaultBrainSkillResolver()
	if err != nil {
		t.Fatal(err)
	}
	state := &WorkflowState{
		Incident: &domain.Incident{ID: "rejected-skill", Namespace: "team-a", Service: "api", Resource: "api", Investigation: &domain.Investigation{}},
		BrainState: BrainState{
			BrainPhase:            domain.BrainPhaseReflection,
			ResumeBrainPhase:      domain.BrainPhaseInvestigation,
			BrainTurns:            []domain.BrainTurn{{ID: "turn:rejected-skill", Sequence: 1, Phase: domain.BrainPhaseReflection}},
			BrainToolPolicy:       brainruntime.DefaultToolCallingPolicy(),
			BrainBudget:           domain.BrainBudgetState{Limits: brainruntime.DefaultBudget()},
			ActiveToolCategory:    domain.BrainToolReasoning,
			ActiveSkillCategories: []domain.BrainToolCategory{domain.BrainToolReasoning, domain.BrainToolControl},
		},
	}
	seedRetrievedBrainSkills(state, "plan-recovery")
	output, err := runBrainRequestSkills(withBrainWorkflowState(context.Background(), state), resolver, requestBrainSkillsInput{
		Intent: "request a phase-incompatible capability", ExpectedObservation: []string{"explicit rejection decision"}, SkillIDs: []string{"plan-recovery"}, Reason: "attempt recovery during investigation", Trigger: "CONSTRAINT_FAILURE",
	})
	if err != nil {
		t.Fatal(err)
	}
	if output.Class != domain.ToolResultConstraint || output.Status != "REJECTED" || output.ConstraintCode != "skill_request_not_activated" || output.NewInformation || len(output.RequestedSkills) != 0 || len(output.SkillActivations) != 1 || !strings.Contains(output.Summary, "plan-recovery:incompatible_phase") {
		t.Fatalf("rejected Skill request did not return its actual execution state: %+v", output)
	}
	(&brainGraphRuntime{}).applyCapabilityOutput(state, output)
	if len(state.SkillActivations) != 1 || state.SkillActivations[0].RejectedReason != "incompatible_phase" || state.PendingReflection == nil || *state.PendingReflection != domain.ReflectionConstraintFailure {
		t.Fatalf("rejected Skill decision was not retained in the audit state: %+v", state)
	}
}

type brainRunbookMemory struct {
	items []domain.MemoryResult
}

func (m brainRunbookMemory) Read(_ context.Context, query domain.MemoryQuery) ([]domain.MemoryResult, error) {
	if query.Kind != domain.MemoryProcedural || query.Scope.Namespace == "" {
		return nil, fmt.Errorf("unexpected runbook query: %+v", query)
	}
	return append([]domain.MemoryResult(nil), m.items...), nil
}

func (brainRunbookMemory) WriteVerifiedIncident(context.Context, domain.IncidentLearningInput) error {
	return nil
}

func (brainRunbookMemory) RecordAccess(context.Context, domain.MemoryAccessEvent) error { return nil }

type recordingBrainMemory struct {
	events []domain.MemoryAccessEvent
}

func (*recordingBrainMemory) Read(context.Context, domain.MemoryQuery) ([]domain.MemoryResult, error) {
	return nil, nil
}
func (*recordingBrainMemory) WriteVerifiedIncident(context.Context, domain.IncidentLearningInput) error {
	return nil
}
func (m *recordingBrainMemory) RecordAccess(_ context.Context, event domain.MemoryAccessEvent) error {
	m.events = append(m.events, event)
	return nil
}

type brainResourceCollector struct{}

func (brainResourceCollector) Collect(_ context.Context, incident *domain.Incident, _ domain.EvidenceRequest) ([]domain.Evidence, error) {
	return []domain.Evidence{{
		Source: "kubernetes", Type: "workload_state", Namespace: incident.Namespace, Service: incident.Service, Resource: incident.Resource,
		Summary: "server-owned dependency topology", Facts: map[string]any{"dependency": "redis"}, ObservedAt: time.Now().UTC(),
	}}, nil
}

func TestBrainCapabilitySurfaceHasNoRemovedToolAliases(t *testing.T) {
	resolver, err := LoadDefaultBrainSkillResolver()
	if err != nil {
		t.Fatal(err)
	}
	registry, err := buildBrainCapabilities(brainRuntimeDeps{}, resolver)
	if err != nil {
		t.Fatal(err)
	}
	wanted := map[string]bool{
		"query_metrics": false, "search_logs": false, "query_traces": false, "inspect_kubernetes": false, "discover_resources": false,
		"retrieve_incidents": false, "retrieve_runbooks": false, "retrieve_patterns": false,
		"validate_hypothesis": false, "compare_hypotheses": false, "validate_diagnosis": false,
	}
	removed := map[string]bool{"query_prometheus_evidence": true, "query_loki_evidence": true, "query_trace_evidence": true, "query_kubernetes_evidence": true, "retrieve_causal_patterns": true}
	for _, node := range []string{captools.NodeBrainEvidence, captools.NodeBrainRetrieval, captools.NodeBrainReasoning, captools.NodeBrainRecovery, captools.NodeBrainControl} {
		infos, loadErr := registry.ToolInfosForNode(context.Background(), node)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		for _, info := range infos {
			if removed[info.Name] {
				t.Fatalf("removed Brain tool alias %q is still registered", info.Name)
			}
			if _, ok := wanted[info.Name]; ok {
				wanted[info.Name] = true
			}
		}
	}
	for name, found := range wanted {
		if !found {
			t.Errorf("required Brain capability %q is not registered", name)
		}
	}
}

func TestDiscoverResourcesReturnsOnlyServerResolvedScope(t *testing.T) {
	state := &WorkflowState{
		Incident: &domain.Incident{ID: "resource-discovery", Namespace: "team-a", Service: "api", Resource: "api", Investigation: &domain.Investigation{}},
		BrainState: BrainState{
			BrainPhase: domain.BrainPhaseInvestigation, ActiveToolCategory: domain.BrainToolEvidence,
			ActiveSkillCategories: []domain.BrainToolCategory{domain.BrainToolEvidence}, BrainToolPolicy: brainruntime.DefaultToolCallingPolicy(), BrainBudget: domain.BrainBudgetState{Limits: brainruntime.DefaultBudget()},
		},
	}
	output, err := runBrainDiscoverResources(withBrainWorkflowState(context.Background(), state), brainRuntimeDeps{Collectors: map[string]Collector{"kubernetes": brainResourceCollector{}}}, brainEvidenceToolInput{
		Intent: "resolve observed one-hop dependencies", ExpectedObservation: []string{"typed resource identities"}, Targets: []domain.ResourceRef{{Namespace: "team-a", Service: "api", Resource: "api", Kind: "Service"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	foundRedis := false
	for _, ref := range output.Resources {
		if ref.Namespace != "team-a" {
			t.Fatalf("resource discovery escaped Incident namespace: %+v", ref)
		}
		foundRedis = foundRedis || ref.Service == "redis"
	}
	if output.Class != domain.ToolResultEvidence || !foundRedis || len(output.Evidence) == 0 || len(output.Provenance.EvidenceIDs) == 0 {
		t.Fatalf("resource discovery omitted server evidence or one-hop identity: %+v", output)
	}
}

func TestBrainIncidentRetrievalUsesOnlyHybridBoundary(t *testing.T) {
	state := &WorkflowState{
		Incident:   &domain.Incident{ID: "hybrid-tool", Cluster: "cluster-a", Namespace: "team-a", Service: "api", Resource: "api", Investigation: &domain.Investigation{}},
		BrainState: BrainState{BrainPhase: domain.BrainPhaseInvestigation, ActiveToolCategory: domain.BrainToolRetrieval, ActiveSkillCategories: []domain.BrainToolCategory{domain.BrainToolRetrieval}, BrainToolPolicy: brainruntime.DefaultToolCallingPolicy(), BrainBudget: domain.BrainBudgetState{Limits: brainruntime.DefaultBudget()}},
	}
	retriever := &recordingBrainHybridRetriever{}
	output, err := runBrainIncidentRetrieval(withBrainWorkflowState(context.Background(), state), brainRuntimeDeps{BrainRetrieval: retriever}, brainRetrievalToolInput{Intent: "retrieve comparable incidents", ExpectedObservation: []string{"fused historical evidence"}, Terms: []string{"timeout"}})
	if err != nil {
		t.Fatal(err)
	}
	if retriever.calls != 1 || output.HybridRetrieval == nil || len(output.HistoricalIncidents) != 1 || output.Provenance.Collector != "hybrid-retrieval-v2" {
		t.Fatalf("Brain did not use its exclusive hybrid retrieval boundary: calls=%d output=%+v", retriever.calls, output)
	}
}

func TestBrainRuntimeAuditProjectionIncludesWorldAndRetrievalState(t *testing.T) {
	now := time.Now().UTC()
	state := &WorkflowState{
		Incident: &domain.Incident{ID: "brain-audit", Investigation: &domain.Investigation{}},
		BrainState: BrainState{
			WorldModel:       &domain.OperationalWorldModel{IncidentID: "brain-audit", BuiltAt: now, EvidenceSnapshotHash: "world-snapshot"},
			HybridRetrievals: []domain.HybridRetrievalResult{{SnapshotHash: "hybrid-snapshot", RetrievedAt: now}},
			SkillRetrievals:  []domain.SkillRetrievalResult{{SnapshotHash: "skill-snapshot", RetrievedAt: now}},
		},
	}
	(&brainGraphRuntime{}).syncInvestigation(state)
	inv := state.Incident.Investigation
	if inv.WorldModel == nil || inv.WorldModel.EvidenceSnapshotHash != "world-snapshot" || len(inv.HybridRetrievals) != 1 || inv.HybridRetrievals[0].SnapshotHash != "hybrid-snapshot" || len(inv.SkillRetrievals) != 1 || inv.SkillRetrievals[0].SnapshotHash != "skill-snapshot" {
		t.Fatalf("new Brain runtime state is missing from the Investigation audit projection: %+v", inv)
	}
}

func TestRunbookRetrievalIsProceduralContextNotEvidence(t *testing.T) {
	state := &WorkflowState{
		Incident: &domain.Incident{ID: "runbook", Cluster: "cluster-a", Namespace: "team-a", Service: "api", Resource: "api", Investigation: &domain.Investigation{}},
		BrainState: BrainState{
			BrainPhase: domain.BrainPhaseInvestigation, ActiveToolCategory: domain.BrainToolRetrieval,
			ActiveSkillCategories: []domain.BrainToolCategory{domain.BrainToolRetrieval}, BrainToolPolicy: brainruntime.DefaultToolCallingPolicy(), BrainBudget: domain.BrainBudgetState{Limits: brainruntime.DefaultBudget()},
		},
	}
	memory := brainRunbookMemory{items: []domain.MemoryResult{{ID: "runbook-1", Summary: "restart only after approval", Kind: domain.MemoryProcedural}}}
	output, err := runBrainRunbookRetrieval(withBrainWorkflowState(context.Background(), state), brainRuntimeDeps{Memory: memory}, brainRetrievalToolInput{Intent: "retrieve bounded recovery procedure", ExpectedObservation: []string{"procedural context"}, Terms: []string{"api"}})
	if err != nil {
		t.Fatal(err)
	}
	if output.Class != domain.ToolResultValidation || len(output.Memory) != 1 || len(output.Evidence) != 0 || output.Provenance.Collector != "procedural-memory" {
		t.Fatalf("runbook retrieval was promoted to Incident evidence: %+v", output)
	}
}

func TestBrainRetrievalAuditIsPersistedThroughMemoryBoundary(t *testing.T) {
	memory := &recordingBrainMemory{}
	state := &WorkflowState{
		Incident: &domain.Incident{ID: "memory-audit", Namespace: "team-a", Service: "api", Resource: "api", Investigation: &domain.Investigation{}},
		BrainState: BrainState{
			BrainTurns:        []domain.BrainTurn{{ID: "turn:memory", Sequence: 1, Phase: domain.BrainPhaseInvestigation}},
			ExecutionSnapshot: domain.ExecutionSnapshot{ToolSchemaHash: "tools", PolicyHash: "policy"},
			BrainMessages:     []*schema.Message{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "call:memory", Type: "function", Function: schema.FunctionCall{Name: "retrieve_runbooks", Arguments: `{"intent":"load procedure","expected_observation":["bounded runbook"]}`}}}}},
		},
	}
	output := brainCapabilityOutput{Class: domain.ToolResultValidation, Status: "OK", Summary: "procedural memory retrieved", NewInformation: true, Memory: []domain.MemoryResult{{ID: "runbook-1", Kind: domain.MemoryProcedural, Summary: "bounded recovery procedure"}}, Provenance: domain.ToolResultProvenance{Collector: "procedural-memory", RawArtifactHash: "artifact", ParserVersion: "memory-v1"}}
	raw, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &brainGraphRuntime{deps: brainRuntimeDeps{Memory: memory}}
	_, err = runtime.classifyToolResults(withBrainWorkflowState(context.Background(), state), []*schema.Message{{Role: schema.Tool, ToolCallID: "call:memory", ToolName: "retrieve_runbooks", Content: string(raw)}})
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Incident.Investigation.MemoryReads) != 1 || len(memory.events) != 1 || memory.events[0].Kind != domain.MemoryProcedural || len(memory.events[0].ResultIDs) != 1 || memory.events[0].ResultIDs[0] != "runbook-1" {
		t.Fatalf("Brain memory read was not durably audited: investigation=%+v recorder=%+v", state.Incident.Investigation.MemoryReads, memory.events)
	}
}

func TestHypothesisComparisonDoesNotSelectOrMutateBelief(t *testing.T) {
	first := domain.AgentHypothesis{ID: "h1", ModelConfidence: .8}
	second := domain.AgentHypothesis{ID: "h2", ModelConfidence: .4}
	state := &WorkflowState{
		Incident: &domain.Incident{ID: "compare", Namespace: "team-a", Service: "api", Resource: "api", Investigation: &domain.Investigation{}},
		BrainState: BrainState{
			BrainPhase: domain.BrainPhaseInvestigation, ActiveToolCategory: domain.BrainToolReasoning, ActiveSkillCategories: []domain.BrainToolCategory{domain.BrainToolReasoning},
			BrainToolPolicy: brainruntime.DefaultToolCallingPolicy(), BrainBudget: domain.BrainBudgetState{Limits: brainruntime.DefaultBudget()}, AgentHypotheses: []domain.AgentHypothesis{first, second},
			HypothesisGroundings: []domain.HypothesisGrounding{{HypothesisRevisionID: "h1", Level: domain.GroundingSupported, Evidence: domain.GroundingEvidence{EvidenceSupport: .9}}, {HypothesisRevisionID: "h2", Level: domain.GroundingPartial, Evidence: domain.GroundingEvidence{EvidenceSupport: .4}}},
		},
	}
	output, err := runBrainCompareHypotheses(withBrainWorkflowState(context.Background(), state), compareBrainHypothesesInput{Intent: "inspect objective validation differences", ExpectedObservation: []string{"coverage and missing observations for both revisions"}, HypothesisIDs: []string{"h1", "h2"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Comparisons) != 2 || output.Diagnosis != nil || state.AgentHypotheses[0].ModelConfidence != .8 || state.AgentHypotheses[1].ModelConfidence != .4 {
		t.Fatalf("Runtime comparison selected or changed a diagnosis belief: output=%+v hypotheses=%+v", output, state.AgentHypotheses)
	}
}

func TestDiagnosisValidationCannotRewriteBrainDiagnosis(t *testing.T) {
	now := time.Now().UTC()
	target := domain.ResourceRef{Namespace: "team-a", Service: "api", Resource: "api", Kind: "Service"}
	snapshot := domain.ExecutionSnapshot{SkillSnapshotHash: "skills", ModelConfigHash: "model", ToolSchemaHash: "tools", PolicyHash: "policy"}
	hypothesis := domain.AgentHypothesis{ID: "h1", Statement: "api is degraded by execution contention", Category: "application", Mechanism: "contention", TargetRefs: []domain.ResourceRef{target}, ModelConfidence: .73}
	grounding := domain.HypothesisGrounding{ID: "g1", HypothesisRevisionID: "h1", Level: domain.GroundingSupported, EvidenceSnapshotHash: "evidence", ValidatedAt: now}
	diagnosis := &domain.AgentDiagnosis{ID: "d1", HypothesisRevisionID: "h1", Statement: hypothesis.Statement, Category: hypothesis.Category, Mechanism: hypothesis.Mechanism, TargetRefs: hypothesis.TargetRefs, ModelConfidence: hypothesis.ModelConfidence, EvidenceIDs: []string{"e1"}, ValidationResultIDs: []string{"g1"}, EvidenceSnapshotHash: "evidence", ExecutionSnapshot: snapshot, Provisional: true}
	state := &WorkflowState{
		Incident: &domain.Incident{ID: "diagnosis-validation", Namespace: "team-a", Service: "api", Resource: "api", Evidence: []domain.Evidence{{ID: "e1", Source: "kubernetes"}}, Investigation: &domain.Investigation{}},
		BrainState: BrainState{
			BrainPhase: domain.BrainPhaseDiagnosis, ActiveToolCategory: domain.BrainToolReasoning, ActiveSkillCategories: []domain.BrainToolCategory{domain.BrainToolReasoning}, BrainToolPolicy: brainruntime.DefaultToolCallingPolicy(), BrainBudget: domain.BrainBudgetState{Limits: brainruntime.DefaultBudget()},
			AgentHypotheses: []domain.AgentHypothesis{hypothesis}, HypothesisAdmissions: []domain.HypothesisAdmission{{HypothesisRevisionID: "h1", Decision: "ADMITTED"}}, HypothesisGroundings: []domain.HypothesisGrounding{grounding}, AgentDiagnosis: diagnosis, EvidenceSnapshotHash: "evidence", ExecutionSnapshot: snapshot,
		},
	}
	output, err := runBrainValidateDiagnosis(withBrainWorkflowState(context.Background(), state), validateBrainDiagnosisInput{Intent: "validate frozen diagnosis references", ExpectedObservation: []string{"Runtime validation result"}, DiagnosisID: "d1"})
	if err != nil {
		t.Fatal(err)
	}
	if output.DiagnosisValidation == nil || !output.DiagnosisValidation.Valid || !output.DiagnosisFinalized || output.Diagnosis == nil || output.Diagnosis.Provisional || output.Diagnosis.Statement != diagnosis.Statement || output.Diagnosis.ModelConfidence != diagnosis.ModelConfidence {
		t.Fatalf("diagnosis validation did not preserve immutable Brain semantics: %+v", output)
	}
}

func TestWorkflowStateSerializesBrainUnderDedicatedBoundary(t *testing.T) {
	state := WorkflowState{Incident: &domain.Incident{ID: "checkpoint"}, BrainState: BrainState{BrainPhase: domain.BrainPhaseInvestigation, BrainBudget: domain.BrainBudgetState{Limits: brainruntime.DefaultBudget()}}}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(raw)
	if !strings.Contains(encoded, `"brain":{"phase":"INVESTIGATION"`) || strings.Contains(encoded, `"brain_phase"`) || strings.Contains(encoded, `"brain_messages"`) {
		t.Fatalf("Brain state did not use the new checkpoint boundary: %s", encoded)
	}
}
