package safety

import (
	"fmt"
	"sync"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

// HypothesisTransitionService is the sole owner of hypothesis lifecycle
// transitions. Callers provide the current verified slice only when a
// transition also needs to update its materialized status; they never write
// lifecycle status directly.
type HypothesisTransitionService struct {
	mu       sync.Mutex
	ledger   *domain.DiagnosisLedger
	statuses map[string]domain.HypothesisStatus
}

func NewHypothesisTransitionService(ledger *domain.DiagnosisLedger, verified []domain.VerifiedHypothesis) *HypothesisTransitionService {
	statuses := make(map[string]domain.HypothesisStatus, len(verified))
	for _, item := range verified {
		if item.Draft.ID != "" && item.Status != "" {
			statuses[item.Draft.ID] = item.Status
		}
	}
	return &HypothesisTransitionService{ledger: ledger, statuses: statuses}
}

func (s *HypothesisTransitionService) Status(id string) domain.HypothesisStatus {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statuses[id]
}

func (s *HypothesisTransitionService) Transition(id string, from, to domain.HypothesisStatus, reason, toolCallID string, evidenceIDs []string) error {
	if s == nil || s.ledger == nil || id == "" {
		return fmt.Errorf("hypothesis transition service is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transitionLocked(id, from, to, reason, toolCallID, evidenceIDs)
}

func (s *HypothesisTransitionService) transitionLocked(id string, from, to domain.HypothesisStatus, reason, toolCallID string, evidenceIDs []string) error {
	current, exists := s.statuses[id]
	if !exists && from != "" {
		return fmt.Errorf("hypothesis %q has no current lifecycle status", id)
	}
	if exists && current != from {
		return fmt.Errorf("hypothesis %q transition expected %s, current %s", id, from, current)
	}
	if current == domain.HypothesisRefuted || (from == domain.HypothesisRefuted && to != domain.HypothesisCreated) {
		return fmt.Errorf("refuted hypothesis %q cannot be reused", id)
	}
	if !CanTransitionHypothesis(from, to) {
		return fmt.Errorf("invalid hypothesis transition %s -> %s", from, to)
	}
	s.ledger.HypothesisTransitions = append(s.ledger.HypothesisTransitions, domain.HypothesisTransition{HypothesisID: id, From: from, To: to, EvidenceIDs: append([]string(nil), evidenceIDs...), ToolCallID: toolCallID, Reason: reason, OccurredAt: time.Now().UTC()})
	s.statuses[id] = to
	return nil
}

// TransitionVerified applies a validated transition and updates the
// materialized verified ledger atomically under the same service lock.
func (s *HypothesisTransitionService) TransitionVerified(items *[]domain.VerifiedHypothesis, id string, from, to domain.HypothesisStatus, reason, toolCallID string, evidenceIDs []string) error {
	if s == nil || s.ledger == nil || id == "" {
		return fmt.Errorf("hypothesis transition service is unavailable")
	}
	if items == nil {
		return fmt.Errorf("verified hypothesis slice is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	selected := -1
	for index := range *items {
		if (*items)[index].Draft.ID == id {
			selected = index
			break
		}
	}
	if selected < 0 {
		return fmt.Errorf("hypothesis %q is not present in verified ledger", id)
	}
	if err := s.transitionLocked(id, from, to, reason, toolCallID, evidenceIDs); err != nil {
		return err
	}
	(*items)[selected].Status = to
	return nil
}

func CanTransitionHypothesis(from, to domain.HypothesisStatus) bool {
	allowed := map[domain.HypothesisStatus]map[domain.HypothesisStatus]bool{
		"":                                 {domain.HypothesisCreated: true},
		domain.HypothesisCreated:           {domain.HypothesisEvidenceSearching: true},
		domain.HypothesisEvidenceSearching: {domain.HypothesisSupported: true, domain.HypothesisRefuted: true},
		domain.HypothesisSupported:         {domain.HypothesisEvidenceSearching: true, domain.HypothesisAccepted: true},
	}
	return allowed[from][to]
}

func TransitionHypothesis(ledger *domain.DiagnosisLedger, id string, from, to domain.HypothesisStatus, reason, toolCallID string, evidenceIDs []string) error {
	// Compatibility helper for non-runtime callers. Production Agent tools use
	// HypothesisTransitionService so current status and refuted identities are
	// validated rather than merely appending an audit row.
	if ledger == nil || id == "" || !CanTransitionHypothesis(from, to) || from == domain.HypothesisRefuted {
		return fmt.Errorf("invalid hypothesis transition %s -> %s", from, to)
	}
	ledger.HypothesisTransitions = append(ledger.HypothesisTransitions, domain.HypothesisTransition{HypothesisID: id, From: from, To: to, EvidenceIDs: append([]string(nil), evidenceIDs...), ToolCallID: toolCallID, Reason: reason, OccurredAt: time.Now().UTC()})
	return nil
}

// Confidence is the objective diagnostic score. It intentionally excludes
// model, historical and topology priors: those may guide investigation or a
// human's review, but cannot raise the evidence confidence used by recovery.
func Confidence(item domain.VerifiedHypothesis, _ float64) float64 {
	score := .50*item.SupportingScore + .30*item.CausalPathCoverage + .20*item.ObservationCoverage - .30*item.ContradictionScore
	return clamp(score)
}

func clamp(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}
