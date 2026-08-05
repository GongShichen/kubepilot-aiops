package evaluator

import (
	"strings"

	"github.com/kubepilot-aiops/kubepilot/benchmark/reporter"
)

// AgentBehaviorMetrics evaluates persisted observations from the live public
// Agent run. Runtime limits and safety decisions remain production concerns.
type AgentBehaviorMetrics struct {
	Cases                 int     `json:"cases"`
	AverageToolCalls      float64 `json:"average_tool_calls"`
	AverageToolCost       float64 `json:"average_tool_cost"`
	AverageIterations     float64 `json:"average_iterations"`
	BudgetExhaustRate     float64 `json:"budget_exhaust_rate"`
	CorrectionSuccessRate float64 `json:"correction_success_rate"`
	AverageCorrections    float64 `json:"average_corrections"`
}

func EvaluateAgentCaseResults(items []reporter.CaseResult) AgentBehaviorMetrics {
	metrics := AgentBehaviorMetrics{Cases: len(items)}
	if len(items) == 0 {
		return metrics
	}
	exhausted, corrected, correctionSucceeded := 0, 0, 0
	for _, item := range items {
		metrics.AverageIterations += float64(item.AgentIterations)
		metrics.AverageToolCalls += float64(item.AgentToolUses)
		metrics.AverageToolCost += float64(item.AgentToolCost)
		metrics.AverageCorrections += float64(item.AgentCorrections)
		if strings.Contains(strings.ToLower(item.Error), "budget exhausted") {
			exhausted++
		}
		if item.AgentCorrections > 0 {
			corrected++
			if item.SelfCorrectionSucceeded {
				correctionSucceeded++
			}
		}
	}
	denominator := float64(len(items))
	metrics.AverageToolCalls /= denominator
	metrics.AverageToolCost /= denominator
	metrics.AverageIterations /= denominator
	metrics.AverageCorrections /= denominator
	metrics.BudgetExhaustRate = float64(exhausted) / denominator
	if corrected > 0 {
		metrics.CorrectionSuccessRate = float64(correctionSucceeded) / float64(corrected)
	}
	return metrics
}
