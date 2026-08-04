package causalevolution

import "testing"

func TestEvolutionActivatesRepeatedCausalPattern(t *testing.T) {
	m := Evaluate(DefaultCases())
	if m.CausalAccuracy != 1 || m.PathCoverage < .99 {
		t.Fatalf("unexpected causal evolution metrics: %+v", m)
	}
}
