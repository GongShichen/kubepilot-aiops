package evidence

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func TestNormalTraceDoesNotOutrankAnomalousMetricOrLog(t *testing.T) {
	now := time.Now().UTC()
	incident := &domain.Incident{Namespace: "team-a", Service: "checkout", Resource: "checkout", CreatedAt: now}
	items := []domain.Evidence{
		{ID: "normal-trace", Source: "jaeger", Type: "trace", Timestamp: now, Namespace: "team-a", Service: "checkout", Resource: "checkout", Facts: map[string]any{"duration_micros": 20_000}},
		{ID: "cpu", Source: "prometheus", Type: "cpu", Timestamp: now, Namespace: "team-a", Service: "checkout", Resource: "checkout", Facts: map[string]any{"current_value": .98, "baseline_value": .20}},
		{ID: "error-log", Source: "loki", Type: "log", Timestamp: now, Namespace: "team-a", Service: "checkout", Resource: "checkout", Summary: "connection timeout", Facts: map[string]any{"level": "error", "occurrence_count": 10}},
	}
	ranked := Rank(DefaultPolicy(), incident, items)
	if ranked[len(ranked)-1].ID != "normal-trace" || ranked[len(ranked)-1].AnomalyScore != 0 {
		t.Fatalf("normal trace outranked anomalous evidence: %+v", ranked)
	}
}

func TestTypedAnomalyParsersCoverMetricTraceAndKubernetesFacts(t *testing.T) {
	items := []domain.Evidence{
		{ID: "metric-change", Source: "prometheus", Type: "cpu", Facts: map[string]any{"current_value": .9, "baseline_value": .3}},
		{ID: "metric-result", Source: "prometheus", Type: "latency", Facts: map[string]any{"result": []any{map[string]any{"value": []any{json.Number("1"), "2.5"}}}}},
		{ID: "trace-error", Source: "jaeger", Type: "trace", Facts: map[string]any{"error_service": "checkout"}},
		{ID: "trace-slow", Source: "jaeger", Type: "trace", Facts: map[string]any{"duration_micros": 1_000_000}},
		{ID: "kube", Source: "kubernetes", Type: "workload", Facts: map[string]any{"pods": []any{map[string]any{"ready": false, "restart_count": int32(2)}}, "deployment": map[string]any{"available_replicas": int64(0), "unavailable_replicas": float32(1)}, "endpoints": []any{}}},
	}
	for _, item := range items {
		analyzed := AnalyzeEvidence(item)
		if analyzed.AnomalyScore <= 0 || len(analyzed.CausalNodeIDs) < 1 {
			t.Fatalf("typed anomaly was not detected for %s: %+v", item.ID, analyzed)
		}
	}
	if values := numericLeaves(map[string]any{"time": 99, "value": []any{"3", 4}}); len(values) != 2 || maxAbs(values) != 4 {
		t.Fatalf("numeric observation leaves were not parsed: %v", values)
	}
	if !isEmptyCollection(nil) || !isEmptyCollection([]string{}) || isEmptyCollection("value") {
		t.Fatal("empty collection detection is inconsistent")
	}
}

func TestHealthyKubernetesFailureThresholdIsNotAnomaly(t *testing.T) {
	item := domain.Evidence{
		ID: "healthy-workload", Source: "kubernetes", Type: "workload_state",
		Facts: map[string]any{
			"deployment": map[string]any{
				"available_replicas": 1,
				"containers": []any{map[string]any{
					"liveness_probe": map[string]any{"failureThreshold": 3},
				}},
			},
			"pods":      []any{map[string]any{"ready": true, "restart_count": 0}},
			"endpoints": []any{map[string]any{"addresses": []any{map[string]any{"ip": "10.0.0.1"}}}},
		},
	}
	analyzed := AnalyzeEvidence(item)
	if analyzed.AnomalyScore != 0 {
		t.Fatalf("healthy Kubernetes configuration became anomalous: %+v", analyzed)
	}
	if len(analyzed.CausalNodeIDs) != 1 || analyzed.CausalNodeIDs[0] != "obs:healthy-workload" {
		t.Fatalf("non-graph signal leaked into causal node IDs: %+v", analyzed.CausalNodeIDs)
	}
}

func TestPrometheusSampleTimestampsAreNotMeasurements(t *testing.T) {
	timestamp := json.Number("1786051200")
	items := []struct {
		name string
		item domain.Evidence
		want float64
	}{
		{"zero-restarts", domain.Evidence{Source: "prometheus", Kind: "restarts", Facts: map[string]any{"result": []any{map[string]any{"value": []any{timestamp, "0"}}}}}, 0},
		{"available", domain.Evidence{Source: "prometheus", Kind: "deployment_availability", Facts: map[string]any{"result": []any{map[string]any{"value": []any{timestamp, "1"}}}}}, 0},
		{"throttled", domain.Evidence{Source: "prometheus", Kind: "cpu_throttling", Facts: map[string]any{"result": []any{map[string]any{"value": []any{timestamp, "0.40"}}}}}, 1},
		{"raw-memory", domain.Evidence{Source: "prometheus", Kind: "memory", Facts: map[string]any{"result": []any{map[string]any{"value": []any{timestamp, "536870912"}}}}}, 0},
	}
	for _, test := range items {
		t.Run(test.name, func(t *testing.T) {
			got := AnalyzeEvidence(test.item).AnomalyScore
			if got != test.want {
				t.Fatalf("anomaly=%v, want %v for %+v", got, test.want, test.item.Facts)
			}
		})
	}
}

func TestEmptyDirectionalNetworkPolicyIsAnomalous(t *testing.T) {
	item := domain.Evidence{
		ID: "policy", Source: "kubernetes", Kind: "workload_state",
		Facts: map[string]any{"network_policies": []any{map[string]any{
			"policy_types": []any{"Egress"}, "egress": nil,
		}}},
	}
	if got := AnalyzeEvidence(item).AnomalyScore; got != 1 {
		t.Fatalf("empty egress policy anomaly=%v, want 1", got)
	}
}

func TestRankingPolicyLoadValidatesWeights(t *testing.T) {
	dir := t.TempDir()
	valid := filepath.Join(dir, "valid.yaml")
	raw := "version: 1\nevidence:\n  temporal_alignment: 1\nincident:\n  normalized_rrf: 1\ntopology:\n  directed_edge_jaccard: 1\n"
	if err := os.WriteFile(valid, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	policy, err := LoadPolicy(valid)
	if err != nil || policy.Hash == "" {
		t.Fatalf("valid policy was not loaded: policy=%+v err=%v", policy, err)
	}
	invalid := filepath.Join(dir, "invalid.yaml")
	if err = os.WriteFile(invalid, []byte("version: 1\nevidence:\n  a: .4\nincident:\n  a: 1\ntopology:\n  a: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadPolicy(invalid); err == nil {
		t.Fatal("invalid policy weights were accepted")
	}
}
