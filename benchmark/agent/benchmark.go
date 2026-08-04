// Package agent contains evaluator-side Agent behavior measurements. It does
// not import the production agent package, so benchmark code cannot become a
// hidden runtime dependency.
package agent

type Observation struct {
	ToolCalls           int  `json:"tool_calls"`
	ToolCost            int  `json:"tool_cost"`
	Iterations          int  `json:"iterations"`
	Tokens              int  `json:"tokens"`
	Corrections         int  `json:"corrections"`
	SafetyRejections    int  `json:"safety_rejections"`
	BudgetExhausted     bool `json:"budget_exhausted"`
	CorrectionSucceeded bool `json:"correction_succeeded"`
}
type Metrics struct {
	Cases                 int     `json:"cases"`
	AverageToolCalls      float64 `json:"average_tool_calls"`
	AverageToolCost       float64 `json:"average_tool_cost"`
	AverageIterations     float64 `json:"average_iterations"`
	BudgetExhaustRate     float64 `json:"budget_exhaust_rate"`
	CorrectionSuccessRate float64 `json:"correction_success_rate"`
	AverageCorrections    float64 `json:"average_corrections"`
}

func Evaluate(observations []Observation) Metrics {
	o := Metrics{Cases: len(observations)}
	if len(observations) == 0 {
		return o
	}
	exhausted, corrected, success := 0, 0, 0
	for _, v := range observations {
		o.AverageToolCalls += float64(v.ToolCalls)
		o.AverageToolCost += float64(v.ToolCost)
		o.AverageIterations += float64(v.Iterations)
		o.AverageCorrections += float64(v.Corrections)
		if v.BudgetExhausted {
			exhausted++
		}
		if v.Corrections > 0 {
			corrected++
			if v.CorrectionSucceeded {
				success++
			}
		}
	}
	d := float64(len(observations))
	o.AverageToolCalls /= d
	o.AverageToolCost /= d
	o.AverageIterations /= d
	o.AverageCorrections /= d
	o.BudgetExhaustRate = float64(exhausted) / d
	if corrected > 0 {
		o.CorrectionSuccessRate = float64(success) / float64(corrected)
	}
	return o
}

type Budget struct {
	MaxToolUses    int
	MaxToolCost    int
	MaxTokens      int
	MaxCorrections int
}

func WithinBudget(o Observation, b Budget) bool {
	return (b.MaxToolUses <= 0 || o.ToolCalls <= b.MaxToolUses) && (b.MaxToolCost <= 0 || o.ToolCost <= b.MaxToolCost) && (b.MaxTokens <= 0 || o.Tokens <= b.MaxTokens) && (b.MaxCorrections <= 0 || o.Corrections <= b.MaxCorrections)
}
