package causal

import (
	"testing"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func TestEvaluateMemoryCausalChain(t *testing.T) {
	metrics := Evaluate(nil, []Case{{Evidence: []domain.Evidence{{Source: "prometheus", Type: "memory_metric", Summary: "memory growth"}, {Source: "kubernetes", Summary: "OOMKilled pod restart"}, {Source: "loki", Summary: "error rate increase"}}, ExpectedCause: "memory_leak", ExpectedPath: []string{"memory_growth", "oom_killed", "pod_restart"}}})
	if metrics.RootCauseAccuracy != 1 || metrics.CausalPathCoverage < .99 {
		t.Fatalf("unexpected causal benchmark metrics: %+v", metrics)
	}
}
