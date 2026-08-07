package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/brainruntime"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/oklog/ulid/v2"
)

func brainRecoveryPermissionNode(ctx context.Context, state *WorkflowState, transition func(context.Context, *domain.Incident, domain.IncidentStatus) error) (*WorkflowState, error) {
	if state.AgentDiagnosis == nil {
		return finishBrainWithoutRecovery(ctx, state, transition, "Brain terminated without a persisted diagnosis")
	}
	grounding, ok := findHypothesisGrounding(state.HypothesisGroundings, state.AgentDiagnosis.HypothesisRevisionID)
	selected, selectedOK := findAgentHypothesis(state.AgentHypotheses, state.AgentDiagnosis.HypothesisRevisionID)
	if !ok || !selectedOK {
		return finishBrainWithoutRecovery(ctx, state, transition, "selected diagnosis has no matching hypothesis grounding")
	}
	selectedAdmission, admissionOK := findHypothesisAdmission(state.HypothesisAdmissions, selected.ID)
	kubernetes := false
	evidenceByID := map[string]domain.Evidence{}
	for _, item := range state.Incident.Evidence {
		evidenceByID[item.ID] = item
	}
	for _, id := range grounding.Evidence.SupportingEvidenceIDs {
		kubernetes = kubernetes || strings.EqualFold(evidenceByID[id].Source, "kubernetes")
	}
	otherSupport := 0.0
	validated := 0
	for _, item := range state.HypothesisGroundings {
		if admission, admitted := findHypothesisAdmission(state.HypothesisAdmissions, item.HypothesisRevisionID); admitted && admission.Decision == "ADMITTED" {
			validated++
		}
		if item.HypothesisRevisionID != grounding.HypothesisRevisionID && item.Evidence.EvidenceSupport > otherSupport {
			otherSupport = item.Evidence.EvidenceSupport
		}
	}
	eligibility := brainruntime.RecoveryAllowed(state.AgentDiagnosis, selected, grounding, state.HypothesisGroundings, state.ExecutionSnapshot, kubernetes, grounding.Evidence.EvidenceSupport-otherSupport, validated)
	if !admissionOK || selectedAdmission.Decision != "ADMITTED" || selectedAdmission.GroundingLevel == domain.AdmissionUnresolved {
		eligibility.Allowed = false
		eligibility.ReasonCodes = append(eligibility.ReasonCodes, "hypothesis_admission_not_recovery_eligible")
	}
	reason := strings.Join(eligibility.ReasonCodes, ",")
	permission := &domain.RecoveryPermission{Allowed: eligibility.Allowed, Level: "HUMAN", Reason: reason}
	if eligibility.Allowed {
		permission.Level, permission.Reason = "AUTO", "complete grounded diagnosis chain and snapshot consistency"
	}
	state.RecoveryPermission = permission
	state.Incident.Investigation.RecoveryPermission = permission
	if !eligibility.Allowed || state.AgentRecoveryPlan == nil {
		if state.AgentRecoveryPlan == nil && reason == "" {
			permission.Reason = "LLM did not submit a recovery plan"
		}
		return finishBrainWithoutRecovery(ctx, state, transition, permission.Reason)
	}
	plan := state.AgentRecoveryPlan
	if plan.ExecutionSnapshot != state.ExecutionSnapshot || plan.EvidenceSnapshotHash != state.EvidenceSnapshotHash || plan.DiagnosisVersion != state.AgentDiagnosis.ID {
		return finishBrainWithoutRecovery(ctx, state, transition, "recovery plan snapshot does not match the current diagnosis")
	}
	target, err := canonicalProposalTarget(plan.PrimaryAction.Target, state.Incident.Namespace, state.Incident.Resource)
	if err != nil {
		return finishBrainWithoutRecovery(ctx, state, transition, err.Error())
	}
	switch plan.PrimaryAction.Action {
	case domain.ActionRestartPod, domain.ActionScaleDeployment, domain.ActionRollbackDeployment:
	default:
		return finishBrainWithoutRecovery(ctx, state, transition, "recovery action is not registered")
	}
	state.Incident.Proposal = &domain.RecoveryProposal{ID: "proposal:" + ulid.Make().String(), Action: plan.PrimaryAction.Action, Namespace: state.Incident.Namespace, Target: target, Parameters: plan.PrimaryAction.Parameters, Reason: plan.PrimaryAction.Reason, Risk: plan.RiskReason, Diff: plan.ExpectedOutcome, Rollback: plan.RollbackPlan, Confidence: state.AgentDiagnosis.ModelConfidence, ExpiresAt: time.Now().UTC().Add(5 * time.Minute)}
	if state.Incident.Status == domain.StatusCollecting {
		if err = transition(ctx, state.Incident, domain.StatusDiagnosing); err != nil {
			return state, err
		}
	}
	if err = transition(ctx, state.Incident, domain.StatusProposing); err != nil {
		return state, err
	}
	return state, nil
}

func brainDryRunNode(ctx context.Context, state *WorkflowState, deps SupervisorDeps) (*WorkflowState, error) {
	result, err := dryRunProposal(ctx, deps.Executor, state.Incident)
	state.DryRun, state.Incident.DryRun = result, result
	now := time.Now().UTC()
	provenance := domain.ToolResultProvenance{ToolCallID: "dry-run:" + ulid.Make().String(), ToolName: "dry_run_recovery", ToolSchemaHash: state.ExecutionSnapshot.ToolSchemaHash, Collector: "safety-kernel", WindowStart: now, WindowEnd: now, ObservedAt: now, ParserVersion: "dry-run-v1"}
	if state.Incident.Proposal != nil && result != nil {
		provenance.MutationSpecHash, provenance.TargetUID, provenance.ResourceVersion = result.MutationSpecHash, state.Incident.Proposal.TargetUID, state.Incident.Proposal.ResourceVersion
		provenance.TargetRefs = []domain.ResourceRef{{Namespace: state.Incident.Proposal.Namespace, Resource: state.Incident.Proposal.Target}}
	}
	provenance.RawArtifactHash = brainruntime.Hash(result)
	record := domain.ToolResultRecord{Class: domain.ToolResultValidation, Provenance: provenance, Status: "DRY_RUN_SUCCEEDED", Summary: "Kubernetes server-side dry-run completed", NewInformation: true, OccurredAt: now}
	if err != nil || result == nil || !result.Success {
		record.Status, record.Summary = "DRY_RUN_FAILED", "Kubernetes server-side dry-run failed"
		trigger := domain.ReflectionRecoveryFailure
		state.PendingReflection = &trigger
		state.Termination = nil
		state.BrainPhase, state.ActiveToolCategory = domain.BrainPhaseRecovery, domain.BrainToolRecovery
		state.AgentRecoveryPlan, state.Incident.Proposal = nil, nil
	}
	state.ToolExecutions = append(state.ToolExecutions, domain.BrainToolExecution{Envelope: domain.AgentActionEnvelope{ActionID: provenance.ToolCallID, IncidentID: state.Incident.ID, TurnID: currentBrainTurnID(state), Phase: domain.BrainPhaseRecovery, ToolName: "dry_run_recovery", ToolCategory: domain.BrainToolRecovery, EvidenceSnapshotHash: state.EvidenceSnapshotHash}, Result: record})
	return state, nil
}

func appendBrainStateChange(state *WorkflowState, toolName, status, summary string, newInformation bool, approvalID string) {
	if state == nil || state.Incident == nil || state.WorkflowAttempt == nil {
		return
	}
	now := time.Now().UTC()
	provenance := domain.ToolResultProvenance{ToolCallID: toolName + ":" + ulid.Make().String(), ToolName: toolName, ToolSchemaHash: state.ExecutionSnapshot.ToolSchemaHash, Collector: "safety-kernel", WindowStart: now, WindowEnd: now, ObservedAt: now, ParserVersion: "state-change-v1", ApprovalID: approvalID}
	if state.Incident.Proposal != nil {
		provenance.TargetUID = state.Incident.Proposal.TargetUID
		provenance.ResourceVersion = state.Incident.Proposal.ResourceVersion
		provenance.TargetRefs = []domain.ResourceRef{{Namespace: state.Incident.Proposal.Namespace, Resource: state.Incident.Proposal.Target}}
	}
	if state.DryRun != nil {
		provenance.MutationSpecHash = state.DryRun.MutationSpecHash
	}
	provenance.RawArtifactHash = brainruntime.Hash(struct {
		Status  string `json:"status"`
		Summary string `json:"summary"`
	}{status, summary})
	class := domain.ToolResultStateChange
	if status == "APPROVAL_REQUESTED" {
		class = domain.ToolResultValidation
	} else if status == "APPROVAL_REJECTED" {
		class = domain.ToolResultConstraint
	}
	if class == domain.ToolResultStateChange {
		provenance.StateChangeID = "state-change:" + ulid.Make().String()
	}
	result := domain.ToolResultRecord{Class: class, Provenance: provenance, Status: status, Summary: summary, NewInformation: newInformation, OccurredAt: now}
	if class == domain.ToolResultConstraint {
		result.ConstraintCode = "approval_rejected"
	}
	state.ToolExecutions = append(state.ToolExecutions, domain.BrainToolExecution{Envelope: domain.AgentActionEnvelope{ActionID: provenance.ToolCallID, IncidentID: state.Incident.ID, TurnID: currentBrainTurnID(state), Phase: state.BrainPhase, ToolName: toolName, ToolCategory: domain.BrainToolRecovery, EvidenceSnapshotHash: state.EvidenceSnapshotHash}, Result: result})
	if state.Incident.Investigation != nil {
		state.Incident.Investigation.ToolExecutions = append([]domain.BrainToolExecution(nil), state.ToolExecutions...)
	}
}

func finishBrainWithoutRecovery(ctx context.Context, state *WorkflowState, transition func(context.Context, *domain.Incident, domain.IncidentStatus) error, reason string) (*WorkflowState, error) {
	preserve := state.Termination != nil && state.Termination.Reason != domain.TerminationDiagnosisConfident
	if !preserve {
		terminationReason := domain.TerminationSafetyBlocked
		if state.AgentDiagnosis != nil && state.AgentDiagnosis.Provisional {
			terminationReason = domain.TerminationDiagnosisProvisional
		}
		setFinalBrainTermination(state, terminationReason, []string{reason})
	}
	if state.Incident.Status != domain.StatusNeedsAttention {
		if state.Incident.Status == domain.StatusCollecting {
			_ = transition(ctx, state.Incident, domain.StatusDiagnosing)
		}
		if err := transition(ctx, state.Incident, domain.StatusNeedsAttention); err != nil {
			return state, err
		}
	}
	return state, nil
}

func setFinalBrainTermination(state *WorkflowState, reason domain.TerminationReason, gaps []string) {
	if state == nil {
		return
	}
	termination, err := brainruntime.NewTermination(reason, currentBrainTurnID(state), finalHypothesisID(state), state.EvidenceSnapshotHash, &state.ExecutionSnapshot, gaps, state.BrainBudget)
	if err == nil {
		state.Termination = &termination
		if state.Incident != nil && state.Incident.Investigation != nil {
			state.Incident.Investigation.Termination = &termination
		}
	}
}

func brainSupportSeparation(groundings []domain.HypothesisGrounding, selected string) float64 {
	values := append([]domain.HypothesisGrounding(nil), groundings...)
	sort.Slice(values, func(i, j int) bool { return values[i].Evidence.EvidenceSupport > values[j].Evidence.EvidenceSupport })
	selectedSupport, other := 0.0, 0.0
	for _, value := range values {
		if value.HypothesisRevisionID == selected {
			selectedSupport = value.Evidence.EvidenceSupport
		} else if value.Evidence.EvidenceSupport > other {
			other = value.Evidence.EvidenceSupport
		}
	}
	return selectedSupport - other
}

func brainRecoverySummary(state *WorkflowState) string {
	if state.AgentRecoveryPlan == nil {
		return ""
	}
	return fmt.Sprintf("%s:%s", state.AgentRecoveryPlan.PrimaryAction.Action, state.AgentRecoveryPlan.PrimaryAction.Target)
}

func brainReflectionAvailable(state *WorkflowState, trigger domain.ReflectionTrigger) bool {
	if state == nil || state.Incident == nil {
		return false
	}
	method, valid := domain.NormalizeDiagnosisMethod(state.Incident.DiagnosisMethod)
	if !valid || method == domain.DiagnosisMethodKubePilotNoReflection {
		return false
	}
	cost := brainruntime.ReflectionCost(trigger)
	return state.BrainBudget.Usage.ReflectionCostUnits+cost <= state.BrainBudget.Limits.MaxReflectionCostUnits
}
