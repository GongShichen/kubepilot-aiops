package telemetry

import (
	"testing"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func TestObserveAgentProjectsRuntimeState(t *testing.T) {
	incident := &domain.Incident{
		RootCause: "database pool exhausted",
		AgentBudget: &domain.AgentBudgetState{
			IncidentUses: 7, IncidentCost: 11, IncidentTokens: 900,
			Usage: map[string]domain.AgentBudgetUsage{"diagnosis": {Iterations: 4, Corrections: 1}},
		},
		Evidence: []domain.Evidence{{Attribution: &domain.EvidenceAttribution{AttributionScore: .9}}},
		DiagnosisLedger: &domain.DiagnosisLedger{
			SelectedHypothesisID: "h1",
			Drafts:               []domain.HypothesisDraft{{ID: "h1"}},
			Verified: []domain.VerifiedHypothesis{{
				Draft: domain.HypothesisDraft{ID: "h1"}, Status: domain.HypothesisAccepted,
				ConfidenceHistory: []domain.HypothesisConfidenceRecord{{Sequence: 1}},
			}},
			SafetyFeedback: []domain.SafetyFeedback{{Allowed: false}},
			AgentDecisions: []domain.AgentDecisionEvent{{SelectedAction: "query_loki_evidence"}},
			Candidates:     []domain.RetrievalCandidate{{Rank: domain.RankBreakdown{TopologyScore: .8}}},
		},
	}
	got := ObserveAgent(incident)
	if got.Iterations != 4 || got.ToolUses != 7 || got.ToolCost != 11 || got.Tokens != 900 || got.Corrections != 1 {
		t.Fatalf("unexpected budget observation: %+v", got)
	}
	if !got.HypothesisConverged || !got.SelfCorrectionSucceeded || got.EvidenceQueries != 1 || got.TopologyCandidates != 1 || got.AttributedEvidence != 1 {
		t.Fatalf("unexpected reasoning observation: %+v", got)
	}
}
