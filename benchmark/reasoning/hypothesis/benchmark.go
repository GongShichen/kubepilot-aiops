// Package hypothesis evaluates diagnosis reasoning without invoking the
// production Agent. It consumes an observed result and keeps expected labels
// in the evaluator process only.
package hypothesis

type Hypothesis struct {
	ID        string `json:"id"`
	Cause     string `json:"cause"`
	Verified  bool   `json:"verified"`
	Supported bool   `json:"supported"`
}
type Case struct {
	ExpectedCause string
	Hypotheses    []Hypothesis
}
type Metrics struct {
	Cases             int     `json:"cases"`
	HypothesisRecall  float64 `json:"hypothesis_recall"`
	FalsePositiveRate float64 `json:"false_positive_rate"`
	AverageHypotheses float64 `json:"average_hypotheses"`
}

func Evaluate(cases []Case) Metrics {
	out := Metrics{Cases: len(cases)}
	if len(cases) == 0 {
		return out
	}
	for _, c := range cases {
		correct, falseCount := 0, 0
		for _, h := range c.Hypotheses {
			if h.Cause == c.ExpectedCause && (h.Supported || h.Verified) {
				correct++
			} else {
				falseCount++
			}
		}
		if correct > 0 {
			out.HypothesisRecall++
		}
		if len(c.Hypotheses) > 0 {
			out.FalsePositiveRate += float64(falseCount) / float64(len(c.Hypotheses))
		}
		out.AverageHypotheses += float64(len(c.Hypotheses))
	}
	d := float64(len(cases))
	out.HypothesisRecall /= d
	out.FalsePositiveRate /= d
	out.AverageHypotheses /= d
	return out
}
