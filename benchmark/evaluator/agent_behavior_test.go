package evaluator

import (
	"testing"

	"github.com/kubepilot-aiops/kubepilot/benchmark/reporter"
)

func TestEvaluateAgentCaseResultsUsesObservedRuntimeData(t *testing.T) {
	metrics := EvaluateAgentCaseResults([]reporter.CaseResult{
		{AgentIterations: 4, AgentToolUses: 10, AgentToolCost: 1000, AgentCorrections: 1, SelfCorrectionSucceeded: true},
		{AgentIterations: 6, AgentToolUses: 20, AgentToolCost: 1, Error: "Agent budget exhausted"},
	})
	if metrics.AverageIterations != 5 || metrics.AverageToolCalls != 15 || metrics.AverageToolCost != 500.5 {
		t.Fatalf("unexpected metrics: %+v", metrics)
	}
	if metrics.BudgetExhaustRate != .5 || metrics.CorrectionSuccessRate != 1 {
		t.Fatalf("unexpected rates: %+v", metrics)
	}
}
