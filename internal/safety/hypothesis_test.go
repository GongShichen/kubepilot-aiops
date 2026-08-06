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

func TestTransitionVerifiedIsAtomicAndCompatibilityHelperIsBounded(t *testing.T) {
	ledger := &domain.DiagnosisLedger{}
	items := []domain.VerifiedHypothesis{{Draft: domain.HypothesisDraft{ID: "h1"}, Status: domain.HypothesisSupported}}
	service := NewHypothesisTransitionService(ledger, items)
	if err := service.TransitionVerified(&items, "h1", domain.HypothesisSupported, domain.HypothesisAccepted, "accepted", "call", []string{"e1"}); err != nil {
		t.Fatal(err)
	}
	if items[0].Status != domain.HypothesisAccepted || service.Status("h1") != domain.HypothesisAccepted || len(ledger.HypothesisTransitions) != 1 {
		t.Fatalf("verified transition was not materialized: items=%+v ledger=%+v", items, ledger)
	}
	before := len(ledger.HypothesisTransitions)
	if err := service.TransitionVerified(&items, "missing", "", domain.HypothesisCreated, "invalid", "", nil); err == nil {
		t.Fatal("missing materialized hypothesis was accepted")
	}
	if len(ledger.HypothesisTransitions) != before || service.Status("missing") != "" {
		t.Fatal("failed verified transition changed lifecycle state")
	}
	compatibility := &domain.DiagnosisLedger{}
	if err := TransitionHypothesis(compatibility, "legacy", "", domain.HypothesisCreated, "created", "", nil); err != nil || len(compatibility.HypothesisTransitions) != 1 {
		t.Fatalf("compatibility transition failed: %v %+v", err, compatibility)
	}
	if err := TransitionHypothesis(nil, "legacy", "", domain.HypothesisCreated, "created", "", nil); err == nil {
		t.Fatal("nil compatibility ledger was accepted")
	}
}

func TestConfidenceIsClampedToPolicyRange(t *testing.T) {
	if score := Confidence(domain.VerifiedHypothesis{Draft: domain.HypothesisDraft{PriorProbability: 20}, SupportingScore: 20, CausalPathCoverage: 20, HistoricalRelevance: 20, TopologyRelevance: 20}, 0); score != 1 {
		t.Fatalf("upper confidence clamp=%f", score)
	}
	if score := Confidence(domain.VerifiedHypothesis{ContradictionScore: 20}, 0); score != 0 {
		t.Fatalf("lower confidence clamp=%f", score)
	}
}
