package validator

import (
	"context"
	"errors"
	"testing"
	"time"

	knowledge "github.com/kubepilot-aiops/kubepilot/internal/causal/knowledge"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

type validatorReader struct {
	patterns []knowledge.CausalPattern
	err      error
}

func (reader validatorReader) List(context.Context, string, int) ([]knowledge.CausalPattern, error) {
	return reader.patterns, reader.err
}

func groundedFixture() (*domain.Incident, knowledge.Proposal) {
	incident := &domain.Incident{ID: "qualified", Namespace: "kubepilot-demo", Status: domain.StatusResolved, Evidence: []domain.Evidence{{ID: "metric", Source: "prometheus", Type: "metric"}, {ID: "event", Source: "kubernetes", Type: "event"}}}
	pattern := knowledge.CausalPattern{
		Cause: "memory leak", Confidence: .9,
		Nodes:              []knowledge.CausalNode{{ID: "cause", Type: "cause", Name: "memory leak"}, {ID: "symptom", Type: "symptom", Name: "timeout"}, {ID: "observation", Type: "observation", Name: "metric", SourceEvidenceIDs: []string{"metric"}}},
		Edges:              []knowledge.CausalEdge{{From: "cause", To: "symptom", Relation: "manifests_as"}, {From: "observation", To: "cause", Relation: "supports"}},
		SupportingEvidence: []knowledge.EvidencePattern{{Source: "prometheus", Type: "metric"}, {Source: "kubernetes", Type: "event"}},
		SourceIncidents:    []string{incident.ID},
	}
	pattern = knowledge.Canonicalize(pattern)
	return incident, knowledge.Proposal{IncidentID: incident.ID, Pattern: pattern}
}

func TestUnknownEvidenceCannotEnterCausalKnowledge(t *testing.T) {
	in := &domain.Incident{ID: "i", Namespace: "kubepilot-demo", Status: domain.StatusResolved, Evidence: []domain.Evidence{{ID: "real", Source: "prometheus", Type: "metric"}, {ID: "event", Source: "kubernetes", Type: "event"}}, UpdatedAt: time.Now()}
	proposal := knowledge.Proposal{IncidentID: "i", Pattern: knowledge.CausalPattern{Cause: "unknown", Nodes: []knowledge.CausalNode{{ID: "c", Type: "cause", Name: "unknown"}, {ID: "s", Type: "symptom", Name: "failure"}}, Edges: []knowledge.CausalEdge{{From: "c", To: "s", Relation: "causes"}}, SupportingEvidence: []knowledge.EvidencePattern{{Source: "prometheus", Type: "missing"}, {Source: "kubernetes", Type: "event"}}, Confidence: .9}}
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
	proposal := knowledge.Proposal{IncidentID: "i", Pattern: knowledge.CausalPattern{Cause: "cause", Nodes: []knowledge.CausalNode{{ID: "c", Type: "cause", Name: "cause"}, {ID: "s", Type: "symptom", Name: "symptom"}}, Edges: []knowledge.CausalEdge{{From: "c", To: "s", Relation: "causes"}}, SupportingEvidence: []knowledge.EvidencePattern{{Source: "prometheus", Type: "metric"}, {Source: "kubernetes", Type: "event"}}, ContradictingEvidence: []knowledge.EvidencePattern{{Source: "prometheus", Type: "metric"}}, Confidence: .9}}
	result, err := New(knowledge.NewMemoryStore()).Validate(context.Background(), in, proposal)
	if err != nil {
		t.Fatal(err)
	}
	if result.Valid || result.Contradiction <= .1 {
		t.Fatalf("grounded contradiction was not rejected: %+v", result)
	}
}

func TestValidatorRejectsMissingExcludedAndStructurallyInvalidInputs(t *testing.T) {
	validator := New(nil)
	if result, err := validator.Validate(context.Background(), nil, knowledge.Proposal{}); err != nil || result.Valid || result.FailedChecks[0] != "incident_missing" {
		t.Fatalf("nil incident result=%+v err=%v", result, err)
	}
	incident, proposal := groundedFixture()
	incident.Namespace = "kubepilot-benchmark"
	if result, _ := validator.Validate(context.Background(), incident, proposal); result.Valid || result.FailedChecks[0] != "evaluation_isolation" {
		t.Fatalf("evaluation incident passed: %+v", result)
	}
	incident, proposal = groundedFixture()
	incident.Alerts = []domain.Alert{{Labels: map[string]string{"evaluation": "true"}}}
	if result, _ := validator.Validate(context.Background(), incident, proposal); result.Valid {
		t.Fatalf("labeled evaluation incident passed: %+v", result)
	}
	incident, proposal = groundedFixture()
	incident.Status = domain.StatusReceived
	if result, _ := validator.Validate(context.Background(), incident, proposal); result.Valid || result.FailedChecks[0] != "incident_not_resolved" {
		t.Fatalf("unresolved incident passed: %+v", result)
	}
	incident, proposal = groundedFixture()
	proposal.Pattern.Cause = ""
	proposal.Pattern.Nodes = []knowledge.CausalNode{{ID: "bad", Type: "unsupported"}}
	proposal.Pattern.Edges = []knowledge.CausalEdge{{From: "missing", To: "bad", Relation: "unsupported"}}
	proposal.Pattern.SupportingEvidence = nil
	if result, _ := validator.Validate(context.Background(), incident, proposal); result.Valid || len(result.FailedChecks) < 5 {
		t.Fatalf("structurally invalid proposal passed: %+v", result)
	}
}

func TestValidatorChecksIdentityObservationStoreAndActivation(t *testing.T) {
	incident, proposal := groundedFixture()
	wrongIdentity := proposal
	wrongIdentity.IncidentID = "other"
	if result, _ := New(nil).Validate(context.Background(), incident, wrongIdentity); result.Valid || result.FailedChecks[0] != "incident_identity_mismatch" {
		t.Fatalf("identity mismatch passed: %+v", result)
	}
	unknownObservation := proposal
	unknownObservation.Pattern.Nodes = append([]knowledge.CausalNode(nil), proposal.Pattern.Nodes...)
	for index := range unknownObservation.Pattern.Nodes {
		if unknownObservation.Pattern.Nodes[index].Type == "observation" {
			unknownObservation.Pattern.Nodes[index].SourceEvidenceIDs = []string{"missing"}
		}
	}
	if result, _ := New(nil).Validate(context.Background(), incident, unknownObservation); result.Valid || result.FailedChecks[0] != "observation_node_unobserved" {
		t.Fatalf("unknown observation passed: %+v", result)
	}
	if _, err := New(validatorReader{err: errors.New("store unavailable")}).Validate(context.Background(), incident, proposal); err == nil {
		t.Fatal("causal store failure was hidden")
	}
	existing := proposal.Pattern
	existing.SourceIncidents = []string{"first", "second"}
	result, err := New(validatorReader{patterns: []knowledge.CausalPattern{existing}}).Validate(context.Background(), incident, proposal)
	if err != nil || !result.Valid || !result.Accepted || result.SupportCount != 3 || result.Confidence != .9 {
		t.Fatalf("three qualified incidents did not activate: result=%+v err=%v", result, err)
	}
	existing.SourceIncidents = []string{incident.ID, "second"}
	result, err = New(validatorReader{patterns: []knowledge.CausalPattern{existing}}).Validate(context.Background(), incident, proposal)
	if err != nil || result.SupportCount != 2 || result.Accepted {
		t.Fatalf("duplicate incident increased support: result=%+v err=%v", result, err)
	}
	if max(2, 1) != 2 || max(1, 2) != 2 {
		t.Fatal("validator max helper returned the wrong bound")
	}
}
