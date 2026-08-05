package log

import "testing"

func TestEvaluateTemplateMetrics(t *testing.T) {
	got := Evaluate([][]string{{"a"}, {"x", "b"}, {"x", "y", "z"}}, []string{"a", "b", "z"})
	if got.RecallAt1 != 1.0/3.0 || got.RecallAt5 != 1 || got.MRR <= .5 {
		t.Fatalf("metrics=%+v", got)
	}
}
