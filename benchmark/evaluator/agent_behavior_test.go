package evaluator

import (
	"testing"

	"github.com/kubepilot-aiops/kubepilot/benchmark/reporter"
)

func TestEvaluateAgentCaseResultsUsesObservedRuntimeData(t *testing.T) {
	metrics := EvaluateAgentCaseResults([]reporter.CaseResult{
		{AgentIterations: 4, AgentToolUses: 10, AgentToolCost: 1000, AgentCorrections: 1, SelfCorrectionSucceeded: true, IndependentEvidenceRequests: 2, NewEvidenceIDs: 4, ConvergenceRounds: 1, CognitiveProposals: 2, CognitiveAcceptedProposals: 2, CognitiveUsefulProposals: 1, HypothesisCount: 2, HypothesisCorrectionOpportunities: 1, GroundedHypothesisCorrections: 1, GroundedDecision: true, NonControlToolResults: 4, InformativeToolResults: 3, ReflectionTriggers: 2, AcceptedReflections: 1, SkillActivations: 4, AcceptedSkillActivations: 4, AcceptedHypothesisAdmissions: 2, GroundableHypothesisAdmissions: 1},
		{AgentIterations: 6, AgentToolUses: 20, AgentToolCost: 1, Error: "Agent budget exhausted", IndependentEvidenceRequests: 4, NewEvidenceIDs: 2, ConvergenceRounds: 2, CognitiveProposals: 2, CognitiveAcceptedProposals: 1, HypothesisCount: 2, NonControlToolResults: 2, InformativeToolResults: 1, SkillActivations: 4, AcceptedSkillActivations: 2, SkillDrift: 1, AcceptedHypothesisAdmissions: 1, UnsupportedDiagnosis: true, IncompleteToolProvenance: 2},
	})
	if metrics.AverageIterations != 5 || metrics.AverageToolCalls != 15 || metrics.AverageToolCost != 500.5 {
		t.Fatalf("unexpected metrics: %+v", metrics)
	}
	if metrics.BudgetExhaustRate != .5 || metrics.CorrectionSuccessRate != 1 {
		t.Fatalf("unexpected rates: %+v", metrics)
	}
	if metrics.AverageCollectorRequests != 3 || metrics.AverageNewEvidenceIDs != 3 || metrics.AverageConvergenceRounds != 1.5 || metrics.CognitiveProposalPrecision != .25 || metrics.CognitiveProposalAcceptance != .75 || metrics.IneffectiveSupplementRate != .5 {
		t.Fatalf("cognitive efficiency metrics were not calculated from runtime observations: %+v", metrics)
	}
	if metrics.HypothesisCorrectionRate != 1 || metrics.GroundedDecisionRate != .5 || metrics.ToolEfficiency != 2.0/3.0 || metrics.ReflectionTriggerPrecision != .5 || metrics.SkillAdherence != .75 || metrics.SkillDrift != 1 || metrics.AdmissionPrecision != 1.0/3.0 || metrics.UnsupportedHypothesisRate != .5 || metrics.ToolProvenanceCompleteness != 28.0/30.0 {
		t.Fatalf("self-reflective metrics were not calculated from runtime observations: %+v", metrics)
	}
}
