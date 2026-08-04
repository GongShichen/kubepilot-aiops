package safety

import (
	"math"
	"testing"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func TestHypothesisLifecycleAndConfidence(t *testing.T) {
	if !CanTransitionHypothesis("", domain.HypothesisCreated) || !CanTransitionHypothesis(domain.HypothesisSupported, domain.HypothesisEvidenceSearching) || CanTransitionHypothesis(domain.HypothesisRefuted, domain.HypothesisSupported) {
		t.Fatal("hypothesis lifecycle does not enforce immutable refutation")
	}
	item := domain.VerifiedHypothesis{Draft: domain.HypothesisDraft{PriorProbability: 1}, SupportingScore: 1, CausalPathCoverage: 1, HistoricalRelevance: 1, TopologyRelevance: 1, ContradictionScore: .2}
	if score := Confidence(item, 1); math.Abs(score-.94) > 1e-9 {
		t.Fatalf("confidence formula mismatch: got %.6f want .94", score)
	}
	item.ContradictionScore = .8
	if lower := Confidence(item, .2); lower >= .94 {
		t.Fatalf("contradiction and temporal decay did not lower confidence: %.6f", lower)
	}
}

func TestHypothesisTransitionServiceRejectsStaleAndRefutedReuse(t *testing.T) {
	ledger := &domain.DiagnosisLedger{}
	service := NewHypothesisTransitionService(ledger, nil)
	if err := service.Transition("h1", "", domain.HypothesisCreated, "created", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := service.Transition("h1", domain.HypothesisCreated, domain.HypothesisEvidenceSearching, "searching", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := service.Transition("h1", domain.HypothesisEvidenceSearching, domain.HypothesisRefuted, "contradiction", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := service.Transition("h1", domain.HypothesisRefuted, domain.HypothesisCreated, "reuse", "", nil); err == nil {
		t.Fatal("expected refuted identity reuse to be rejected")
	}
	if err := service.Transition("h1", domain.HypothesisCreated, domain.HypothesisEvidenceSearching, "stale", "", nil); err == nil {
		t.Fatal("expected stale transition to be rejected")
	}
	if got := service.Status("h1"); got != domain.HypothesisRefuted {
		t.Fatalf("unexpected lifecycle status: %s", got)
	}
}
