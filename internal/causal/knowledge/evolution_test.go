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
