package evidence

import (
	"encoding/json"
	"math"
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

func TestTypedEmptyKubernetesEndpointSliceProducesAvailabilitySignal(t *testing.T) {
	// Client-go collectors retain typed slices before Evidence is serialized for
	// API/audit output. The parser must treat them the same as JSON []any so a
	// real Service without ready endpoints can enter the deterministic
	// dependency path.
	type endpointSubset struct{ Address string }
	analyzed := AnalyzeEvidence(domain.Evidence{
		ID: "typed-empty-endpoints", Source: "kubernetes", Type: "workload_state",
		Facts: map[string]any{"endpoints": []endpointSubset{}},
	})
	if analyzed.AnomalyScore != 1 {
		t.Fatalf("empty typed endpoints anomaly=%v, want 1", analyzed.AnomalyScore)
	}
	if !hasSignal(analyzed.Signals, "endpoint_unavailable") {
		t.Fatalf("endpoint-unavailable signal missing from %+v", analyzed.Signals)
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
		{"incidental-throttling", domain.Evidence{Source: "prometheus", Kind: "cpu_throttling", Facts: map[string]any{"result": []any{map[string]any{"value": []any{timestamp, "0.05"}}}}}, 0},
		{"sustained-quota-pressure", domain.Evidence{Source: "prometheus", Kind: "cpu_throttling", Facts: map[string]any{"result": []any{map[string]any{"value": []any{timestamp, "0.28"}}}}}, .9},
		{"raw-memory", domain.Evidence{Source: "prometheus", Kind: "memory", Facts: map[string]any{"result": []any{map[string]any{"value": []any{timestamp, "536870912"}}}}}, 0},
	}
	for _, test := range items {
		t.Run(test.name, func(t *testing.T) {
			got := AnalyzeEvidence(test.item).AnomalyScore
			if math.Abs(got-test.want) > 1e-9 {
				t.Fatalf("anomaly=%v, want %v for %+v", got, test.want, test.item.Facts)
			}
		})
	}
}

func TestMetricChangeKeepsOperationalDirectionAndIgnoresQueryMetadata(t *testing.T) {
	decreasingLatency := AnalyzeEvidence(domain.Evidence{
		Source: "prometheus", Kind: "p95_latency_change",
		Facts: map[string]any{"baseline": 1.0, "current": .1, "change_rate": -.9},
	})
	if decreasingLatency.AnomalyScore != 0 {
		t.Fatalf("latency recovery became an anomaly: %+v", decreasingLatency)
	}
	emptyThrottleQuery := AnalyzeEvidence(domain.Evidence{
		Source: "prometheus", Kind: "cpu_throttling_current",
		Facts: map[string]any{"query": "rate(container_cpu_cfs_throttled_seconds_total[5m])", "result": []any{}},
	})
	if emptyThrottleQuery.AnomalyScore != 0 {
		t.Fatalf("empty metric query became anomalous from its query text: %+v", emptyThrottleQuery)
	}
	lowRawCPUChange := AnalyzeEvidence(domain.Evidence{
		Source: "prometheus", Kind: "cpu_change",
		Facts: map[string]any{"baseline": .0001, "current": .001, "change_rate": 9},
	})
	if lowRawCPUChange.AnomalyScore != 0 {
		t.Fatalf("raw core-rate change became CPU pressure: %+v", lowRawCPUChange)
	}
	normalizedCPUChange := AnalyzeEvidence(domain.Evidence{
		Source: "prometheus", Kind: "cpu_change",
		Facts: map[string]any{"baseline": .2, "current": 1.0, "change_rate": 4, "normalization": "ratio_to_cpu_limit"},
	})
	if normalizedCPUChange.AnomalyScore != 1 {
		t.Fatalf("normalised CPU limit ratio was not recognised: %+v", normalizedCPUChange)
	}
	lowUtilizationMemoryTrend := AnalyzeEvidence(domain.Evidence{
		Source: "prometheus", Kind: "memory_trend",
		Facts: map[string]any{"result": []any{map[string]any{"values": []any{[]any{json.Number("1786051200"), "0.02"}, []any{json.Number("1786051215"), "0.08"}}}}},
	})
	if lowUtilizationMemoryTrend.AnomalyScore != 0 {
		t.Fatalf("low-utilisation memory trend became pressure: %+v", lowUtilizationMemoryTrend)
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

func TestExplicitNetworkPolicyEffectProducesNetworkSignal(t *testing.T) {
	item := domain.Evidence{
		ID: "policy-effect", Source: "kubernetes", Kind: "workload_state",
		Facts: map[string]any{"network_policy_effects": []any{map[string]any{
			"mode": "deny_all", "direction": "Egress", "selected_pods": []any{"checkout-1"},
		}}},
	}
	analyzed := AnalyzeEvidence(item)
	if analyzed.AnomalyScore != 1 {
		t.Fatalf("explicit policy effect anomaly=%v, want 1", analyzed.AnomalyScore)
	}
	if !hasSignal(analyzed.Signals, "network_policy_configured") || !hasSignal(analyzed.Signals, "network_policy_denial") {
		t.Fatalf("policy effect did not emit configuration and denial signals: %+v", analyzed.Signals)
	}
}

func TestKubernetesFailureAndControllerFactsProduceTypedSignals(t *testing.T) {
	analyzed := AnalyzeEvidence(domain.Evidence{
		ID: "kube-rollout", Source: "kubernetes", Kind: "workload_state",
		Facts: map[string]any{
			"pods": []map[string]any{{
				"ready": false,
				"container_statuses": []map[string]any{{
					"state": map[string]any{"waiting": map[string]any{"reason": "ErrImagePull"}},
				}},
			}},
			"events": []map[string]any{{"reason": "ScalingReplicaSet", "message": "Scaled up replica set"}},
		},
	})
	if !hasSignal(analyzed.Signals, "image_pull_failure") || !hasSignal(analyzed.Signals, "deployment_change") {
		t.Fatalf("nested Kubernetes facts lost typed failure signals: %+v", analyzed.Signals)
	}
}

func TestKubernetesLifecycleAndServiceConfigurationSignalsUseStructuredFacts(t *testing.T) {
	t.Run("last termination retains OOM mechanism", func(t *testing.T) {
		analyzed := AnalyzeEvidence(domain.Evidence{
			ID: "oom-after-restart", Source: "kubernetes", Kind: "workload_state",
			Facts: map[string]any{"pods": []map[string]any{{
				"ready": true, "restart_count": 1,
				"last_termination_state": map[string]any{"terminated": map[string]any{"reason": "OOMKilled", "exit_code": 137}},
			}}},
		})
		if analyzed.AnomalyScore == 0 || !hasSignal(analyzed.Signals, "oom_killed") {
			t.Fatalf("last termination did not become an OOM signal: %+v", analyzed)
		}
	})
	t.Run("selector mismatch requires endpoints pods and labels", func(t *testing.T) {
		facts := map[string]any{
			"endpoints": []any{},
			"service":   map[string]any{"selector": map[string]string{"app": "checkout"}},
			"pods":      []map[string]any{{"labels": map[string]string{"app": "orders"}}},
		}
		analyzed := AnalyzeEvidence(domain.Evidence{ID: "selector", Source: "kubernetes", Kind: "workload_state", Facts: facts})
		if !hasSignal(analyzed.Signals, "service_selector_mismatch") {
			t.Fatalf("selector mismatch was not projected: %+v", analyzed.Signals)
		}
		facts["pods"] = []map[string]any{{"labels": map[string]string{"app": "checkout"}}}
		if hasSignal(AnalyzeEvidence(domain.Evidence{ID: "matching-selector", Source: "kubernetes", Kind: "workload_state", Facts: facts}).Signals, "service_selector_mismatch") {
			t.Fatal("matching Service selector became a mismatch")
		}
	})
	t.Run("configuration and database endpoint classifications remain distinct", func(t *testing.T) {
		configuration := AnalyzeEvidence(domain.Evidence{ID: "configured-missing", Source: "kubernetes", Kind: "workload_state", Facts: map[string]any{
			"configured_endpoint_resolution": []map[string]any{{"host": "cache", "status": "service_not_found"}},
		}})
		if configuration.AnomalyScore == 0 || !hasSignal(configuration.Signals, "configured_endpoint_unresolvable") {
			t.Fatalf("structured endpoint resolution failure was not projected: %+v", configuration)
		}
		database := AnalyzeEvidence(domain.Evidence{ID: "database-endpoint", Source: "kubernetes", Kind: "workload_state", Facts: map[string]any{
			"endpoints": []any{}, "service": map[string]any{"ports": []map[string]any{{"name": "mysql", "port": 3306}}},
		}})
		if !hasSignal(database.Signals, "database_endpoint_unavailable") {
			t.Fatalf("database endpoint was not classed from declared protocol: %+v", database.Signals)
		}
	})
}

func TestImagePullTransportDetailDoesNotBecomeApplicationNetworkSignal(t *testing.T) {
	analyzed := AnalyzeEvidence(domain.Evidence{
		ID: "image-pull-transport", Source: "kubernetes", Kind: "event",
		Facts: map[string]any{"message": "Failed to pull image registry.example/app: dial tcp 10.0.0.9:443: i/o timeout"},
	})
	if !hasSignal(analyzed.Signals, "image_pull_failure") || hasSignal(analyzed.Signals, "connection_timeout") {
		t.Fatalf("image pull transport detail was projected as an application network failure: %+v", analyzed.Signals)
	}
}

func TestUnresolvableImageReferenceProducesSpecificWorkloadSignal(t *testing.T) {
	analyzed := AnalyzeEvidence(domain.Evidence{
		ID: "unresolvable-image", Source: "kubernetes", Kind: "workload_state",
		Facts: map[string]any{"message": "Failed to pull image: failed to resolve reference registry.example.invalid/app:tag"},
	})
	if !hasSignal(analyzed.Signals, "image_pull_failure") || !hasSignal(analyzed.Signals, "image_reference_unresolvable") {
		t.Fatalf("unresolvable image reference lost its specific typed signal: %+v", analyzed.Signals)
	}
}

func TestMemoryTrendProducesGrowthAndPressureSignals(t *testing.T) {
	analyzed := AnalyzeEvidence(domain.Evidence{
		ID: "memory-trend", Source: "prometheus", Kind: "memory_trend",
		Facts: map[string]any{"result": []any{map[string]any{"values": []any{
			[]any{json.Number("1786051200"), "0.62"}, []any{json.Number("1786051215"), "0.85"},
		}}}},
	})
	if analyzed.AnomalyScore == 0 || !hasSignal(analyzed.Signals, "memory_growth") || !hasSignal(analyzed.Signals, "memory_pressure") {
		t.Fatalf("memory trend did not retain independent growth and pressure facts: %+v", analyzed)
	}
}

func hasSignal(signals []domain.EvidenceSignal, want string) bool {
	for _, signal := range signals {
		if signal.Signal == want {
			return true
		}
	}
	return false
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
