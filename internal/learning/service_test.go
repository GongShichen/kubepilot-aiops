package learning

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

type testGenerator struct{ candidate domain.PolicyVersion }

func (generator testGenerator) Propose(context.Context, []domain.Incident, domain.PolicyVersion) (domain.PolicyVersion, error) {
	return generator.candidate, nil
}

type testEvaluator struct{ metrics domain.PolicyMetrics }

func (evaluator testEvaluator) Evaluate(context.Context, domain.PolicyVersion) (string, domain.PolicyMetrics, error) {
	return "validation-replay", evaluator.metrics, nil
}

type failingGenerator struct{}

func (failingGenerator) Propose(context.Context, []domain.Incident, domain.PolicyVersion) (domain.PolicyVersion, error) {
	return domain.PolicyVersion{}, errors.New("generation failed")
}

type failingEvaluator struct{}

func (failingEvaluator) Evaluate(context.Context, domain.PolicyVersion) (string, domain.PolicyMetrics, error) {
	return "", domain.PolicyMetrics{}, errors.New("evaluation failed")
}

type testStore struct {
	policies    []domain.PolicyVersion
	evaluations []domain.PolicyEvaluation
}

func (store *testStore) SavePolicyVersion(_ context.Context, policy domain.PolicyVersion) error {
	store.policies = append(store.policies, policy)
	return nil
}

func (store *testStore) SavePolicyEvaluation(_ context.Context, evaluation domain.PolicyEvaluation) error {
	store.evaluations = append(store.evaluations, evaluation)
	return nil
}

func TestPromotionRequiresAccuracyConfidenceSafetyAndCost(t *testing.T) {
	baseline := domain.PolicyMetrics{StrictDiagnosisAccuracy: .70, RecoverySuccess: .80, MeanCost: 1, P95LatencySeconds: 10}
	good := domain.PolicyMetrics{StrictDiagnosisAccuracy: .74, AccuracyDifferenceLower: .01, RecoverySuccess: .81, MeanCost: 1.2, P95LatencySeconds: 11}
	decision := DecidePromotion("current", baseline, good, false)
	if !decision.Accepted || len(decision.RolloutStages) != 3 || decision.RollbackPolicy != "current" {
		t.Fatalf("good candidate was rejected: %+v", decision)
	}
	unsafe := good
	unsafe.NamespaceViolations = 1
	if decision = DecidePromotion("current", baseline, unsafe, false); decision.Accepted {
		t.Fatalf("unsafe candidate was accepted: %+v", decision)
	}
}

func TestEligibleFeedbackExcludesBenchmarkAndUnverifiedIncidents(t *testing.T) {
	items := []domain.Incident{
		{ID: "production", Namespace: "production", Status: domain.StatusResolved, Verification: &domain.Verification{Success: true}, ExecutionContext: &domain.ExecutionContext{ApprovalID: "approval"}},
		{ID: "benchmark", Namespace: "kubepilot-benchmark", Status: domain.StatusResolved, Verification: &domain.Verification{Success: true}, ExecutionContext: &domain.ExecutionContext{ApprovalID: "approval"}},
		{ID: "unverified", Namespace: "production", Status: domain.StatusResolved},
	}
	eligible := eligibleFeedback(items)
	if len(eligible) != 1 || eligible[0].ID != "production" {
		t.Fatalf("unexpected eligible feedback: %+v", eligible)
	}
}

func TestImprovePersistsCandidateEvaluationAndShadowDecision(t *testing.T) {
	baseline := domain.PolicyMetrics{StrictDiagnosisAccuracy: .70, RecoverySuccess: .80, MeanCost: 1, P95LatencySeconds: 10}
	candidateMetrics := domain.PolicyMetrics{StrictDiagnosisAccuracy: .74, AccuracyDifferenceLower: .01, RecoverySuccess: .81, MeanCost: 1.2, P95LatencySeconds: 11}
	store := &testStore{}
	service := Service{
		Generator: testGenerator{candidate: domain.PolicyVersion{ID: "planner-memory-candidate", Definition: domain.PolicyDefinition{DebateConfidenceGate: .8, DebateMarginGate: .15, MaximumDebateRounds: 2}}},
		Evaluator: testEvaluator{metrics: candidateMetrics}, Store: store,
	}
	incident := domain.Incident{ID: "production", Namespace: "production", Status: domain.StatusResolved, Verification: &domain.Verification{Success: true}, ExecutionContext: &domain.ExecutionContext{ApprovalID: "approval"}, UpdatedAt: time.Now().UTC()}
	policy, evaluation, decision, err := service.Improve(context.Background(), []domain.Incident{incident}, domain.PolicyVersion{ID: "current"}, baseline, false)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Status != "shadow" || !evaluation.Accepted || !decision.Accepted {
		t.Fatalf("unexpected learning result: policy=%+v evaluation=%+v decision=%+v", policy, evaluation, decision)
	}
	if len(store.policies) != 2 || store.policies[0].Status != "candidate" || store.policies[1].Status != "shadow" || len(store.evaluations) != 1 {
		t.Fatalf("unexpected persistence sequence: policies=%+v evaluations=%+v", store.policies, store.evaluations)
	}
}

func TestRolloutRequiresHumanApprovalAndAdvancesInFixedStages(t *testing.T) {
	store := &testStore{}
	service := Service{Store: store}
	policy := domain.PolicyVersion{ID: "candidate", Status: "shadow", PreviousPolicyID: "current"}
	if _, err := service.AdvanceRollout(context.Background(), policy, .25, "operator"); err == nil {
		t.Fatal("rollout skipped the five-percent stage")
	}
	if _, err := service.AdvanceRollout(context.Background(), policy, .05, ""); err == nil {
		t.Fatal("rollout advanced without human approval")
	}
	for _, stage := range []float64{.05, .25, 1} {
		var err error
		policy, err = service.AdvanceRollout(context.Background(), policy, stage, "operator")
		if err != nil {
			t.Fatal(err)
		}
	}
	if policy.Status != "active" || policy.RolloutFraction != 1 || policy.PromotedAt.IsZero() {
		t.Fatalf("candidate was not promoted: %+v", policy)
	}
	retired, restored, err := service.Rollback(context.Background(), policy, domain.PolicyVersion{ID: "current", Status: "retired"}, "operator")
	if err != nil {
		t.Fatal(err)
	}
	if retired.Status != "retired" || restored.Status != "active" || restored.RolloutFraction != 1 {
		t.Fatalf("rollback failed: retired=%+v restored=%+v", retired, restored)
	}
}

func TestLearningRejectsIncompleteInvalidAndRegressingCandidates(t *testing.T) {
	incident := domain.Incident{ID: "production", Namespace: "production", Status: domain.StatusResolved, Verification: &domain.Verification{Success: true}, ExecutionContext: &domain.ExecutionContext{ApprovalID: "approval"}}
	if _, _, _, err := (Service{}).Improve(context.Background(), []domain.Incident{incident}, domain.PolicyVersion{}, domain.PolicyMetrics{}, false); err == nil {
		t.Fatal("incomplete learning dependencies were accepted")
	}
	store := &testStore{}
	service := Service{Generator: failingGenerator{}, Evaluator: testEvaluator{}, Store: store}
	if _, _, _, err := service.Improve(context.Background(), []domain.Incident{incident}, domain.PolicyVersion{}, domain.PolicyMetrics{}, false); err == nil {
		t.Fatal("candidate generation failure was hidden")
	}
	service.Generator = testGenerator{candidate: domain.PolicyVersion{ID: "", Definition: domain.PolicyDefinition{MaximumDebateRounds: 3}}}
	if _, _, _, err := service.Improve(context.Background(), []domain.Incident{incident}, domain.PolicyVersion{}, domain.PolicyMetrics{}, false); err == nil {
		t.Fatal("invalid candidate was accepted")
	}
	service.Generator = testGenerator{candidate: domain.PolicyVersion{ID: "candidate", Definition: domain.PolicyDefinition{DebateConfidenceGate: .8, DebateMarginGate: .15, MaximumDebateRounds: 2}}}
	service.Evaluator = failingEvaluator{}
	if _, _, _, err := service.Improve(context.Background(), []domain.Incident{incident}, domain.PolicyVersion{}, domain.PolicyMetrics{}, false); err == nil {
		t.Fatal("replay evaluation failure was hidden")
	}
	baseline := domain.PolicyMetrics{StrictDiagnosisAccuracy: .8, RecoverySuccess: .9, MeanCost: 1, P95LatencySeconds: 10}
	regression := domain.PolicyMetrics{StrictDiagnosisAccuracy: .81, AccuracyDifferenceLower: 0, RecoverySuccess: .8, MeanCost: 2, P95LatencySeconds: 20, ApprovalBypasses: 1, NamespaceViolations: 1, DuplicateMutations: 1}
	decision := DecidePromotion("current", baseline, regression, false)
	if decision.Accepted || !strings.Contains(decision.Reason, "recovery success regressed") || !strings.Contains(decision.Reason, "twenty-five percent") || !strings.Contains(decision.Reason, "p95 latency") {
		t.Fatalf("regressing policy was not fully rejected: %+v", decision)
	}
}

func TestLearningRolloutAndRollbackRejectInvalidState(t *testing.T) {
	service := Service{}
	if _, err := service.AdvanceRollout(context.Background(), domain.PolicyVersion{}, .05, "operator"); err == nil {
		t.Fatal("rollout without a store was accepted")
	}
	service.Store = &testStore{}
	if _, err := service.AdvanceRollout(context.Background(), domain.PolicyVersion{Status: "candidate"}, .05, "operator"); err == nil {
		t.Fatal("non-shadow rollout was accepted")
	}
	if _, _, err := (Service{}).Rollback(context.Background(), domain.PolicyVersion{}, domain.PolicyVersion{}, "operator"); err == nil {
		t.Fatal("rollback without a store was accepted")
	}
	if _, _, err := service.Rollback(context.Background(), domain.PolicyVersion{PreviousPolicyID: "previous"}, domain.PolicyVersion{ID: "other"}, "operator"); err == nil {
		t.Fatal("mismatched rollback target was accepted")
	}
	if _, _, err := service.Rollback(context.Background(), domain.PolicyVersion{PreviousPolicyID: "previous"}, domain.PolicyVersion{ID: "previous"}, ""); err == nil {
		t.Fatal("rollback without operator was accepted")
	}
}

func TestCandidateValidationBoundsEveryLearnableKnob(t *testing.T) {
	for _, candidate := range []domain.PolicyVersion{
		{ID: "candidate", Definition: domain.PolicyDefinition{DebateConfidenceGate: -1}},
		{ID: "candidate", Definition: domain.PolicyDefinition{DebateMarginGate: 2}},
		{ID: "candidate", Definition: domain.PolicyDefinition{MaximumDebateRounds: 3}},
		{ID: "candidate", Definition: domain.PolicyDefinition{PlannerTaskWeights: map[string]float64{"": .5}}},
		{ID: "candidate", Definition: domain.PolicyDefinition{MemoryRankingWeights: map[string]float64{"episodic": -1}}},
		{ID: "candidate", Definition: domain.PolicyDefinition{HypothesisWeights: map[string]float64{"evidence": 2}}},
	} {
		if err := validateCandidate(candidate); err == nil {
			t.Fatalf("invalid candidate passed validation: %+v", candidate)
		}
	}
}
