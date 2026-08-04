package evolution

import "testing"

func TestEvolutionEvaluator(t *testing.T) {
	m := Evaluate([]PatternCase{{Expected: []string{"memory_growth", "oom_killed"}, Found: []string{"memory_growth", "oom_killed"}, Confidence: 1}})
	if m.PatternPrecision != 1 || m.PatternRecall != 1 || m.ConfidenceCalibration != 1 {
		t.Fatalf("unexpected evolution metrics: %+v", m)
	}
}
