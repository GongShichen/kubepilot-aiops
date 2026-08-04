package causal

import "testing"

func TestCausalBenchmarkScoresPathAndCause(t *testing.T) {
	m := Evaluate([]Case{{PredictedCause: "memory_leak", PredictedPath: []string{"memory_growth", "oom_killed", "pod_restart"}, ExpectedCause: "memory_leak", ExpectedPath: []string{"memory_growth", "oom_killed", "pod_restart"}}})
	if m.CausalAccuracy != 1 || m.PathCoverage != 1 || m.PatternPrecision != 1 || m.PatternRecall != 1 {
		t.Fatalf("unexpected causal metrics: %+v", m)
	}
}
