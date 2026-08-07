package reasoning

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	evidencepolicy "github.com/kubepilot-aiops/kubepilot/internal/reasoning/evidence"
)

func TestCandidateGenerationUsesSemanticAndLexicalOnly(t *testing.T) {
	e := New(DefaultConfig())
	c := func(id string) domain.RetrievalCandidate { return domain.RetrievalCandidate{IncidentID: id} }
	semanticB := c("b")
	semanticB.Features.TopologyServices = []string{"gateway"}
	lexicalB := c("b")
	lexicalB.Resource = "deployment/order"
	lexicalB.Features.CausalNodeIDs = []string{"connection_failure"}
	a := e.Fuse(CandidateLists{Semantic: []domain.RetrievalCandidate{c("a"), semanticB}, Lexical: []domain.RetrievalCandidate{lexicalB, c("a")}, Topology: []domain.RetrievalCandidate{c("b")}})
	if len(a) != 2 || a[0].IncidentID != "a" {
		t.Fatalf("unexpected fused ranking: %#v", a)
	}
	if a[0].SourceRanks["semantic"] != 1 || a[0].SourceRanks["lexical"] != 2 {
		t.Fatalf("rank breakdown missing: %#v", a[0])
	}
	var merged domain.RetrievalCandidate
	for _, candidate := range a {
		if candidate.IncidentID == "b" {
			merged = candidate
		}
	}
	if merged.Resource != "deployment/order" || len(merged.Features.CausalNodeIDs) != 1 {
		t.Fatalf("multi-retriever features were not deterministically merged: %#v", merged)
	}
}

func TestAnnotateCausalNodesFromStructuredSignals(t *testing.T) {
	e := New(DefaultConfig())
	items := e.AnnotateCausalNodes([]domain.Evidence{{ID: "e", AnomalyScore: .9, Signals: []domain.EvidenceSignal{{Signal: "connection_refused", Direction: "abnormal"}, {Signal: "endpoint_unavailable", Direction: "abnormal"}}}}, []domain.CausalPattern{{ID: "p", Status: "active", Nodes: []domain.CausalNode{{ID: "connection_failure", Signals: []string{"connection_refused"}}, {ID: "network_config", Signals: []string{"endpoint_unavailable"}}}}})
	if len(items) != 1 || strings.Join(items[0].CausalNodeIDs, ",") != "connection_failure,network_config" {
		t.Fatalf("causal node annotation=%#v", items)
	}
}

func TestAnnotateCausalNodesAdmitsObservedTransitionOnlyAsCause(t *testing.T) {
	e := New(DefaultConfig())
	items := e.AnnotateCausalNodes([]domain.Evidence{{
		ID: "rollout", Source: "kubernetes", Signals: []domain.EvidenceSignal{{Signal: "deployment_change", Direction: "observed"}},
	}}, []domain.CausalPattern{{
		ID: "rollout", Status: "active", Nodes: []domain.CausalNode{
			{ID: "rollout_change", Type: "cause", Signals: []string{"deployment_change"}},
			{ID: "bad_mechanism", Type: "mechanism", Signals: []string{"deployment_change"}},
		},
	}})
	if len(items) != 1 || strings.Join(items[0].CausalNodeIDs, ",") != "rollout_change" {
		t.Fatalf("observed transition escaped cause-only boundary: %#v", items)
	}
}

func TestAnnotateCausalNodesDoesNotUseEvidenceText(t *testing.T) {
	e := New(DefaultConfig())
	items := e.AnnotateCausalNodes([]domain.Evidence{{
		ID: "policy", Source: "kubernetes", AnomalyScore: 1,
		Summary: "network policy blocks endpoint traffic", Facts: map[string]any{"network_policies": []any{map[string]any{"policy_types": []any{"Egress"}, "egress": nil}}},
	}}, []domain.CausalPattern{{ID: "network", Status: "active", Nodes: []domain.CausalNode{{ID: "network_config", Signals: []string{"network_policy_denial"}}}}})
	if len(items) != 1 || len(items[0].CausalNodeIDs) != 0 {
		t.Fatalf("causal annotation used prose or raw facts instead of a server signal: %#v", items)
	}
}

func TestMatchCausalPatternsRequiresObservedCanonicalNode(t *testing.T) {
	e := New(DefaultConfig())
	pattern := domain.CausalPattern{ID: "network", Status: "active", Nodes: []domain.CausalNode{{ID: "network_config", Type: "cause", Signals: []string{"network_policy_denial"}}}}
	evidence := []domain.Evidence{{ID: "e", AnomalyScore: 1, Summary: "network policy denial", CausalNodeIDs: []string{"obs:e"}}}
	if got := e.MatchCausalPatterns(domain.IncidentFeatures{Terms: []string{"networkpolicy"}}, evidence, []domain.CausalPattern{pattern}); len(got) != 0 {
		t.Fatalf("text-only evidence activated causal pattern: %#v", got)
	}
	evidence[0].CausalNodeIDs = append(evidence[0].CausalNodeIDs, "network_config")
	if got := e.MatchCausalPatterns(domain.IncidentFeatures{}, evidence, []domain.CausalPattern{pattern}); len(got) != 1 || got[0].ID != "network" {
		t.Fatalf("observed canonical node did not activate pattern: %#v", got)
	}
}

func TestEvidenceRankingLimitsAndRequiredSources(t *testing.T) {
	now := time.Now().UTC()
	in := &domain.Incident{ID: "i", Namespace: "n", Service: "s", Resource: "d", EvidenceStartAt: now.Add(-time.Minute)}
	items := []domain.Evidence{{ID: "k", Source: "kubernetes", Summary: "pod ready state"}, {ID: "l", Source: "loki", Summary: "OOMKilled " + strings.Repeat("界", 20000)}}
	for i := 0; i < 20; i++ {
		items = append(items, domain.Evidence{ID: string(rune('a'+i)) + "m", Source: "prometheus", Summary: "latency timeout"})
	}
	got, err := New(Config{ModelEvidenceMaxItems: 12, ModelContextMaxBytes: 32768}).RankEvidence(in, items)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Evidence) > 12 || got.Ledger.EvidenceRetainedBytes > 32768 {
		t.Fatalf("limits violated: %#v", got.Ledger)
	}
	if !hasSource(got.Evidence, "kubernetes") || !hasAnySource(got.Evidence, "loki", "prometheus") {
		t.Fatalf("required sources missing: %#v", got.Evidence)
	}
}

func TestEvidenceRankingKeepsRequiredSourcesWhenKubernetesFactsAreOversized(t *testing.T) {
	now := time.Now().UTC()
	incident := &domain.Incident{ID: "incident", Namespace: "namespace", Service: "service", Resource: "deployment", EvidenceStartAt: now.Add(-time.Minute)}
	items := []domain.Evidence{
		{
			ID: "kubernetes", Source: "kubernetes", Type: "workload_state", Summary: "workload state",
			Facts: map[string]any{
				"deployment": map[string]any{"available_replicas": 1, "unavailable_replicas": 1},
				"events":     strings.Repeat("workload event payload ", 10_000),
			},
		},
	}
	for index := 0; index < 14; index++ {
		items = append(items, domain.Evidence{
			ID: fmt.Sprintf("metric-%02d", index), Source: "prometheus", Type: "latency_metric", Summary: "latency observation",
			Facts: map[string]any{"result": strings.Repeat("metric sample ", 4_000), "current": float64(index + 1)},
		})
	}

	ranked, err := New(Config{ModelEvidenceMaxItems: 12, ModelContextMaxBytes: 32768}).RankEvidence(incident, items)
	if err != nil {
		t.Fatal(err)
	}
	if !hasSource(ranked.Evidence, "kubernetes") || !hasAnySource(ranked.Evidence, "prometheus", "metric", "loki", "log", "jaeger", "trace") {
		t.Fatalf("required independently collected sources were discarded: %#v", ranked.Evidence)
	}
	var kube domain.Evidence
	for _, item := range ranked.Evidence {
		if item.ID == "kubernetes" {
			kube = item
			break
		}
	}
	if len(kube.Facts) == 0 {
		t.Fatalf("oversized Kubernetes facts were not retained canonically: %#v", kube)
	}
	if len(ranked.RuntimeEvidence) != len(items) {
		t.Fatalf("deterministic runtime lost observations to the model context budget: got=%d want=%d", len(ranked.RuntimeEvidence), len(items))
	}
	if ranked.Ledger.EvidenceRetainedBytes > 32768 {
		t.Fatalf("context limit was exceeded: %#v", ranked.Ledger)
	}
}

func TestEvidenceRankingPreservesCanonicalNestedFactsAcrossPasses(t *testing.T) {
	now := time.Now().UTC()
	incident := &domain.Incident{ID: "incident", Namespace: "namespace", Service: "service", Resource: "deployment", EvidenceStartAt: now.Add(-time.Minute)}
	items := []domain.Evidence{
		{
			ID: "kube", Source: "kubernetes", Type: "workload_state", Timestamp: now,
			Facts: map[string]any{
				"pods": []map[string]any{{
					"ready": false,
					"container_statuses": []map[string]any{{
						"state": map[string]any{"waiting": map[string]any{"reason": "ErrImagePull"}},
					}},
				}},
				"events": []map[string]any{{"reason": "ScalingReplicaSet", "message": "Scaled up replica set"}},
			},
		},
		{ID: "metric", Source: "prometheus", Type: "latency", Timestamp: now, Facts: map[string]any{"current": 2.0, "baseline": 1.0}},
	}
	engine := New(Config{ModelEvidenceMaxItems: 12, ModelContextMaxBytes: 512})
	first, err := engine.RankEvidence(incident, items)
	if err != nil {
		t.Fatal(err)
	}
	second, err := engine.RankEvidence(incident, first.RuntimeEvidence)
	if err != nil {
		t.Fatal(err)
	}
	var kube domain.Evidence
	for _, item := range second.Evidence {
		if item.ID == "kube" {
			kube = item
			break
		}
	}
	if kube.Facts == nil {
		t.Fatalf("canonical Kubernetes facts were discarded on second rank: %#v", second.Evidence)
	}
	signals := evidencepolicy.AnalyzeEvidence(kube).Signals
	if !hasEvidenceSignal(signals, "image_pull_failure") || !hasEvidenceSignal(signals, "deployment_change") {
		t.Fatalf("second ranking pass lost nested Kubernetes signals: %#v", signals)
	}
}

func hasEvidenceSignal(signals []domain.EvidenceSignal, want string) bool {
	for _, signal := range signals {
		if signal.Signal == want {
			return true
		}
	}
	return false
}

func TestEvidenceRankingUsesObservedMetricResultAndCollapsesDynamicTemplates(t *testing.T) {
	now := time.Now().UTC()
	incident := &domain.Incident{ID: "i", Namespace: "n", Service: "s", EvidenceStartAt: now.Add(-time.Minute)}
	items := []domain.Evidence{
		{ID: "k", Source: "kubernetes", Summary: "deployment is available"},
		{ID: "healthy-throttling", Source: "prometheus", Kind: "cpu_throttling", Summary: "Prometheus CPU throttling query result", Data: map[string]any{"query": "rate(container_cpu_cfs_throttled_seconds_total[5m])", "result": []any{}}},
		{ID: "info-1", Source: "loki", Type: "indexed_log_template", Summary: `{"level":"INFO","path":"/metrics","time":"2026-08-04T01:02:03Z"}`, Content: map[string]any{"level": "INFO", "occurrence_count": 400}},
		{ID: "info-2", Source: "loki", Type: "indexed_log_template", Summary: `{"level":"INFO","path":"/metrics","time":"2026-08-04T01:02:09Z"}`, Content: map[string]any{"level": "INFO", "occurrence_count": 401}},
		{ID: "error", Source: "loki", Type: "log_entry", Summary: `{"level":"ERROR","message":"connection refused"}`, Content: map[string]any{"level": "ERROR"}},
	}
	got, err := New(Config{ModelEvidenceMaxItems: 12, ModelContextMaxBytes: 32768}).RankEvidence(incident, items)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]domain.Evidence{}
	indexed := 0
	for _, item := range got.Evidence {
		byID[item.ID] = item
		if item.Type == "indexed_log_template" {
			indexed++
		}
	}
	if indexed != 1 {
		t.Fatalf("dynamic INFO templates were not collapsed: %#v", got.Evidence)
	}
	if byID["healthy-throttling"].RelevanceScore >= byID["error"].RelevanceScore {
		t.Fatalf("healthy metric query name outranked observed error: healthy=%f error=%f", byID["healthy-throttling"].RelevanceScore, byID["error"].RelevanceScore)
	}
	metric := byID["healthy-throttling"]
	if metric.Data != nil {
		t.Fatalf("duplicated compatibility payload was retained: %#v", metric.Data)
	}
	if _, exists := metric.Content["query"]; exists {
		t.Fatalf("server-generated query leaked into model context: %#v", metric.Content)
	}
}

func TestEvidenceRankingPreservesIndependentTelemetryAndMetricFamilies(t *testing.T) {
	items := []domain.Evidence{
		{ID: "topology", Source: "kubernetes", RelevanceScore: .99, Summary: "workload topology"},
		{ID: "cpu", Source: "prometheus", Kind: "cpu", RelevanceScore: .98, Summary: "cpu observation"},
		{ID: "cpu-current", Source: "prometheus", Kind: "cpu_current", RelevanceScore: .97, Summary: "current cpu observation"},
		{ID: "memory", Source: "prometheus", Kind: "memory", RelevanceScore: .96, Summary: "memory observation"},
		{ID: "errors", Source: "prometheus", Kind: "error_rate", RelevanceScore: .95, Summary: "error observation"},
		{ID: "throughput", Source: "prometheus", Kind: "throughput", RelevanceScore: .94, Summary: "throughput observation"},
		{ID: "latency", Source: "prometheus", Kind: "latency", RelevanceScore: .93, Summary: "latency observation"},
		{ID: "logs", Source: "loki", RelevanceScore: .70, Summary: "request failure log"},
		{ID: "trace", Source: "jaeger", RelevanceScore: .60, Summary: "trace dependency observation"},
	}
	got := preserveRequiredSources(items, 8)
	byID := map[string]domain.Evidence{}
	for _, item := range got {
		byID[item.ID] = item
	}
	for _, id := range []string{"topology", "logs", "trace", "throughput"} {
		if _, ok := byID[id]; !ok {
			t.Fatalf("independent observation %q was omitted: %#v", id, got)
		}
	}
	if _, ok := byID["cpu-current"]; ok {
		t.Fatalf("duplicate current-window metric displaced another signal family: %#v", got)
	}
}

func TestHypothesisUnknownEvidenceRejected(t *testing.T) {
	e := New(DefaultConfig())
	_, err := e.VerifyHypotheses([]domain.HypothesisDraft{{ID: "h", PriorProbability: .9, SupportingEvidenceIDs: []string{"missing"}, ExpectedCausalPath: []string{"cause"}}}, []domain.Evidence{{ID: "real"}}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "unknown or expired") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHypothesisVerificationCausalPathAndContradiction(t *testing.T) {
	engine := New(DefaultConfig())
	evidence := []domain.Evidence{
		{ID: "e1", Source: "kubernetes", RelevanceScore: .9, AnomalyScore: .9, CausalNodeIDs: []string{"network_config"}},
		{ID: "e2", Source: "loki", RelevanceScore: .8, AnomalyScore: .8, CausalNodeIDs: []string{"connection_failure"}},
	}
	pattern := domain.CausalPattern{Category: "network", Status: "active", Nodes: []domain.CausalNode{{ID: "network_config", Match: []string{"selector"}}, {ID: "connection_failure", Match: []string{"connection refused"}}, {ID: "trace_gap", Match: []string{"missing span"}}}, Edges: []domain.CausalEdge{{From: "network_config", To: "connection_failure"}, {From: "connection_failure", To: "trace_gap"}}}
	complete := domain.HypothesisDraft{ID: "complete", Category: "network", PriorProbability: .9, SupportingEvidenceIDs: []string{"e1", "e2"}, ExpectedCausalNodeIDs: []string{"network_config", "connection_failure"}}
	conflicted := domain.HypothesisDraft{ID: "conflict", Category: "network", PriorProbability: .9, SupportingEvidenceIDs: []string{"e1", "e2"}, ContradictingEvidenceIDs: []string{"e1"}, ExpectedCausalNodeIDs: []string{"network_config", "connection_failure", "trace_gap"}}
	verified, err := engine.VerifyHypotheses([]domain.HypothesisDraft{conflicted, complete}, evidence, nil, []domain.CausalPattern{pattern})
	if err != nil {
		t.Fatal(err)
	}
	if verified[0].Draft.ID != "complete" || verified[0].CausalPathCoverage != 1 || len(verified[0].MissingCausalNodes) != 0 {
		t.Fatalf("complete causal path was not ranked first: %#v", verified)
	}
	if verified[1].ContradictionScore == 0 || len(verified[1].MissingCausalNodes) != 1 {
		t.Fatalf("missing/conflicting causal evidence was not recorded: %#v", verified[1])
	}
}

func TestHypothesisVerificationUsesCausalSignalsAndIndependentSourceAggregation(t *testing.T) {
	engine := New(DefaultConfig())
	pattern := domain.CausalPattern{
		ID: "cpu", Category: "cpu", Status: "active",
		Nodes: []domain.CausalNode{
			{ID: "cpu_saturation", Type: "mechanism", Signals: []string{"cpu_pressure"}},
			{ID: "latency_error", Type: "symptom", Signals: []string{"trace_latency"}},
		},
		Edges: []domain.CausalEdge{{From: "cpu_saturation", To: "latency_error"}},
	}
	evidence := []domain.Evidence{
		{
			ID: "metric", Source: "prometheus", Namespace: "ns", Service: "checkout", Resource: "checkout", QualityScore: .99,
			Signals: []domain.EvidenceSignal{
				{ID: "cpu", Signal: "cpu_pressure", Direction: "abnormal", Strength: .8, Reliability: 1, Independence: 1, TemporalAlignment: 1},
				{ID: "unrelated", Signal: "request_rate", Direction: "abnormal", Strength: 1, Reliability: 1, Independence: 1, TemporalAlignment: 1},
			},
		},
		{ID: "trace", Source: "jaeger", Namespace: "ns", Service: "checkout", Resource: "checkout", QualityScore: .99, Signals: []domain.EvidenceSignal{{ID: "latency", Signal: "trace_latency", Direction: "abnormal", Strength: .4, Reliability: 1, Independence: 1, TemporalAlignment: 1}}},
		{ID: "kube", Source: "kubernetes", Namespace: "ns", Service: "checkout", Resource: "checkout", QualityScore: .99, Signals: []domain.EvidenceSignal{{ID: "scope", Signal: "workload_health", Direction: "normal"}}},
	}
	draft := domain.HypothesisDraft{
		ID: "cpu", Category: "cpu", Service: "checkout", Resource: "checkout",
		SupportingEvidenceIDs: []string{"metric", "trace"}, ExpectedCausalNodeIDs: []string{"cpu_saturation", "latency_error"},
	}
	verified, err := engine.VerifyHypotheses([]domain.HypothesisDraft{draft}, evidence, nil, []domain.CausalPattern{pattern})
	if err != nil {
		t.Fatal(err)
	}
	if len(verified) != 1 || verified[0].SupportingScore < .87 || verified[0].SupportingScore > .89 {
		t.Fatalf("support must use only causal signal quality and combine independent sources without dilution: %+v", verified)
	}
}

func TestDeterministicCandidateRequiresObservedMechanism(t *testing.T) {
	engine := New(DefaultConfig())
	pattern := domain.CausalPattern{
		ID: "resource", Category: "resource", Status: "active",
		Nodes: []domain.CausalNode{
			{ID: "demand", Type: "cause", Signals: []string{"request_rate"}},
			{ID: "saturation", Type: "mechanism", Signals: []string{"cpu_pressure"}},
			{ID: "failure", Type: "symptom", Signals: []string{"error_rate"}},
		},
		Edges: []domain.CausalEdge{{From: "demand", To: "saturation"}, {From: "saturation", To: "failure"}},
	}
	draft := domain.HypothesisDraft{
		ID: "resource", Category: "resource", RequireCausalMechanism: true,
		SupportingEvidenceIDs: []string{"demand", "symptom"},
		ExpectedCausalNodeIDs: []string{"demand", "saturation", "failure"},
	}
	evidence := []domain.Evidence{
		{ID: "demand", Source: "prometheus", AnomalyScore: 1, CausalNodeIDs: []string{"demand"}},
		{ID: "symptom", Source: "loki", AnomalyScore: 1, CausalNodeIDs: []string{"failure"}},
	}
	verified, err := engine.VerifyHypotheses([]domain.HypothesisDraft{draft}, evidence, nil, []domain.CausalPattern{pattern})
	if err != nil {
		t.Fatal(err)
	}
	if verified[0].CausalPathCoverage != 0 || !containsString(verified[0].MissingCausalNodes, "saturation") {
		t.Fatalf("candidate without an observed mechanism passed causal coverage: %+v", verified[0])
	}

	evidence = append(evidence, domain.Evidence{ID: "mechanism", Source: "prometheus", AnomalyScore: 1, CausalNodeIDs: []string{"saturation"}})
	draft.SupportingEvidenceIDs = append(draft.SupportingEvidenceIDs, "mechanism")
	verified, err = engine.VerifyHypotheses([]domain.HypothesisDraft{draft}, evidence, nil, []domain.CausalPattern{pattern})
	if err != nil {
		t.Fatal(err)
	}
	if verified[0].CausalPathCoverage != 1 || len(verified[0].MissingCausalNodes) != 0 {
		t.Fatalf("observed mechanism did not restore causal coverage: %+v", verified[0])
	}
}

func TestHypothesisVerificationCreditsCurrentKubernetesTopology(t *testing.T) {
	engine := New(DefaultConfig())
	evidence := []domain.Evidence{
		{ID: "kube", Source: "kubernetes", Service: "payment-service", Resource: "payment-pod", RelevanceScore: .9, Summary: "payment pod identity", CausalNodeIDs: []string{"obs:kube"}},
		{ID: "metric", Source: "prometheus", Service: "payment-service", Resource: "payment-pod", RelevanceScore: .9, AnomalyScore: 1, Summary: "payment pod memory growth", CausalNodeIDs: []string{"obs:metric"}},
	}
	draft := domain.HypothesisDraft{ID: "memory", Category: "memory", Service: "payment-service", Resource: "payment-pod", PriorProbability: 1, SupportingEvidenceIDs: []string{"metric"}, ExpectedCausalNodeIDs: []string{"obs:metric"}}
	verified, err := engine.VerifyHypotheses([]domain.HypothesisDraft{draft}, evidence, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(verified) != 1 || verified[0].TopologyRelevance != 1 || verified[0].FinalScore < .80 || !containsString(verified[0].VerifiedEvidenceIDs, "kube") {
		t.Fatalf("current topology evidence was not credited: %+v", verified)
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func TestHypothesisCausalCoverageRejectsUnknownServerNode(t *testing.T) {
	engine := New(DefaultConfig())
	evidence := []domain.Evidence{{ID: "metric", Source: "prometheus", AnomalyScore: 1, CausalNodeIDs: []string{"obs:metric"}}}
	draft := domain.HypothesisDraft{ID: "unknown-node", SupportingEvidenceIDs: []string{"metric"}, ExpectedCausalNodeIDs: []string{"model-invented-node"}}
	if _, err := engine.VerifyHypotheses([]domain.HypothesisDraft{draft}, evidence, nil, nil); err == nil || !strings.Contains(err.Error(), "unknown node ID") {
		t.Fatalf("unknown causal node was accepted: %v", err)
	}
}

func TestLoadPatternSeed(t *testing.T) {
	seed, err := LoadPatternSeed("../knowledge/causal_patterns.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(seed.Patterns) < 5 {
		t.Fatalf("expected seed patterns, got %d", len(seed.Patterns))
	}
}

func TestBuildIncidentDependencyGraphUsesObservedCallsAndSharedDatastore(t *testing.T) {
	incident := &domain.Incident{Service: "payment-service", Namespace: "kubepilot-demo"}
	evidence := []domain.Evidence{{Source: "jaeger", Service: "payment-service", Summary: "payment-service -> mysql timeout", Content: map[string]any{"latency_ms": 120.0, "error_rate": .8}}, {Source: "kubernetes", Summary: "endpoint for order-service", Content: map[string]any{"dependency": "mysql"}}}
	features := New(DefaultConfig()).BuildFeatures(incident, evidence)
	if features.TopologyGraph.RootService != "payment-service" || len(features.TopologyGraph.Edges) == 0 {
		t.Fatalf("observed dependency graph was not built: %+v", features.TopologyGraph)
	}
	foundMySQL := false
	for _, node := range features.TopologyGraph.Nodes {
		if node.ID == "mysql" && node.Role == "critical_dependency" {
			foundMySQL = true
		}
	}
	if !foundMySQL {
		t.Fatalf("shared datastore was not represented as critical dependency: %+v", features.TopologyGraph.Nodes)
	}
}

func TestTopologyRerankUsesDependencyGraphAcrossServiceNames(t *testing.T) {
	engine := New(DefaultConfig())
	features := domain.IncidentFeatures{
		Namespace:        "kubepilot-demo",
		Service:          "payment-service",
		TopologyServices: []string{"payment-service", "mysql"},
		TopologyGraph: domain.IncidentDependencyGraph{
			RootService:           "payment-service",
			Nodes:                 []domain.DependencyNode{{ID: "payment-service", Role: "root"}, {ID: "mysql", Role: "critical_dependency"}},
			Edges:                 []domain.DependencyEdge{{From: "payment-service", To: "mysql", Kind: "observed_call"}},
			SuspectedFailureNodes: []string{"mysql"},
			ErrorPropagationPaths: [][]string{{"payment-service", "mysql"}},
		},
	}
	candidates := []domain.RetrievalCandidate{
		{
			IncidentID: "shared-database",
			Namespace:  "kubepilot-demo",
			Service:    "order-service",
			Features: domain.IncidentFeatures{
				TopologyServices: []string{"order-service", "mysql"},
				TopologyGraph: domain.IncidentDependencyGraph{
					RootService:           "order-service",
					Nodes:                 []domain.DependencyNode{{ID: "order-service", Role: "root"}, {ID: "mysql", Role: "critical_dependency"}},
					Edges:                 []domain.DependencyEdge{{From: "order-service", To: "mysql", Kind: "observed_call"}},
					SuspectedFailureNodes: []string{"mysql"},
					ErrorPropagationPaths: [][]string{{"order-service", "mysql"}},
				},
			},
		},
		{
			IncidentID: "text-only",
			Namespace:  "kubepilot-demo",
			Service:    "unrelated-service",
			Features: domain.IncidentFeatures{
				TopologyServices: []string{"unrelated-service"},
				TopologyGraph: domain.IncidentDependencyGraph{
					RootService: "unrelated-service",
					Nodes:       []domain.DependencyNode{{ID: "unrelated-service", Role: "root"}},
				},
			},
		},
	}
	ranked := engine.Rerank(features, candidates)
	if len(ranked) != 2 || ranked[0].IncidentID != "shared-database" {
		t.Fatalf("shared dependency graph did not outrank unrelated topology: %+v", ranked)
	}
	if ranked[0].Rank.TopologySimilarity <= ranked[1].Rank.TopologySimilarity {
		t.Fatalf("graph similarity was not used for topology score: %+v", ranked)
	}
}
