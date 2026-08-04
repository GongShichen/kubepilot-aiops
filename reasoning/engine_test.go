package reasoning

import (
	"strings"
	"testing"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func TestWeightedRRFStableAndDeduplicated(t *testing.T) {
	e := New(DefaultConfig())
	c := func(id string) domain.RetrievalCandidate { return domain.RetrievalCandidate{IncidentID: id} }
	semanticB := c("b")
	semanticB.Features.TopologyServices = []string{"gateway"}
	lexicalB := c("b")
	lexicalB.Resource = "deployment/order"
	lexicalB.Features.CausalNodeIDs = []string{"connection_failure"}
	a := e.Fuse(CandidateLists{Semantic: []domain.RetrievalCandidate{c("a"), semanticB}, Lexical: []domain.RetrievalCandidate{lexicalB, c("a")}, Topology: []domain.RetrievalCandidate{c("b")}})
	if len(a) != 2 || a[0].IncidentID != "b" {
		t.Fatalf("unexpected fused ranking: %#v", a)
	}
	if a[0].SourceRanks["semantic"] != 2 || a[0].SourceRanks["lexical"] != 1 {
		t.Fatalf("rank breakdown missing: %#v", a[0])
	}
	if a[0].Resource != "deployment/order" || len(a[0].Features.CausalNodeIDs) != 1 {
		t.Fatalf("multi-retriever features were not deterministically merged: %#v", a[0])
	}
}

func TestAnnotateCausalNodesFromActivePatterns(t *testing.T) {
	e := New(DefaultConfig())
	items := e.AnnotateCausalNodes([]domain.Evidence{{ID: "e", Summary: "connection refused from downstream endpoint"}}, []domain.CausalPattern{{ID: "p", Status: "active", Nodes: []domain.CausalNode{{ID: "connection_failure", Match: []string{"connection refused"}}, {ID: "network_config", Match: []string{"endpoint"}}}}})
	if len(items) != 1 || strings.Join(items[0].CausalNodeIDs, ",") != "connection_failure,network_config" {
		t.Fatalf("causal node annotation=%#v", items)
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
		{ID: "e1", Source: "kubernetes", RelevanceScore: .9, CausalNodeIDs: []string{"network_config"}},
		{ID: "e2", Source: "loki", RelevanceScore: .8, CausalNodeIDs: []string{"connection_failure"}},
	}
	pattern := domain.CausalPattern{Category: "network", Status: "active", Nodes: []domain.CausalNode{{ID: "network_config", Match: []string{"selector"}}, {ID: "connection_failure", Match: []string{"connection refused"}}, {ID: "trace_gap", Match: []string{"missing span"}}}}
	complete := domain.HypothesisDraft{ID: "complete", Category: "network", PriorProbability: .9, SupportingEvidenceIDs: []string{"e1", "e2"}, ExpectedCausalPath: []string{"network_config", "connection_failure"}}
	conflicted := domain.HypothesisDraft{ID: "conflict", Category: "network", PriorProbability: .9, SupportingEvidenceIDs: []string{"e1", "e2"}, ContradictingEvidenceIDs: []string{"e1"}, ExpectedCausalPath: []string{"network_config", "connection_failure", "trace_gap"}}
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
