package agent

import (
	"context"
	"testing"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/internal/safety"
)

func TestMergeVerificationHistoryTracksConfidenceDecayAndEvidenceDelta(t *testing.T) {
	previous := domain.VerifiedHypothesis{
		Draft:               domain.HypothesisDraft{ID: "h1"},
		Status:              domain.HypothesisSupported,
		FinalScore:          .91,
		VerifiedEvidenceIDs: []string{"e1", "e2"},
		ConfidenceHistory:   []domain.HypothesisConfidenceRecord{{HypothesisID: "h1", Sequence: 1, Score: .91}},
	}
	current := domain.VerifiedHypothesis{
		Draft:               domain.HypothesisDraft{ID: "h1"},
		Status:              domain.HypothesisEvidenceSearching,
		FinalScore:          .55,
		VerifiedEvidenceIDs: []string{"e2", "e3"},
		ConfidenceHistory:   []domain.HypothesisConfidenceRecord{{HypothesisID: "h1", Sequence: 1, Score: .55}},
	}
	merged := mergeVerificationHistory(previous, current)
	if len(merged.ConfidenceHistory) != 2 || merged.ConfidenceHistory[1].Sequence != 2 || merged.ConfidenceHistory[1].Score >= merged.ConfidenceHistory[0].Score {
		t.Fatalf("confidence history was not appended with decay: %+v", merged.ConfidenceHistory)
	}
	if got := merged.ConfidenceHistory[1].AddedEvidenceIDs; len(got) != 1 || got[0] != "e3" {
		t.Fatalf("added evidence delta is wrong: %v", got)
	}
	if got := merged.ConfidenceHistory[1].RemovedEvidenceIDs; len(got) != 1 || got[0] != "e1" {
		t.Fatalf("removed evidence delta is wrong: %v", got)
	}
}

func TestUnknownEvidenceReturnsRepairableToolObservation(t *testing.T) {
	limits := map[string]domain.AgentBudget{DiagnosisAgentName: {MaxIterations: 4, MaxToolUses: 4, MaxTokens: 1000, MaxCorrections: 2}}
	budget := safety.NewBudgetController(nil, limits, map[string]int{"submit_hypotheses": 1})
	state := &WorkflowState{Incident: &domain.Incident{Evidence: []domain.Evidence{{ID: "evidence-1", Source: "kubernetes"}}}}
	runtime := &constrainedRuntime{state: state, budgets: budget, done: map[string]bool{}}
	ctx := withConstrainedRuntime(context.Background(), runtime)
	draft := domain.HypothesisDraft{ID: "h1", SupportingEvidenceIDs: []string{"unknown"}, ExpectedCausalPath: []string{"cause", "symptom"}}
	result, err := recordHypotheses(ctx, HypothesisSubmission{ReasoningType: "hypothesis_verification", Hypotheses: []domain.HypothesisDraft{draft}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Feedback == nil || result.Feedback.Category != domain.SafetyRepairable || !result.Feedback.Retryable {
		t.Fatalf("unknown evidence did not return repairable feedback: %+v", result.Feedback)
	}
	if len(state.HypothesisDrafts) != 0 {
		t.Fatal("invalid hypothesis reached the ledger")
	}
	draft.SupportingEvidenceIDs = []string{"evidence-1"}
	result, err = recordHypotheses(ctx, HypothesisSubmission{ReasoningType: "hypothesis_verification", Hypotheses: []domain.HypothesisDraft{draft}})
	if err != nil || !result.OK || len(state.HypothesisDrafts) != 1 {
		t.Fatalf("corrected hypothesis was not accepted: result=%+v err=%v", result, err)
	}
}
