package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

func TestPrometheusQueryRange(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query_range" || r.URL.Query().Get("step") != "15" || r.URL.Query().Get("start") == "" || r.URL.Query().Get("end") == "" {
			t.Fatalf("unexpected range request %s", r.URL.String())
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "matrix", "result": []any{}}})
	}))
	defer server.Close()
	now := time.Now().UTC()
	result, err := NewPrometheus(server.URL).QueryRange(context.Background(), "up", now.Add(-time.Minute), now, 15*time.Second)
	if err != nil || result.Query != "up" || result.ResultType != "matrix" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}
