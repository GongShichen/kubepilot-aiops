package tools

import (
	"strings"
	"testing"
)

func TestMetricQueriesCoverRecoverySignals(t *testing.T) {
	queries := MetricQueries("kubepilot-benchmark", "gateway-service")
	for _, name := range []string{"cpu", "cpu_current", "cpu_throttling", "cpu_throttling_current", "memory", "qps", "qps_current", "error_rate", "error_rate_current", "p95_latency", "p95_latency_current", "restarts", "deployment_availability"} {
		query, ok := queries[name]
		if !ok || query == "" {
			t.Fatalf("missing query %s", name)
		}
		if !strings.Contains(query, "kubepilot-benchmark") {
			t.Fatalf("query %s is not namespace scoped: %s", name, query)
		}
	}
}
