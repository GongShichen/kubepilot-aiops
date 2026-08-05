package agent

import (
	"strings"

	"github.com/kubepilot-aiops/kubepilot/benchmark/reporter"
)

// EvaluateCaseResults converts observations emitted by the real public Agent
// benchmark into behavior metrics. It never creates an Agent or synthesizes a
// model response; the input is always the persisted result of a live run.
func EvaluateCaseResults(items []reporter.CaseResult) Metrics {
	observations := make([]Observation, 0, len(items))
	for _, item := range items {
		observations = append(observations, Observation{
			ToolCalls:           item.AgentToolUses,
			ToolCost:            item.AgentToolCost,
			Tokens:              item.AgentTokens,
			Corrections:         item.AgentCorrections,
			SafetyRejections:    item.SafetyRejections,
			BudgetExhausted:     strings.Contains(strings.ToLower(item.Error), "budget exhausted"),
			CorrectionSucceeded: item.SelfCorrectionSucceeded,
		})
	}
	return Evaluate(observations)
}
