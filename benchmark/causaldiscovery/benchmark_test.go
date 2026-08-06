package causaldiscovery

import "testing"

func TestDiscoveryBenchmarkFindsHiddenPath(t *testing.T) {
	metrics := Evaluate(DefaultCases())
	if metrics.Cases != 150 || metrics.PatternRecall != 1 {
		t.Fatalf("unexpected discovery metrics: %+v", metrics)
	}
	if metrics.PatternPrecision <= 0 || metrics.DiscoveredPatterns == 0 {
		t.Fatalf("discovery did not produce candidates: %+v", metrics)
	}
	if metrics.PatternF1 <= 0 || metrics.FalseDiscoveryRate != 0 || metrics.MeanPathEditDistance != 0 {
		t.Fatalf("unexpected causal quality metrics: %+v", metrics)
	}
}
