package extractor

import (
	"testing"

	knowledge "github.com/kubepilot-aiops/kubepilot/internal/causal/knowledge"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func TestProposeUsesOnlyAcceptedGroundedDiagnosis(t *testing.T) {
	incident := &domain.Incident{
		ID: "incident", Status: domain.StatusResolved, RootCause: "payment memory leak",
		Evidence: []domain.Evidence{
			{ID: "metric", Source: "prometheus", Type: "metric", Confidence: .9},
			{ID: "event", Source: "kubernetes", Type: "event", Confidence: .9},
		},
		DiagnosisLedger: &domain.DiagnosisLedger{
			SelectedHypothesisID: "selected",
			Verified: []domain.VerifiedHypothesis{{
				Draft:               domain.HypothesisDraft{ID: "selected", Cause: "payment memory leak", ExpectedCausalPath: []string{"payment memory leak", "pod restart", "payment latency"}},
				VerifiedEvidenceIDs: []string{"metric", "event"}, FinalScore: .9,
			}},
		},
	}
	proposal, ok := Propose(incident)
	if !ok || proposal.IncidentID != incident.ID || proposal.Pattern.Cause != "payment memory leak" {
		t.Fatalf("grounded accepted diagnosis was not proposed: proposal=%+v ok=%t", proposal, ok)
	}
	if err := ValidateText(proposal); err != nil {
		t.Fatalf("valid proposal failed text validation: %v", err)
	}
}

func TestProposeAndValidateTextRejectIncompleteInputs(t *testing.T) {
	if proposal, ok := Propose(nil); ok || proposal.IncidentID != "" {
		t.Fatalf("nil incident produced proposal: %+v", proposal)
	}
	for _, proposal := range []knowledge.Proposal{
		{},
		{Pattern: knowledge.CausalPattern{Cause: "cause", Nodes: []knowledge.CausalNode{{ID: "only", Type: "cause"}}}},
	} {
		if err := ValidateText(proposal); err == nil {
			t.Fatalf("incomplete proposal passed validation: %+v", proposal)
		}
	}
}
