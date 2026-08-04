package validator

import (
	"context"
	"testing"
	"time"

	knowledge "github.com/kubepilot-aiops/kubepilot/internal/causal/knowledge"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func TestUnknownEvidenceCannotEnterCausalKnowledge(t *testing.T) {
	in := &domain.Incident{ID: "i", Namespace: "kubepilot-demo", Status: domain.StatusResolved, Evidence: []domain.Evidence{{ID: "real", Source: "prometheus", Type: "metric"}, {ID: "event", Source: "kubernetes", Type: "event"}}, UpdatedAt: time.Now()}
	proposal := knowledge.Proposal{IncidentID: "i", Pattern: knowledge.CausalPattern{Cause: "unknown", CausalGraph: knowledge.CausalGraph{Nodes: []knowledge.CausalNode{{ID: "c", Type: "cause", Name: "unknown"}, {ID: "s", Type: "symptom", Name: "failure"}}, Edges: []knowledge.CausalEdge{{Source: "c", Target: "s", Relation: "causes"}}}, SupportingEvidence: []knowledge.EvidencePattern{{Source: "prometheus", Type: "missing"}, {Source: "kubernetes", Type: "event"}}, Confidence: .9}}
	result, err := New(knowledge.NewMemoryStore()).Validate(context.Background(), in, proposal)
	if err != nil {
		t.Fatal(err)
	}
	if result.Valid {
		t.Fatalf("unsupported evidence proposal passed: %+v", result)
	}
}

func TestGroundedContradictionRejectsProposal(t *testing.T) {
	in := &domain.Incident{ID: "i", Namespace: "kubepilot-demo", Status: domain.StatusResolved, Evidence: []domain.Evidence{{ID: "m", Source: "prometheus", Type: "metric"}, {ID: "k", Source: "kubernetes", Type: "event"}}, UpdatedAt: time.Now()}
	proposal := knowledge.Proposal{IncidentID: "i", Pattern: knowledge.CausalPattern{Cause: "cause", CausalGraph: knowledge.CausalGraph{Nodes: []knowledge.CausalNode{{ID: "c", Type: "cause", Name: "cause"}, {ID: "s", Type: "symptom", Name: "symptom"}}, Edges: []knowledge.CausalEdge{{Source: "c", Target: "s", Relation: "causes"}}}, SupportingEvidence: []knowledge.EvidencePattern{{Source: "prometheus", Type: "metric"}, {Source: "kubernetes", Type: "event"}}, ContradictingEvidence: []knowledge.EvidencePattern{{Source: "prometheus", Type: "metric"}}, Confidence: .9}}
	result, err := New(knowledge.NewMemoryStore()).Validate(context.Background(), in, proposal)
	if err != nil {
		t.Fatal(err)
	}
	if result.Valid || result.Contradiction <= .1 {
		t.Fatalf("grounded contradiction was not rejected: %+v", result)
	}
}
