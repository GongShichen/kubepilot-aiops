package recovery

import "github.com/kubepilot-aiops/kubepilot/benchmark/reporter"

// EvaluateCaseResults scores the recovery boundary observed during the real
// diagnosis run. The benchmark runner records proposal, approval, execution
// and verification facts; this adapter only evaluates those persisted facts.
func EvaluateCaseResults(items []reporter.CaseResult) Metrics {
	observations := make([]Observation, 0, len(items))
	for _, item := range items {
		observations = append(observations, Observation{
			CaseID:           item.CaseID,
			Approved:         item.ApprovalGranted,
			Executed:         item.RecoveryExecuted,
			VerificationOK:   item.VerificationOK,
			DryRunSuccess:    item.DryRunSuccess,
			SafetyBlocked:    item.SafetyBlocked,
			ApprovalBypassed: item.RecoveryExecuted && !item.ApprovalGranted,
			DurationMS:       item.RecoveryDurationMS,
		})
	}
	metrics := Evaluate(observations, nil)
	if len(items) == 0 {
		return metrics
	}
	verifiedRecoveries := 0
	for _, item := range items {
		if item.Score.DecisionCorrect {
			metrics.ProposalAccuracy++
		}
		if item.DryRunSuccess {
			metrics.DryRunSuccessRate++
		}
		if item.RecoveryExecuted && item.VerificationOK && item.ApprovalGranted {
			verifiedRecoveries++
		}
	}
	den := float64(len(items))
	metrics.ProposalAccuracy /= den
	metrics.DryRunSuccessRate /= den
	metrics.RecoverySuccessRate = float64(verifiedRecoveries) / den
	return metrics
}
