package discovery

import (
	"context"
	"strings"
	"testing"
	"time"

	causalknowledge "github.com/kubepilot-aiops/kubepilot/internal/causal/knowledge"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func resolvedIncident(id string, path []string) *domain.Incident {
	return &domain.Incident{
		ID:        id,
		Status:    domain.StatusResolved,
		Namespace: "kubepilot-demo",
		Service:   "payment-service",
		Evidence: []domain.Evidence{
			{ID: id + "-metric", Source: "prometheus", Type: "metric", Summary: "memory growth", Confidence: .9, Timestamp: time.Now()},
			{ID: id + "-event", Source: "kubernetes", Type: "kubernetes_event", Summary: "OOMKilled", Confidence: .95, Timestamp: time.Now()},
		},
		Verification:         &domain.Verification{Success: true, Checks: map[string]bool{"ready": true, "error_rate": true, "probe": true}},
		DiagnosisLedger:      &domain.DiagnosisLedger{SelectedHypothesisID: "h1", Verified: []domain.VerifiedHypothesis{{Draft: domain.HypothesisDraft{ID: "h1", Cause: path[0], ExpectedCausalPath: path, SupportingEvidenceIDs: []string{id + "-metric", id + "-event"}, PriorProbability: .8}, SupportingScore: .9, CausalPathCoverage: 1, FinalScore: .9, Status: domain.HypothesisSupported}}},
		RootCauseEvidenceIDs: []string{id + "-metric", id + "-event"},
		Confidence:           .9,
		CreatedAt:            time.Now().Add(-time.Minute),
		UpdatedAt:            time.Now(),
	}
}

func TestBuildIncidentCausalGraphFusesVerifiedEvidenceAndRecovery(t *testing.T) {
	incident := resolvedIncident("incident-1", []string{"memory_growth", "oom_killed", "pod_restart"})
	incident.Proposal = &domain.RecoveryProposal{Action: domain.ActionRestartPod, Confidence: .9}
	graph, err := BuildIncidentCausalGraph(incident)
	if err != nil {
		t.Fatal(err)
	}
	if graph.IncidentID != incident.ID || len(graph.Nodes) < 6 {
		t.Fatalf("unexpected graph: %#v", graph)
	}
	if !hasEdge(graph, "causes", "path:0:memory_growth", "path:1:oom_killed") {
		t.Fatalf("causal path edge missing: %#v", graph.Edges)
	}
	if !hasNodeType(graph, NodeMetric) || !hasNodeType(graph, NodeKubernetesEvent) || !hasNodeType(graph, NodeRecoveryResult) {
		t.Fatalf("evidence or recovery nodes missing: %#v", graph.Nodes)
	}
}

func TestBuildIncidentCausalGraphRejectsBenchmarkIncident(t *testing.T) {
	incident := resolvedIncident("benchmark-1", []string{"a", "b"})
	incident.Namespace = "kubepilot-benchmark"
	if _, err := BuildIncidentCausalGraph(incident); err == nil {
		t.Fatal("benchmark incident must not enter discovery")
	}
}

func TestPatternMinerFindsFrequentPath(t *testing.T) {
	graphs := []IncidentCausalGraph{
		testGraph("i1", []string{"a", "b", "c"}),
		testGraph("i2", []string{"a", "b", "c"}),
		testGraph("i3", []string{"a", "b", "c"}),
	}
	items := NewPatternMiner().Mine(graphs)
	for _, item := range items {
		if pathKey(item.CausalPath) == "a->b->c" {
			if item.Frequency != 3 || item.Coverage != 1 || len(item.Contradictions) != 0 {
				t.Fatalf("unexpected candidate: %#v", item)
			}
			return
		}
	}
	t.Fatal("frequent causal path was not discovered")
}

func TestPatternMinerPenalizesContradictoryGraph(t *testing.T) {
	graphs := []IncidentCausalGraph{
		testGraph("i1", []string{"a", "b", "c"}),
		testGraph("i2", []string{"a", "b", "c"}),
		testGraph("i3", []string{"a", "b", "c"}),
		testGraph("counter", []string{"a", "b"}),
	}
	items := NewPatternMiner().Mine(graphs)
	for _, item := range items {
		if pathKey(item.CausalPath) == "a->b->c" {
			if len(item.Contradictions) != 1 || item.Contradictions[0] != "counter" {
				t.Fatalf("contradiction was not recorded: %#v", item)
			}
			return
		}
	}
	t.Fatal("candidate was not discovered")
}

func TestDiscoveryEngineAcceptsThreeIndependentIncidents(t *testing.T) {
	incidents := []*domain.Incident{
		resolvedIncident("i1", []string{"a", "b", "c"}),
		resolvedIncident("i2", []string{"a", "b", "c"}),
		resolvedIncident("i3", []string{"a", "b", "c"}),
	}
	store := NewMemoryStore()
	items, err := NewEngine(store, nil).Discover(context.Background(), incidents)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range items {
		if pathKey(item.CausalPath) == "a->b->c" {
			found = true
			if item.Status != StatusAccepted || len(item.SupportingIncidents) != 3 {
				t.Fatalf("candidate did not pass discovery validation: %#v", item)
			}
		}
	}
	if !found {
		t.Fatal("expected accepted candidate")
	}
}

func TestDiscoveryEngineUsesExistingValidatorForNewPattern(t *testing.T) {
	incidents := []*domain.Incident{
		resolvedIncident("v1", []string{"a", "b", "c"}),
		resolvedIncident("v2", []string{"a", "b", "c"}),
		resolvedIncident("v3", []string{"a", "b", "c"}),
	}
	store := NewMemoryStore()
	patterns := causalknowledge.NewMemoryStore()
	engine := NewEngine(store, patterns)
	engine.Patterns = patterns
	items, err := engine.Discover(context.Background(), incidents)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if pathKey(item.CausalPath) == "a->b->c" && item.Status != StatusAccepted {
			t.Fatalf("new pattern did not pass existing structural validator: %#v", item)
		}
	}
	all, err := patterns.List(context.Background(), "", 10)
	if err != nil || len(all) == 0 {
		t.Fatalf("accepted candidate was not merged into causal knowledge: all=%#v err=%v", all, err)
	}
}

func TestExplanationProviderCannotPromoteCandidate(t *testing.T) {
	incidents := []*domain.Incident{
		resolvedIncident("e1", []string{"a", "b", "c"}),
		resolvedIncident("e2", []string{"a", "b", "c"}),
	}
	store := NewMemoryStore()
	engine := NewEngine(store, nil)
	engine.Explainer = ExplainFunc(func(context.Context, CausalPatternCandidate) (string, error) {
		return "model explanation", nil
	})
	items, err := engine.Discover(context.Background(), incidents)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Explanation != "model explanation" {
			t.Fatalf("explanation was not retained: %#v", item)
		}
		if item.Status == StatusAccepted {
			t.Fatal("explanation provider must not bypass minimum support validation")
		}
	}
}

func TestDiscoveryPublicHelpersAndCandidateStore(t *testing.T) {
	incidents := []*domain.Incident{
		resolvedIncident("helper-one", []string{"memory growth", "oom killed", "pod restart"}),
		resolvedIncident("helper-two", []string{"memory growth", "oom killed", "pod restart"}),
		resolvedIncident("helper-three", []string{"memory growth", "oom killed", "pod restart"}),
	}
	graphs := make([]IncidentCausalGraph, 0, len(incidents))
	graphByID := map[string]IncidentCausalGraph{}
	incidentByID := map[string]*domain.Incident{}
	for _, incident := range incidents {
		graph, err := Build(incident)
		if err != nil {
			t.Fatal(err)
		}
		graphs = append(graphs, graph)
		graphByID[incident.ID] = graph
		incidentByID[incident.ID] = incident
	}
	candidates := Mine(graphs)
	if len(candidates) == 0 || !ValidateCandidate(context.Background(), candidates[0], graphByID, incidentByID, nil) {
		t.Fatalf("public discovery helpers rejected grounded candidate: %+v", candidates)
	}
	if string(MarshalPath([]string{"memory growth", "oom killed"})) != `["memory growth","oom killed"]` {
		t.Fatal("causal path JSON is not stable")
	}
	store := NewMemoryStore()
	accepted := NormalizeCandidate(candidates[0])
	accepted.Status = StatusAccepted
	accepted.Confidence = .9
	if err := store.Upsert(context.Background(), accepted); err != nil {
		t.Fatal(err)
	}
	rejected := NormalizeCandidate(CausalPatternCandidate{CausalPath: []string{"network", "timeout"}, Status: StatusRejected, Confidence: .2})
	if err := store.Upsert(context.Background(), rejected); err != nil {
		t.Fatal(err)
	}
	listed, err := store.List(context.Background(), StatusAccepted, 1)
	if err != nil || len(listed) != 1 || listed[0].PatternID != accepted.PatternID {
		t.Fatalf("candidate list=%+v err=%v", listed, err)
	}
	found, err := store.Search(context.Background(), []string{"OOM"}, 1)
	if err != nil || len(found) != 1 || !strings.Contains(strings.Join(found[0].CausalPath, " "), "oom") {
		t.Fatalf("candidate search=%+v err=%v", found, err)
	}
	allAccepted, err := store.Search(context.Background(), nil, 1)
	if err != nil || len(allAccepted) != 1 {
		t.Fatalf("empty search=%+v err=%v", allAccepted, err)
	}
	var unavailable *MemoryStore
	if err := unavailable.Upsert(context.Background(), accepted); err == nil {
		t.Fatal("nil candidate store accepted a write")
	}
	if _, err := unavailable.List(context.Background(), "", 0); err == nil {
		t.Fatal("nil candidate store returned data")
	}
}

func TestCausalGraphBuildRejectsUnverifiedInputs(t *testing.T) {
	if _, err := Build(nil); err == nil {
		t.Fatal("nil incident entered causal learning")
	}
	base := resolvedIncident("invalid-input", []string{"cause", "symptom"})
	cases := []struct {
		name   string
		mutate func(*domain.Incident)
	}{
		{name: "unresolved", mutate: func(in *domain.Incident) { in.Status = domain.StatusDiagnosing }},
		{name: "verification", mutate: func(in *domain.Incident) { in.Verification = nil }},
		{name: "selection", mutate: func(in *domain.Incident) { in.DiagnosisLedger.SelectedHypothesisID = "missing" }},
		{name: "contradiction", mutate: func(in *domain.Incident) { in.DiagnosisLedger.Verified[0].ContradictionScore = .2 }},
		{name: "confidence", mutate: func(in *domain.Incident) { in.DiagnosisLedger.Verified[0].FinalScore = .7 }},
		{name: "path", mutate: func(in *domain.Incident) { in.DiagnosisLedger.Verified[0].Draft.ExpectedCausalPath = nil }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			copyIncident := *base
			ledger := *base.DiagnosisLedger
			ledger.Verified = append([]domain.VerifiedHypothesis(nil), base.DiagnosisLedger.Verified...)
			copyIncident.DiagnosisLedger = &ledger
			test.mutate(&copyIncident)
			if _, err := Build(&copyIncident); err == nil {
				t.Fatalf("%s input entered causal learning", test.name)
			}
		})
	}
}

func testGraph(id string, path []string) IncidentCausalGraph {
	graph := IncidentCausalGraph{IncidentID: id}
	for i, name := range path {
		nodeID := "n" + string(rune('0'+i))
		typ := NodeSymptom
		if i == 0 {
			typ = NodeCause
		}
		graph.Nodes = append(graph.Nodes, CausalNode{ID: nodeID, Type: typ, Name: name, Confidence: .9})
		if i > 0 {
			graph.Edges = append(graph.Edges, CausalEdge{From: "n" + string(rune('0'+i-1)), To: nodeID, Relation: "causes", Confidence: .9})
		}
	}
	graph.Nodes = append(graph.Nodes,
		CausalNode{ID: "metric", Type: NodeMetric, Name: "metric evidence", Confidence: .9},
		CausalNode{ID: "event", Type: NodeKubernetesEvent, Name: "event evidence", Confidence: .9})
	return graph
}

func hasNodeType(graph IncidentCausalGraph, typ NodeType) bool {
	for _, node := range graph.Nodes {
		if node.Type == typ {
			return true
		}
	}
	return false
}

func hasEdge(graph IncidentCausalGraph, relation, source, target string) bool {
	for _, edge := range graph.Edges {
		if edge.Relation == relation && edge.From == source && edge.To == target {
			return true
		}
	}
	return false
}
