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
	if inv.Termination == nil || inv.Termination.Reason != domain.TerminationDiagnosisConfident || inv.WorkflowAttempt == nil || inv.WorkflowAttempt.Status != domain.WorkflowAttemptInterrupted {
		t.Fatalf("unexpected pre-approval termination/attempt: termination=%+v attempt=%+v", inv.Termination, inv.WorkflowAttempt)
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
