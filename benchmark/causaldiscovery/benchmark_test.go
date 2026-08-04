package causaldiscovery

import "testing"

func TestDiscoveryBenchmarkFindsHiddenPath(t *testing.T) {
	metrics := Evaluate(DefaultCases())
	if metrics.Cases != 100 || metrics.PatternRecall != 1 {
		t.Fatalf("unexpected discovery metrics: %+v", metrics)
	}
	if metrics.PatternPrecision <= 0 || metrics.DiscoveredPatterns == 0 {
		t.Fatalf("discovery did not produce candidates: %+v", metrics)
	}
}
