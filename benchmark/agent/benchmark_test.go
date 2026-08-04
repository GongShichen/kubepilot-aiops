package agent

import "testing"

func TestAgentBudgetEvaluator(t *testing.T) {
	if !WithinBudget(Observation{ToolCalls: 2, ToolCost: 3}, Budget{MaxToolUses: 2, MaxToolCost: 3}) {
		t.Fatal("valid observation rejected")
	}
	if WithinBudget(Observation{ToolCalls: 3}, Budget{MaxToolUses: 2}) {
		t.Fatal("budget overflow accepted")
	}
}
