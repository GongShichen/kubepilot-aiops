package hypothesis

import "testing"

func TestHypothesisEvaluator(t *testing.T) {
	m := Evaluate([]Case{{ExpectedCause: "memory_leak", Hypotheses: []Hypothesis{{Cause: "memory_leak", Verified: true}, {Cause: "database", Verified: false}}}})
	if m.HypothesisRecall != 1 || m.FalsePositiveRate != .5 {
		t.Fatalf("unexpected hypothesis metrics: %+v", m)
	}
}
