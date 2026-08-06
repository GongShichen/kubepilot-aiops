package knowledge_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/causal/extractor"
	knowledge "github.com/kubepilot-aiops/kubepilot/internal/causal/knowledge"
	"github.com/kubepilot-aiops/kubepilot/internal/causal/validator"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func resolved(id string, namespace string) *domain.Incident {
	e1 := domain.Evidence{ID: "m", Source: "prometheus", Type: "memory_metric", Summary: "memory growth"}
	e2 := domain.Evidence{ID: "k", Source: "kubernetes", Type: "kubernetes_event", Summary: "OOMKilled pod restart"}
	draft := domain.HypothesisDraft{ID: "h", Category: "memory", Cause: "memory leak", Service: "payment-service", Resource: "payment-service", PriorProbability: .95, SupportingEvidenceIDs: []string{"m", "k"}, ExpectedCausalPath: []string{"memory_leak", "memory_growth", "oom_killed", "pod_restart"}}
	verified := domain.VerifiedHypothesis{Draft: draft, VerifiedEvidenceIDs: []string{"m", "k"}, FinalScore: .95, ContradictionScore: .02}
	return &domain.Incident{ID: id, Namespace: namespace, Status: domain.StatusResolved, Confidence: .95, Evidence: []domain.Evidence{e1, e2}, DiagnosisLedger: &domain.DiagnosisLedger{SelectedHypothesisID: "h", Verified: []domain.VerifiedHypothesis{verified}}, UpdatedAt: time.Now().UTC()}
}

func TestCausalProposalRequiresValidationBeforeMerge(t *testing.T) {
	store := knowledge.NewMemoryStore()
	v := validator.New(store)
	in := resolved("i1", "kubepilot-demo")
	proposal, ok := extractor.Propose(in)
	if !ok {
		t.Fatal("proposal was not extracted")
	}
	result, err := v.Validate(context.Background(), in, proposal)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Valid || result.Accepted {
		t.Fatalf("first observation should be valid but pending: %+v", result)
	}
	proposal.Pattern.Confidence = result.Confidence
	if _, err = store.Merge(context.Background(), proposal.Pattern); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.List(context.Background(), "active", 10); len(got) != 0 {
		t.Fatalf("pending pattern became active: %+v", got)
	}
}

func TestPatternIdentityExcludesIncidentSpecificEvidence(t *testing.T) {
	base := knowledge.CausalPattern{Cause: "memory leak", Cluster: "cluster-a", Namespace: "team-a", Nodes: []knowledge.CausalNode{{ID: "cause", Type: "cause", Name: "memory leak", Confidence: .9}, {ID: "observation", Type: "observation", Name: "memory growth", Source: "prometheus", SourceEvidenceIDs: []string{"incident-a-metric"}}}, Edges: []knowledge.CausalEdge{{From: "observation", To: "cause", Relation: "supports", Confidence: .9}}, SupportingEvidence: []knowledge.EvidencePattern{{Source: "prometheus", Type: "metric", Tokens: []string{"first", "wording"}}}, SourceIncidents: []string{"incident-a"}, Confidence: .9}
	variant := base
	variant.Nodes = append([]knowledge.CausalNode(nil), base.Nodes...)
	variant.Nodes[1].SourceEvidenceIDs = []string{"incident-b-metric"}
	variant.SupportingEvidence = []knowledge.EvidencePattern{{Source: "prometheus", Type: "metric", Tokens: []string{"different", "wording"}}}
	variant.SourceIncidents = []string{"incident-b"}
	if knowledge.PatternID(base) != knowledge.PatternID(variant) {
		t.Fatal("incident-specific evidence changed the normalized causal pattern identity")
	}
	if len(base.Nodes[1].SourceEvidenceIDs) != 1 || base.Nodes[1].SourceEvidenceIDs[0] != "incident-a-metric" || base.Nodes[0].Confidence != .9 {
		t.Fatalf("identity calculation mutated the audited source graph: %+v", base.Nodes)
	}
}

func TestCausalConfidenceEvolvesWithIndependentIncidents(t *testing.T) {
	store := knowledge.NewMemoryStore()
	v := validator.New(store)
	for i := 1; i <= 10; i++ {
		in := resolved("i"+strconv.Itoa(i), "kubepilot-demo")
		proposal, ok := extractor.Propose(in)
		if !ok {
			t.Fatal("proposal was not extracted")
		}
		result, err := v.Validate(context.Background(), in, proposal)
		if err != nil {
			t.Fatal(err)
		}
		proposal.Pattern.Confidence = result.Confidence
		if result.Accepted {
			proposal.Pattern.Status = "active"
		}
		if _, err = store.Merge(context.Background(), proposal.Pattern); err != nil {
			t.Fatal(err)
		}
	}
	items, err := store.List(context.Background(), "active", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Confidence < .8 {
		t.Fatalf("repeated incidents did not activate causal pattern: %+v", items)
	}
}

func TestCausalLifecycleActivatesAtThreeQualifiedIncidents(t *testing.T) {
	store := knowledge.NewMemoryStore()
	v := validator.New(store)
	expected := []string{"candidate", "validating", "active"}
	for index, status := range expected {
		in := resolved("lifecycle-"+strconv.Itoa(index+1), "kubepilot-demo")
		proposal, ok := extractor.Propose(in)
		if !ok {
			t.Fatal("proposal was not extracted")
		}
		result, err := v.Validate(context.Background(), in, proposal)
		if err != nil {
			t.Fatal(err)
		}
		proposal.Pattern.Confidence = result.Confidence
		merged, err := store.Merge(context.Background(), proposal.Pattern)
		if err != nil {
			t.Fatal(err)
		}
		if merged.Status != status || merged.SupportCount != index+1 || merged.Version != index+1 {
			t.Fatalf("support=%d status=%s, want %s: %+v", index+1, merged.Status, status, merged)
		}
	}
}

func TestRepeatedSameIncidentDoesNotIncreaseSupport(t *testing.T) {
	store := knowledge.NewMemoryStore()
	v := validator.New(store)
	in := resolved("same", "kubepilot-demo")
	proposal, ok := extractor.Propose(in)
	if !ok {
		t.Fatal("proposal was not extracted")
	}
	result, err := v.Validate(context.Background(), in, proposal)
	if err != nil {
		t.Fatal(err)
	}
	proposal.Pattern.Confidence = result.Confidence
	_, err = store.Merge(context.Background(), proposal.Pattern)
	if err != nil {
		t.Fatal(err)
	}
	result, err = v.Validate(context.Background(), in, proposal)
	if err != nil {
		t.Fatal(err)
	}
	if result.SupportCount != 1 || result.Accepted {
		t.Fatalf("same incident incorrectly increased support: %+v", result)
	}
}

func TestBenchmarkIncidentCannotEnterKnowledge(t *testing.T) {
	store := knowledge.NewMemoryStore()
	v := validator.New(store)
	proposal, ok := extractor.Propose(resolved("b1", "kubepilot-benchmark"))
	if !ok {
		t.Fatal("proposal extraction should be side-effect free")
	}
	result, err := v.Validate(context.Background(), resolved("b1", "kubepilot-benchmark"), proposal)
	if err != nil {
		t.Fatal(err)
	}
	if result.Valid {
		t.Fatalf("benchmark pattern passed validation: %+v", result)
	}
	if items, _ := store.List(context.Background(), "", 10); len(items) != 0 {
		t.Fatal("benchmark knowledge was persisted")
	}
}
