package agent

import (
	"context"
	"testing"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/brainruntime"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func TestReviseInvestigationPlanCreatesImmutableBoundRevision(t *testing.T) {
	now := time.Now().UTC()
	state := &WorkflowState{
		Incident: &domain.Incident{ID: "incident-1", Namespace: "team-a", Service: "api"},
		BrainState: BrainState{
			BrainPhase: domain.BrainPhaseInvestigation, ActiveToolCategory: domain.BrainToolReasoning,
			ActiveSkillCategories: []domain.BrainToolCategory{domain.BrainToolReasoning},
			BrainTurns:            []domain.BrainTurn{{ID: "turn-2", Phase: domain.BrainPhaseInvestigation}},
			InvestigationPlan:     &domain.InvestigationPlan{ID: "plan-1", Version: 1, Objective: "initial", CreatedAt: now},
			AgentHypotheses:       []domain.AgentHypothesis{{ID: "hyp-1", LineageID: "lineage-1", Version: 1, Status: domain.HypothesisInvestigating}},
			HypothesisAdmissions:  []domain.HypothesisAdmission{{HypothesisRevisionID: "hyp-1", Decision: "ADMITTED"}},
			BrainBudget:           domain.BrainBudgetState{Limits: brainruntime.DefaultBudget()},
			BrainToolPolicy:       brainruntime.DefaultToolCallingPolicy(),
		},
	}
	input := reviseInvestigationPlanInput{
		Intent: "replace a low-information step", ExpectedObservation: []string{"immutable plan revision"},
		ParentPlanID: "plan-1", RevisionReason: "new grounding eliminated the original branch", HypothesisIDs: []string{"hyp-1"},
		Objective: "resolve the remaining conflict", Goals: []string{"inspect the discriminating current state"}, StopConditions: []string{"conflict resolved"},
	}
	output, err := runBrainRevisePlan(withBrainWorkflowState(context.Background(), state), input)
	if err != nil {
		t.Fatal(err)
	}
	if output.Class != domain.ToolResultValidation || output.InvestigationPlan == nil || output.InvestigationPlan.ID == "plan-1" || output.InvestigationPlan.ParentID != "plan-1" || output.InvestigationPlan.Version != 2 || output.InvestigationPlan.RevisionReason == "" {
		t.Fatalf("plan revision lost immutable lineage: %+v", output)
	}
	input.ParentPlanID = "stale-plan"
	stale, err := runBrainRevisePlan(withBrainWorkflowState(context.Background(), state), input)
	if err != nil {
		t.Fatal(err)
	}
	if stale.Class != domain.ToolResultConstraint || stale.ConstraintCode != "stale_investigation_plan" {
		t.Fatalf("stale parent plan was not rejected: %+v", stale)
	}
}

func TestHypothesisValidationRequiresAttributionForEveryBoundEvidence(t *testing.T) {
	now := time.Now().UTC()
	state := &WorkflowState{
		Incident: &domain.Incident{ID: "incident-2", Namespace: "team-a", Service: "api", EvidenceStartAt: now.Add(-time.Minute), Evidence: []domain.Evidence{{ID: "e1", Source: "prometheus", ObservedAt: now}, {ID: "e2", Source: "kubernetes", ObservedAt: now}}},
		BrainState: BrainState{
			BrainPhase: domain.BrainPhaseInvestigation, ActiveToolCategory: domain.BrainToolReasoning,
			ActiveSkillCategories: []domain.BrainToolCategory{domain.BrainToolReasoning}, BrainTurns: []domain.BrainTurn{{ID: "turn-3"}},
			AgentHypotheses:      []domain.AgentHypothesis{{ID: "hyp-1", LineageID: "lineage-1", Version: 1, Statement: "api is degraded", Mechanism: "open mechanism", TargetRefs: []domain.ResourceRef{{Namespace: "team-a", Service: "api"}}, EvidenceNeeds: []string{"current state"}, FalsificationConditions: []string{"state is normal"}, Status: domain.HypothesisAdmitted}},
			HypothesisAdmissions: []domain.HypothesisAdmission{{HypothesisRevisionID: "hyp-1", Decision: "ADMITTED", ResourceScope: []domain.ResourceScopeDecision{{Allowed: true}}}},
			ToolExecutions:       []domain.BrainToolExecution{{Envelope: domain.AgentActionEnvelope{ToolName: "query_metrics", ToolCategory: domain.BrainToolEvidence, Intent: domain.AgentActionIntent{HypothesisIDs: []string{"hyp-1"}}}, Result: domain.ToolResultRecord{Class: domain.ToolResultEvidence, NewInformation: true, Provenance: domain.ToolResultProvenance{EvidenceIDs: []string{"e1", "e2"}}}}},
			BrainBudget:          domain.BrainBudgetState{Limits: brainruntime.DefaultBudget()}, BrainToolPolicy: brainruntime.DefaultToolCallingPolicy(),
		},
	}
	input := validateBrainHypothesisInput{Intent: "validate all bound current Evidence", ExpectedObservation: []string{"explicit attribution"}, HypothesisID: "hyp-1", Attributions: []domain.EvidenceAttributionIntent{{EvidenceID: "e1", Relation: domain.EvidenceSupports, Weight: .8, Reason: "current metric"}}}
	output, err := runBrainValidateHypothesis(withBrainWorkflowState(context.Background(), state), input)
	if err != nil {
		t.Fatal(err)
	}
	if output.Class != domain.ToolResultConstraint || output.ConstraintCode != "unattributed_bound_evidence" {
		t.Fatalf("partial Evidence attribution was admitted: %+v", output)
	}
	input.Attributions = append(input.Attributions, domain.EvidenceAttributionIntent{EvidenceID: "e2", Relation: domain.EvidenceNeutral, Weight: .2, Reason: "current but non-discriminating"})
	output, err = runBrainValidateHypothesis(withBrainWorkflowState(context.Background(), state), input)
	if err != nil {
		t.Fatal(err)
	}
	if output.Class != domain.ToolResultValidation || len(output.EvidenceAttributions) != 2 || len(output.Grounding.Evidence.NeutralEvidenceIDs) != 1 {
		t.Fatalf("complete attribution set was not persisted: %+v", output)
	}
}
