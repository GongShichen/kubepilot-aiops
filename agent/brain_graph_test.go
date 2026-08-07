package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/kubepilot-aiops/kubepilot/internal/brainruntime"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

type brainGraphModel struct {
	mu      sync.Mutex
	calls   int
	history []string
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
		Phase      domain.BrainPhase `json:"phase"`
		Evidence   []domain.Evidence `json:"evidence"`
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
	evidenceIDs := evidenceIDs(payload.Evidence)
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
			if available["query_prometheus_evidence"] {
				return call("query_prometheus_evidence", map[string]any{"intent": "test local resource pressure", "expected_observation": []string{"current resource saturation facts"}, "targets": []any{target}, "hypothesis_ids": hypothesisIDs, "evidence_need": []string{"resource pressure signal"}, "signal_kinds": []string{"cpu", "memory"}, "window_minutes": 5})
			}
			if strings.Contains(system, `<skill id="investigate-metrics"`) {
				return call("select_tool_category", map[string]any{"intent": "activate the metric evidence boundary", "expected_observation": []string{"Evidence ToolsNode selected for the next turn"}, "category": "EVIDENCE"})
			}
			attemptedWithoutSkill := false
			for _, prior := range m.history {
				attemptedWithoutSkill = attemptedWithoutSkill || prior == string(domain.BrainPhaseInvestigation)+":select_tool_category"
			}
			if !attemptedWithoutSkill {
				return call("select_tool_category", map[string]any{"intent": "attempt evidence collection before the required Skill is active", "expected_observation": []string{"bounded category decision"}, "category": "EVIDENCE"})
			}
			return call("request_skills", map[string]any{"intent": "load the metric investigation procedure", "expected_observation": []string{"versioned metric Skill activation decision"}, "skill_ids": []string{"investigate-metrics"}, "reason": "resource pressure is a leading discriminating observation", "trigger": "HYPOTHESIS_CONFLICT"})
		}
		if !hasKubernetes {
			if available["query_kubernetes_evidence"] {
				return call("query_kubernetes_evidence", map[string]any{"intent": "obtain an independent Kubernetes source", "expected_observation": []string{"current workload readiness and restart facts"}, "targets": []any{target}, "hypothesis_ids": hypothesisIDs, "evidence_need": []string{"resource pressure signal"}, "signal_kinds": []string{"workload", "pod"}, "window_minutes": 5})
			}
			if strings.Contains(system, `<skill id="inspect-kubernetes"`) {
				return call("select_tool_category", map[string]any{"intent": "activate the Kubernetes evidence boundary", "expected_observation": []string{"Evidence ToolsNode selected for the next turn"}, "category": "EVIDENCE"})
			}
			return call("request_skills", map[string]any{"intent": "load the Kubernetes inspection procedure", "expected_observation": []string{"versioned Kubernetes Skill activation decision"}, "skill_ids": []string{"inspect-kubernetes"}, "reason": "automatic recovery requires an independent Kubernetes source", "trigger": "GROUNDING_GAP"})
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
		if execution.Result.Provenance.ToolCallID == "" || execution.Result.Provenance.ToolName == "" || execution.Result.Provenance.ToolSchemaHash == "" || execution.Result.Provenance.ObservedAt.IsZero() {
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
}

func TestNoReflectionAblationBypassesPendingReflection(t *testing.T) {
	trigger := domain.ReflectionHypothesisRefuted
	state := &WorkflowState{
		Incident:          &domain.Incident{ID: "no-reflection", DiagnosisMethod: domain.DiagnosisMethodKubePilotNoReflection},
		PendingReflection: &trigger,
		BrainBudget:       domain.BrainBudgetState{Limits: brainruntime.DefaultBudget()},
	}
	next, err := (&brainGraphRuntime{}).reflectionRoute(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if next != "brain_termination_router" || state.PendingReflection != nil || len(state.Reflections) != 0 || state.BrainBudget.Usage.ReflectionCostUnits != 0 {
		t.Fatalf("no-reflection ablation executed reflection: next=%s state=%+v", next, state)
	}
}

func TestReflectionRouteStopsAtBrainTurnBudget(t *testing.T) {
	trigger := domain.ReflectionGroundingFailure
	budget := brainruntime.DefaultBudget()
	state := &WorkflowState{
		Incident:             &domain.Incident{ID: "reflection-budget", DiagnosisMethod: domain.DiagnosisMethodKubePilot, Investigation: &domain.Investigation{}},
		PendingReflection:    &trigger,
		BrainBudget:          domain.BrainBudgetState{Limits: budget, Usage: domain.BrainBudgetUsage{Turns: budget.MaxTurns}},
		ExecutionSnapshot:    domain.ExecutionSnapshot{SkillSnapshotHash: "skills", ModelConfigHash: "model", ToolSchemaHash: "tools", PolicyHash: "policy"},
		EvidenceSnapshotHash: "evidence",
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
		Incident:          &domain.Incident{ID: "verification-reflection", DiagnosisMethod: domain.DiagnosisMethodKubePilot, Status: domain.StatusVerifying, Investigation: &domain.Investigation{Architecture: "eino-native-self-reflective-brain"}},
		BrainBudget:       domain.BrainBudgetState{Limits: brainruntime.DefaultBudget()},
		Termination:       &domain.TerminationEvent{Reason: domain.TerminationDiagnosisConfident},
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
	if result.Termination == nil || result.Termination.Reason != domain.TerminationRecoveryFailed || result.PendingTermination != "" {
		t.Fatalf("post-failure reflection did not terminate safely: %+v", result.Termination)
	}
}

func TestUnresolvedHypothesisCannotAuthorizeRecovery(t *testing.T) {
	snapshot := domain.ExecutionSnapshot{SkillSnapshotHash: "skill", ModelConfigHash: "model", ToolSchemaHash: "tool", PolicyHash: "policy"}
	selected := domain.AgentHypothesis{ID: "h1", LastValidatedSnapshotHash: "evidence", Status: domain.HypothesisSupported}
	state := &WorkflowState{
		Incident:        &domain.Incident{ID: "unresolved", DiagnosisMethod: domain.DiagnosisMethodKubePilot, Status: domain.StatusCollecting, Namespace: "team-a", Resource: "api", Investigation: &domain.Investigation{}},
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
	state := &WorkflowState{Incident: &domain.Incident{ID: "termination", DiagnosisMethod: domain.DiagnosisMethodKubePilot}, BrainToolPolicy: brainruntime.DefaultToolCallingPolicy()}
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
		Incident:              &domain.Incident{ID: "diagnosis-boundary", DiagnosisMethod: domain.DiagnosisMethodKubePilot, Evidence: []domain.Evidence{evidence}},
		BrainPhase:            domain.BrainPhaseDiagnosis,
		AgentHypotheses:       []domain.AgentHypothesis{hypothesis},
		HypothesisGroundings:  []domain.HypothesisGrounding{grounding},
		EvidenceSnapshotHash:  "snapshot",
		BrainToolPolicy:       brainruntime.DefaultToolCallingPolicy(),
		BrainBudget:           domain.BrainBudgetState{Limits: brainruntime.DefaultBudget()},
		ActiveSkillCategories: []domain.BrainToolCategory{domain.BrainToolReasoning},
		ActiveToolCategory:    domain.BrainToolReasoning,
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
	assistant := &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "call-1", Type: "function", Function: schema.FunctionCall{Name: "query_kubernetes_evidence", Arguments: `{"intent":"inspect readiness","expected_observation":["current pod state"]}`}}}}
	tool := &schema.Message{Role: schema.Tool, ToolCallID: "call-1", ToolName: "query_kubernetes_evidence", Content: `{"class":"EVIDENCE","status":"OK","summary":"pod is not ready","provenance":{"evidence_ids":["e1"]}}`}
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
	message := &schema.Message{Role: schema.Assistant, ReasoningContent: "hidden", ToolCalls: []schema.ToolCall{{ID: "call-1", Type: "function", Function: schema.FunctionCall{Name: "query_kubernetes_evidence", Arguments: `{"intent":"inspect readiness","expected_observation":["current pod state"]}`}}}}
	sanitized, record, persisted := normalizeBrainAssistantOutput(message, "turn-13", time.Now().UTC())
	if !persisted || !record.Persisted || !record.ToolCallPresent || record.ContentPresent || !record.ReasoningPresent {
		t.Fatalf("ToolCall Assistant audit was incorrect: %+v", record)
	}
	if sanitized.ReasoningContent != "" || !strings.Contains(sanitized.Content, `"type":"assistant_tool_calls"`) || !strings.Contains(sanitized.Content, "inspect readiness") {
		t.Fatalf("ToolCall Assistant did not receive a visible provider-neutral summary: %+v", sanitized)
	}
}

func TestToolHistoryUsesServerClassifiedResultWithProvenance(t *testing.T) {
	assistant := &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "call-1", Type: "function", Function: schema.FunctionCall{Name: "query_kubernetes_evidence", Arguments: `{"intent":"inspect readiness","expected_observation":["current pod state"],"targets":[{"namespace":"team-a","service":"api"}]}`}}}}
	now := time.Now().UTC()
	output := brainCapabilityOutput{Class: domain.ToolResultConstraint, Status: "REJECTED", Summary: "scope denied", ConstraintCode: "cross_namespace_denied", Provenance: domain.ToolResultProvenance{Collector: "constraint-kernel", WindowStart: now, WindowEnd: now, ObservedAt: now, RawArtifactHash: "artifact", ParserVersion: "v1"}}
	raw, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	state := &WorkflowState{
		Incident:          &domain.Incident{ID: "tool-result", Investigation: &domain.Investigation{}},
		BrainMessages:     []*schema.Message{assistant},
		ExecutionSnapshot: domain.ExecutionSnapshot{ToolSchemaHash: "schema-hash"},
		BrainBudget:       domain.BrainBudgetState{Limits: brainruntime.DefaultBudget()},
	}
	message := &schema.Message{Role: schema.Tool, ToolCallID: "call-1", ToolName: "query_kubernetes_evidence", Content: string(raw)}
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
		Incident:              &domain.Incident{ID: "reflection-admission", Namespace: "team-a", Service: "api", Resource: "api", Investigation: &domain.Investigation{}},
		BrainPhase:            domain.BrainPhaseReflection,
		ResumeBrainPhase:      domain.BrainPhaseInvestigation,
		BrainToolPolicy:       brainruntime.DefaultToolCallingPolicy(),
		BrainBudget:           domain.BrainBudgetState{Limits: brainruntime.DefaultBudget()},
		ActiveToolCategory:    domain.BrainToolReasoning,
		ActiveSkillCategories: []domain.BrainToolCategory{domain.BrainToolReasoning},
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
	output, err := runBrainSubmitHypotheses(withBrainWorkflowState(context.Background(), state), input)
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
		Incident:              &domain.Incident{ID: "reflection-skill", Namespace: "team-a", Service: "api", Resource: "api", Investigation: &domain.Investigation{}},
		BrainPhase:            domain.BrainPhaseReflection,
		ResumeBrainPhase:      domain.BrainPhaseInvestigation,
		BrainToolPolicy:       brainruntime.DefaultToolCallingPolicy(),
		BrainBudget:           domain.BrainBudgetState{Limits: brainruntime.DefaultBudget()},
		ActiveToolCategory:    domain.BrainToolReasoning,
		ActiveSkillCategories: []domain.BrainToolCategory{domain.BrainToolReasoning, domain.BrainToolControl},
		Reflections:           []domain.ReflectionRecord{{ID: "reflection:constraint", Trigger: domain.ReflectionConstraintFailure}},
	}
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
	resolved, err := resolver.Resolve(state.BrainPhase, state.RequestedSkills, state.BrainBudget.Limits.MaxOptionalSkillsPerTurn)
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

func TestRejectedSkillRequestReturnsExplicitConstraintAndAudit(t *testing.T) {
	resolver, err := LoadDefaultBrainSkillResolver()
	if err != nil {
		t.Fatal(err)
	}
	state := &WorkflowState{
		Incident:              &domain.Incident{ID: "rejected-skill", Namespace: "team-a", Service: "api", Resource: "api", Investigation: &domain.Investigation{}},
		BrainPhase:            domain.BrainPhaseReflection,
		ResumeBrainPhase:      domain.BrainPhaseInvestigation,
		BrainToolPolicy:       brainruntime.DefaultToolCallingPolicy(),
		BrainBudget:           domain.BrainBudgetState{Limits: brainruntime.DefaultBudget()},
		ActiveToolCategory:    domain.BrainToolReasoning,
		ActiveSkillCategories: []domain.BrainToolCategory{domain.BrainToolReasoning, domain.BrainToolControl},
	}
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
