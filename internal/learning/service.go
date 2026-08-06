package learning

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

type CandidateGenerator interface {
	Propose(context.Context, []domain.Incident, domain.PolicyVersion) (domain.PolicyVersion, error)
}

type ReplayEvaluator interface {
	Evaluate(context.Context, domain.PolicyVersion) (string, domain.PolicyMetrics, error)
}

type Store interface {
	SavePolicyVersion(context.Context, domain.PolicyVersion) error
	SavePolicyEvaluation(context.Context, domain.PolicyEvaluation) error
}

type Service struct {
	Generator CandidateGenerator
	Evaluator ReplayEvaluator
	Store     Store
}

type PromotionDecision struct {
	Accepted       bool      `json:"accepted"`
	Reason         string    `json:"reason"`
	RolloutStages  []float64 `json:"rollout_stages,omitempty"`
	RollbackPolicy string    `json:"rollback_policy,omitempty"`
}

func (service Service) Improve(ctx context.Context, verified []domain.Incident, current domain.PolicyVersion, baseline domain.PolicyMetrics, allowCostOverride bool) (domain.PolicyVersion, domain.PolicyEvaluation, PromotionDecision, error) {
	if service.Generator == nil || service.Evaluator == nil || service.Store == nil {
		return domain.PolicyVersion{}, domain.PolicyEvaluation{}, PromotionDecision{}, fmt.Errorf("learning service dependencies are incomplete")
	}
	eligible := eligibleFeedback(verified)
	if len(eligible) == 0 {
		return domain.PolicyVersion{}, domain.PolicyEvaluation{}, PromotionDecision{}, fmt.Errorf("no verified production feedback is eligible")
	}
	candidate, err := service.Generator.Propose(ctx, eligible, current)
	if err != nil {
		return domain.PolicyVersion{}, domain.PolicyEvaluation{}, PromotionDecision{}, err
	}
	if err = validateCandidate(candidate); err != nil {
		return domain.PolicyVersion{}, domain.PolicyEvaluation{}, PromotionDecision{}, err
	}
	candidate.Status = "candidate"
	if candidate.CreatedAt.IsZero() {
		candidate.CreatedAt = time.Now().UTC()
	}
	if err = service.Store.SavePolicyVersion(ctx, candidate); err != nil {
		return domain.PolicyVersion{}, domain.PolicyEvaluation{}, PromotionDecision{}, err
	}
	runID, metrics, err := service.Evaluator.Evaluate(ctx, candidate)
	if err != nil {
		return candidate, domain.PolicyEvaluation{}, PromotionDecision{}, err
	}
	decision := DecidePromotion(current.ID, baseline, metrics, allowCostOverride)
	evaluation := domain.PolicyEvaluation{PolicyID: candidate.ID, RunID: runID, Metrics: metrics, Accepted: decision.Accepted, Reason: decision.Reason, CreatedAt: time.Now().UTC()}
	if decision.Accepted {
		candidate.Status = "shadow"
		candidate.PreviousPolicyID = current.ID
	} else {
		candidate.Status = "rejected"
	}
	if err = service.Store.SavePolicyEvaluation(ctx, evaluation); err != nil {
		return candidate, evaluation, decision, err
	}
	if err = service.Store.SavePolicyVersion(ctx, candidate); err != nil {
		return candidate, evaluation, decision, err
	}
	return candidate, evaluation, decision, nil
}

func (service Service) AdvanceRollout(ctx context.Context, candidate domain.PolicyVersion, fraction float64, operator string) (domain.PolicyVersion, error) {
	if service.Store == nil {
		return candidate, fmt.Errorf("learning store is unavailable")
	}
	if strings.TrimSpace(operator) == "" {
		return candidate, fmt.Errorf("human rollout approval is required")
	}
	if candidate.Status != "shadow" && candidate.Status != "active" {
		return candidate, fmt.Errorf("only a shadow or active candidate can advance")
	}
	stages := []float64{.05, .25, 1}
	expected := 0.0
	for _, stage := range stages {
		if stage > candidate.RolloutFraction {
			expected = stage
			break
		}
	}
	if math.Abs(fraction-expected) > 1e-9 {
		return candidate, fmt.Errorf("next rollout stage must be %.2f", expected)
	}
	candidate.RolloutFraction = fraction
	candidate.ApprovedBy = operator
	if fraction == 1 {
		candidate.Status = "active"
		candidate.PromotedAt = time.Now().UTC()
	} else {
		candidate.Status = "shadow"
	}
	if err := service.Store.SavePolicyVersion(ctx, candidate); err != nil {
		return candidate, err
	}
	return candidate, nil
}

func (service Service) Rollback(ctx context.Context, candidate, previous domain.PolicyVersion, operator string) (domain.PolicyVersion, domain.PolicyVersion, error) {
	if service.Store == nil {
		return candidate, previous, fmt.Errorf("learning store is unavailable")
	}
	if strings.TrimSpace(operator) == "" {
		return candidate, previous, fmt.Errorf("human rollback approval is required")
	}
	if candidate.PreviousPolicyID == "" || candidate.PreviousPolicyID != previous.ID {
		return candidate, previous, fmt.Errorf("rollback target does not match the candidate predecessor")
	}
	candidate.Status = "retired"
	candidate.RolloutFraction = 0
	candidate.ApprovedBy = operator
	previous.Status = "active"
	previous.RolloutFraction = 1
	previous.ApprovedBy = operator
	previous.PromotedAt = time.Now().UTC()
	if err := service.Store.SavePolicyVersion(ctx, candidate); err != nil {
		return candidate, previous, err
	}
	if err := service.Store.SavePolicyVersion(ctx, previous); err != nil {
		return candidate, previous, err
	}
	return candidate, previous, nil
}

func DecidePromotion(currentPolicyID string, baseline, candidate domain.PolicyMetrics, allowCostOverride bool) PromotionDecision {
	decision := PromotionDecision{RollbackPolicy: currentPolicyID}
	reasons := []string{}
	if candidate.StrictDiagnosisAccuracy-baseline.StrictDiagnosisAccuracy < .03 {
		reasons = append(reasons, "strict diagnosis accuracy improved by less than three percentage points")
	}
	if candidate.AccuracyDifferenceLower <= 0 {
		reasons = append(reasons, "accuracy improvement confidence interval includes zero")
	}
	if candidate.RecoverySuccess < baseline.RecoverySuccess {
		reasons = append(reasons, "recovery success regressed")
	}
	if candidate.ApprovalBypasses != 0 || candidate.NamespaceViolations != 0 || candidate.DuplicateMutations != 0 {
		reasons = append(reasons, "a protected safety invariant was violated")
	}
	if !allowCostOverride && baseline.MeanCost > 0 && candidate.MeanCost > baseline.MeanCost*1.25 {
		reasons = append(reasons, "mean cost increased by more than twenty-five percent")
	}
	if baseline.P95LatencySeconds > 0 && candidate.P95LatencySeconds > baseline.P95LatencySeconds*1.25 {
		reasons = append(reasons, "p95 latency exceeded the rollout guardrail")
	}
	if len(reasons) > 0 {
		decision.Reason = strings.Join(reasons, "; ")
		return decision
	}
	decision.Accepted = true
	decision.Reason = "candidate passed offline replay gates and may enter shadow evaluation"
	decision.RolloutStages = []float64{.05, .25, 1}
	return decision
}

func validateCandidate(candidate domain.PolicyVersion) error {
	if strings.TrimSpace(candidate.ID) == "" {
		return fmt.Errorf("candidate policy ID is required")
	}
	definition := candidate.Definition
	if definition.DebateConfidenceGate < 0 || definition.DebateConfidenceGate > 1 || definition.DebateMarginGate < 0 || definition.DebateMarginGate > 1 {
		return fmt.Errorf("debate gates must be within zero and one")
	}
	if definition.MaximumDebateRounds < 0 || definition.MaximumDebateRounds > 2 {
		return fmt.Errorf("maximum debate rounds cannot exceed two")
	}
	for name, group := range map[string]map[string]float64{"planner": definition.PlannerTaskWeights, "memory": definition.MemoryRankingWeights, "hypothesis": definition.HypothesisWeights} {
		for key, value := range group {
			if strings.TrimSpace(key) == "" || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
				return fmt.Errorf("%s policy weight %q is invalid", name, key)
			}
		}
	}
	return nil
}

func eligibleFeedback(items []domain.Incident) []domain.Incident {
	out := make([]domain.Incident, 0, len(items))
	for _, incident := range items {
		if incident.Namespace == "kubepilot-benchmark" || incident.Status != domain.StatusResolved || incident.Verification == nil || !incident.Verification.Success || incident.ExecutionContext == nil || incident.ExecutionContext.ApprovalID == "" {
			continue
		}
		out = append(out, incident)
	}
	return out
}
