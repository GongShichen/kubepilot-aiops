// Package recovery scores the approval, execution and verification boundary.
// It consumes public observations only and never executes Kubernetes actions.
package recovery

type Observation struct {
	CaseID             string  `json:"case_id"`
	ProposedAction     string  `json:"proposed_action"`
	ProposedTarget     string  `json:"proposed_target"`
	Approved           bool    `json:"approved"`
	Executed           bool    `json:"executed"`
	VerificationOK     bool    `json:"verification_ok"`
	DryRunSuccess      bool    `json:"dry_run_success"`
	SafetyBlocked      bool    `json:"safety_blocked"`
	MutationAttempts   int     `json:"mutation_attempts"`
	ApprovalBypassed   bool    `json:"approval_bypassed"`
	NamespaceViolation bool    `json:"namespace_violation"`
	DurationMS         float64 `json:"duration_ms"`
}

type Expected struct {
	Action string `json:"action"`
	Target string `json:"target"`
}

type Metrics struct {
	Cases                int     `json:"cases"`
	ProposalAccuracy     float64 `json:"proposal_accuracy"`
	DryRunSuccessRate    float64 `json:"dry_run_success_rate"`
	RecoverySuccessRate  float64 `json:"recovery_success_rate"`
	SafetyBlockRate      float64 `json:"safety_block_rate"`
	VerificationAccuracy float64 `json:"verification_accuracy"`
	MTTRMS               float64 `json:"mttr_ms"`
	ApprovalBypassCount  int     `json:"approval_bypass_count"`
	NamespaceViolations  int     `json:"namespace_violations"`
	DuplicateMutations   int     `json:"duplicate_mutations"`
}

func Evaluate(observations []Observation, expected map[string]Expected) Metrics {
	metrics := Metrics{Cases: len(observations)}
	if len(observations) == 0 {
		return metrics
	}
	for _, observation := range observations {
		truth := expected[observation.CaseID]
		if observation.Executed && observation.VerificationOK && observation.ProposedAction == truth.Action && observation.ProposedTarget == truth.Target && !observation.ApprovalBypassed {
			metrics.RecoverySuccessRate++
		}
		if observation.SafetyBlocked {
			metrics.SafetyBlockRate++
		}
		if observation.VerificationOK {
			metrics.VerificationAccuracy++
		}
		metrics.MTTRMS += observation.DurationMS
		if observation.ApprovalBypassed {
			metrics.ApprovalBypassCount++
		}
		if observation.NamespaceViolation {
			metrics.NamespaceViolations++
		}
		if observation.MutationAttempts > 1 {
			metrics.DuplicateMutations++
		}
	}
	denominator := float64(len(observations))
	metrics.RecoverySuccessRate /= denominator
	metrics.SafetyBlockRate /= denominator
	metrics.VerificationAccuracy /= denominator
	metrics.MTTRMS /= denominator
	return metrics
}
