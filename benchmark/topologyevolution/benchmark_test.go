package topologyevolution

import "testing"

func TestEvolutionMergesSharedDatabasePattern(t *testing.T) {
	m := Evaluate(DefaultCases())
	if m.PatternPrecision != 1 || m.PatternRecall != 1 || m.PatternsLearned != 1 {
		t.Fatalf("unexpected topology evolution metrics: %+v", m)
	}
}
