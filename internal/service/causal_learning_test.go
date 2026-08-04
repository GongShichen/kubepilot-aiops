package service

import (
	"context"
	"testing"
	"time"

	causaldiscovery "github.com/kubepilot-aiops/kubepilot/internal/causal/discovery"
	causalknowledge "github.com/kubepilot-aiops/kubepilot/internal/causal/knowledge"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	topologyknowledge "github.com/kubepilot-aiops/kubepilot/internal/topology/knowledge"
	"github.com/kubepilot-aiops/kubepilot/retrieval"
)

type learningStore struct {
	supports  map[string]map[string]bool
	patterns  map[string]domain.CausalPattern
	knowledge int
}

type learningEmbedder struct{}

func (learningEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{1}
	}
	return out, nil
}

type learningVectors struct{ docs []retrieval.Document }

type discoveryHistory struct{ incidents []*domain.Incident }

func (h discoveryHistory) ListResolvedIncidents(context.Context, []string, int) ([]*domain.Incident, error) {
	return h.incidents, nil
}

func (v *learningVectors) Upsert(_ context.Context, docs []retrieval.Document) error {
	v.docs = append(v.docs, docs...)
	return nil
}
func (*learningVectors) Search(context.Context, []float32, map[string]string, int) ([]retrieval.Document, error) {
	return nil, nil
}

func newLearningStore() *learningStore {
	return &learningStore{supports: map[string]map[string]bool{}, patterns: map[string]domain.CausalPattern{}}
}
func (s *learningStore) UpsertIncidentKnowledge(context.Context, *domain.Incident, domain.IncidentFeatures, string) error {
	s.knowledge++
	return nil
}
func (s *learningStore) SearchLexicalIncidents(context.Context, domain.IncidentFeatures, int) ([]domain.RetrievalCandidate, error) {
	return nil, nil
}
func (s *learningStore) SearchTopologyIncidents(context.Context, domain.IncidentFeatures, int) ([]domain.RetrievalCandidate, error) {
	return nil, nil
}
func (s *learningStore) SeedCausalPatterns(_ context.Context, items []domain.CausalPattern) error {
	for _, item := range items {
		if old, ok := s.patterns[item.ID]; ok {
			item.Status = old.Status
		}
		s.patterns[item.ID] = item
	}
	return nil
}
func (s *learningStore) ListCausalPatterns(context.Context, string) ([]domain.CausalPattern, error) {
	return nil, nil
}
func (s *learningStore) GetCausalPattern(_ context.Context, id string) (*domain.CausalPattern, error) {
	item := s.patterns[id]
	return &item, nil
}
func (s *learningStore) SetCausalPatternStatus(_ context.Context, id, status, operator string) (*domain.CausalPattern, error) {
	item := s.patterns[id]
	item.Status = status
	s.patterns[id] = item
	return &item, nil
}
func (s *learningStore) RecordCausalPatternEvent(_ context.Context, pattern, incident, event, reason string, _ map[string]any) error {
	if event == "incident_support" {
		if s.supports[pattern] == nil {
			s.supports[pattern] = map[string]bool{}
		}
		s.supports[pattern][incident] = true
	}
	return nil
}
func (s *learningStore) CountCausalPatternSupport(_ context.Context, id string) (int, error) {
	return len(s.supports[id]), nil
}

func eligibleLearnIncident(id, namespace string) *domain.Incident {
	e1 := domain.Evidence{ID: "e1", Source: "prometheus", Type: "metric", Summary: "timeout"}
	e2 := domain.Evidence{ID: "e2", Source: "kubernetes", Type: "workload", Summary: "endpoint missing"}
	draft := domain.HypothesisDraft{ID: "h1", Category: "network", Variant: "selector", Cause: "selector mismatch", Service: "order-service", Resource: "order-service", PriorProbability: .95, SupportingEvidenceIDs: []string{"e1", "e2"}, ExpectedCausalPath: []string{"selector", "endpoint", "timeout"}}
	verified := domain.VerifiedHypothesis{Draft: draft, ContradictionScore: .05, VerifiedEvidenceIDs: []string{"e1", "e2"}, FinalScore: .95}
	return &domain.Incident{ID: id, Namespace: namespace, Service: "order-service", Resource: "order-service", Status: domain.StatusResolved, Confidence: .95, RootCauseCategory: "network", RootCauseEvidenceIDs: []string{"e1", "e2"}, Evidence: []domain.Evidence{e1, e2}, Proposal: &domain.RecoveryProposal{ID: "p"}, ExecutionContext: &domain.ExecutionContext{ApprovalID: "a"}, Verification: &domain.Verification{Success: true, Checks: map[string]bool{"pod": true, "metric": true, "trace": true}, CompletedAt: time.Now()}, DiagnosisLedger: &domain.DiagnosisLedger{Drafts: []domain.HypothesisDraft{draft}, Verified: []domain.VerifiedHypothesis{verified}, SelectedHypothesisID: "h1"}}
}

func TestCausalLearningNeedsTwoIndependentIncidents(t *testing.T) {
	store := newLearningStore()
	vectors := &learningVectors{}
	learner := CausalLearner{Store: store, ConfidenceThreshold: .9, Namespaces: []string{"kubepilot-demo"}, Embedder: learningEmbedder{}, Vectors: vectors}
	if err := learner.Learn(context.Background(), eligibleLearnIncident("i1", "kubepilot-demo")); err != nil {
		t.Fatal(err)
	}
	for _, pattern := range store.patterns {
		if pattern.Status != "candidate" {
			t.Fatalf("activated with one incident: %#v", pattern)
		}
	}
	if err := learner.Learn(context.Background(), eligibleLearnIncident("i2", "kubepilot-demo")); err != nil {
		t.Fatal(err)
	}
	active := false
	for _, pattern := range store.patterns {
		active = active || pattern.Status == "active"
	}
	if !active {
		t.Fatal("pattern was not activated after two independent incidents")
	}
	if len(vectors.docs) != 2 || vectors.docs[0].Namespace != "kubepilot-demo" {
		t.Fatalf("resolved incidents were not added to semantic history: %#v", vectors.docs)
	}
}
func TestCausalLearningExcludesBenchmarkNamespace(t *testing.T) {
	store := newLearningStore()
	learner := CausalLearner{Store: store, Namespaces: []string{"kubepilot-demo", "kubepilot-benchmark"}}
	if err := learner.Learn(context.Background(), eligibleLearnIncident("i1", "kubepilot-benchmark")); err != nil {
		t.Fatal(err)
	}
	if store.knowledge != 0 || len(store.patterns) != 0 {
		t.Fatal("benchmark incident contaminated learning store")
	}
}

func TestCausalLearningRejectsInfrastructureFailures(t *testing.T) {
	store := newLearningStore()
	incident := eligibleLearnIncident("i1", "kubepilot-demo")
	incident.DiagnosisLedger.InfrastructureErrors = []string{"semantic retrieval unavailable"}
	learner := CausalLearner{Store: store, Namespaces: []string{"kubepilot-demo"}}
	if err := learner.Learn(context.Background(), incident); err != nil {
		t.Fatal(err)
	}
	if store.knowledge != 0 || len(store.patterns) != 0 {
		t.Fatal("incident with infrastructure failure entered learning")
	}
}

func TestKnowledgeEvolutionRunsOutsideAgentAndMergesResolvedIncident(t *testing.T) {
	store := newLearningStore()
	topologyStore := topologyknowledge.NewMemoryStore()
	causalStore := causalknowledge.NewMemoryStore()
	incident := eligibleLearnIncident("evolve-1", "kubepilot-demo")
	incident.Evidence[0].Content = map[string]any{"dependency": "mysql"}
	learner := CausalLearner{Store: store, Namespaces: []string{"kubepilot-demo"}, TopologyPatterns: topologyStore, CausalPatterns: causalStore}
	if err := learner.Learn(context.Background(), incident); err != nil {
		t.Fatal(err)
	}
	patterns, err := topologyStore.List(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(patterns) != 1 || patterns[0].Frequency != 1 {
		t.Fatalf("topology knowledge was not extracted: %+v", patterns)
	}
	causalPatterns, err := causalStore.List(context.Background(), "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(causalPatterns) != 1 || causalPatterns[0].Status == "active" {
		t.Fatalf("causal proposal bypassed repetition gate: %+v", causalPatterns)
	}
}

func TestCausalLearningTriggersDiscoveryOutsideAgent(t *testing.T) {
	store := newLearningStore()
	incidents := []*domain.Incident{eligibleLearnIncident("discover-1", "kubepilot-demo"), eligibleLearnIncident("discover-2", "kubepilot-demo"), eligibleLearnIncident("discover-3", "kubepilot-demo")}
	learner := CausalLearner{Store: store, Namespaces: []string{"kubepilot-demo"}, Discovery: causaldiscovery.NewEngine(causaldiscovery.NewMemoryStore(), nil), IncidentHistory: discoveryHistory{incidents: incidents}}
	if err := learner.Learn(context.Background(), incidents[0]); err != nil {
		t.Fatal(err)
	}
	items, err := learner.Discovery.Candidates.List(context.Background(), causaldiscovery.StatusAccepted, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 {
		t.Fatal("resolved incident learning did not trigger discovery")
	}
}
