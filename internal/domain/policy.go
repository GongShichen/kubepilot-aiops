package domain

import "time"

type PolicyDefinition struct {
	PlannerTaskWeights   map[string]float64 `json:"planner_task_weights,omitempty"`
	MemoryRankingWeights map[string]float64 `json:"memory_ranking_weights,omitempty"`
	HypothesisWeights    map[string]float64 `json:"hypothesis_weights,omitempty"`
	DebateConfidenceGate float64            `json:"debate_confidence_gate,omitempty"`
	DebateMarginGate     float64            `json:"debate_margin_gate,omitempty"`
	MaximumDebateRounds  int                `json:"maximum_debate_rounds,omitempty"`
}

type PolicyVersion struct {
	ID               string           `json:"id"`
	Status           string           `json:"status"`
	Definition       PolicyDefinition `json:"definition"`
	PreviousPolicyID string           `json:"previous_policy_id,omitempty"`
	RolloutFraction  float64          `json:"rollout_fraction,omitempty"`
	ApprovedBy       string           `json:"approved_by,omitempty"`
	CreatedAt        time.Time        `json:"created_at"`
	PromotedAt       time.Time        `json:"promoted_at,omitempty"`
}

type PolicyMetrics struct {
	StrictDiagnosisAccuracy float64 `json:"strict_diagnosis_accuracy"`
	AccuracyDifferenceLower float64 `json:"accuracy_difference_lower"`
	RecoverySuccess         float64 `json:"recovery_success"`
	MeanCost                float64 `json:"mean_cost"`
	P95LatencySeconds       float64 `json:"p95_latency_seconds"`
	ApprovalBypasses        int     `json:"approval_bypasses"`
	NamespaceViolations     int     `json:"namespace_violations"`
	DuplicateMutations      int     `json:"duplicate_mutations"`
}

type PolicyEvaluation struct {
	PolicyID  string        `json:"policy_id"`
	RunID     string        `json:"run_id"`
	Metrics   PolicyMetrics `json:"metrics"`
	Accepted  bool          `json:"accepted"`
	Reason    string        `json:"reason"`
	CreatedAt time.Time     `json:"created_at"`
}
