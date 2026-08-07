package agent

import (
	"context"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/causal"
	causalknowledge "github.com/kubepilot-aiops/kubepilot/internal/causal/knowledge"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/internal/topology"
)

type Collector interface {
	Collect(context.Context, *domain.Incident, domain.EvidenceRequest) ([]domain.Evidence, error)
}
type WorkflowState struct {
	Workflow           string                                 `json:"workflow"`
	Incident           *domain.Incident                       `json:"incident"`
	EvidencePlan       EvidencePlan                           `json:"evidence_plan"`
	DryRun             *domain.DryRunResult                   `json:"dry_run,omitempty"`
	ExecutionContext   *domain.ExecutionContext               `json:"execution_context,omitempty"`
	VerificationState  VerificationState                      `json:"verification_state"`
	ModelSnapshotHash  string                                 `json:"model_snapshot_hash,omitempty"`
	ToolCalls          int                                    `json:"tool_calls"`
	DiagnosisAttempts  int                                    `json:"diagnosis_attempts"`
	Errors             []string                               `json:"errors,omitempty"`
	RankedEvidence     []domain.Evidence                      `json:"ranked_evidence,omitempty"`
	StateAssertions    []domain.StateAssertion                `json:"state_assertions,omitempty"`
	DiagnosisRuntime   *CognitiveDiagnosisState               `json:"diagnosis_runtime,omitempty"`
	Features           domain.IncidentFeatures                `json:"incident_features"`
	CandidateLists     map[string][]domain.RetrievalCandidate `json:"candidate_lists,omitempty"`
	Candidates         []domain.RetrievalCandidate            `json:"candidates,omitempty"`
	CausalPatterns     []domain.CausalPattern                 `json:"causal_patterns,omitempty"`
	IncidentGraph      *topology.IncidentGraph                `json:"incident_graph,omitempty"`
	CausalMatches      []causal.PatternMatch                  `json:"causal_matches,omitempty"`
	CausalEvidence     []causal.HypothesisCausalEvidence      `json:"causal_evidence,omitempty"`
	CausalProposal     *causalknowledge.Proposal              `json:"causal_proposal,omitempty"`
	CausalValidation   *causalknowledge.ValidationResult      `json:"causal_validation,omitempty"`
	HypothesisDrafts   []domain.HypothesisDraft               `json:"hypothesis_drafts,omitempty"`
	VerifiedHypotheses []domain.VerifiedHypothesis            `json:"verified_hypotheses,omitempty"`
	DiagnosisLedger    domain.DiagnosisLedger                 `json:"diagnosis_ledger"`
}

const WorkflowName = domain.WorkflowRuntimeName

// GraphMaxSteps bounds graph control transitions, including the two-round
// Active Diagnosis loop. It is not an Agent or Incident wall-clock limit.
const GraphMaxSteps = 32

// CognitiveDiagnosisState is the checkpointable, server-owned state for the
// deterministic diagnosis subgraph. It contains facts and bounded control
// state only; LLM outputs are retained in Investigation as auditable proposals.
type CognitiveDiagnosisState struct {
	Method                    string                       `json:"method"`
	RuleOnly                  bool                         `json:"rule_only"`
	Cognitive                 bool                         `json:"cognitive"`
	Active                    bool                         `json:"active"`
	Round                     int                          `json:"round"`
	MaxRounds                 int                          `json:"max_rounds"`
	Plan                      domain.InvestigationPlan     `json:"plan"`
	PendingRequests           []domain.EvidenceRequest     `json:"pending_requests,omitempty"`
	SeenRequestFingerprints   map[string]bool              `json:"seen_request_fingerprints,omitempty"`
	Evidence                  []domain.Evidence            `json:"evidence,omitempty"`
	Assertions                []domain.StateAssertion      `json:"assertions,omitempty"`
	CausalPatterns            []domain.CausalPattern       `json:"causal_patterns,omitempty"`
	Drafts                    []domain.HypothesisDraft     `json:"drafts,omitempty"`
	Verified                  []domain.VerifiedHypothesis  `json:"verified,omitempty"`
	Arbitration               domain.ArbitrationResult     `json:"arbitration"`
	Investigation             *domain.Investigation        `json:"investigation,omitempty"`
	PendingPolicies           []domain.InvestigationPolicy `json:"pending_policies,omitempty"`
	PolicyBaseline            *policyDecisionSnapshot      `json:"policy_baseline,omitempty"`
	UnresolvedCandidateActive bool                         `json:"unresolved_candidate_active"`
	UnresolvedCandidate       *domain.HypothesisDraft      `json:"unresolved_candidate,omitempty"`
	Completed                 bool                         `json:"completed"`
	StopReason                string                       `json:"stop_reason,omitempty"`
}

// policyDecisionSnapshot contains only deterministic ranking state used to
// audit whether an accepted Investigator request actually changed a decision.
// It is deliberately separate from any model proposal.
type policyDecisionSnapshot struct {
	TopID       string  `json:"top_id"`
	Margin      float64 `json:"margin"`
	Accepted    bool    `json:"accepted"`
	Entropy     float64 `json:"entropy"`
	NewEvidence bool    `json:"new_evidence"`
}

type EvidencePlan struct {
	WindowStart time.Time            `json:"window_start" jsonschema:"required"`
	WindowEnd   time.Time            `json:"window_end" jsonschema:"required"`
	Sources     []EvidencePlanSource `json:"sources" jsonschema:"required,minItems=4"`
}

type EvidencePlanSource struct {
	Source   string `json:"source" jsonschema:"required,enum=metric,enum=log,enum=trace,enum=kubernetes"`
	Service  string `json:"service,omitempty"`
	Resource string `json:"resource,omitempty"`
}

type CorrelationDecision struct {
	Merge      bool    `json:"merge" jsonschema:"required"`
	IncidentID string  `json:"incident_id,omitempty"`
	Confidence float64 `json:"confidence" jsonschema:"required,minimum=0,maximum=1"`
	Reason     string  `json:"reason" jsonschema:"required"`
}

type HypothesisSubmission struct {
	ReasoningType             string                   `json:"reasoning_type" jsonschema:"required,enum=hypothesis_verification"`
	Hypotheses                []domain.HypothesisDraft `json:"hypotheses" jsonschema:"required,minItems=1,maxItems=3"`
	RequestAdditionalEvidence bool                     `json:"request_additional_evidence,omitempty"`
}

type RecoveryDecision struct {
	Action     domain.RecoveryAction `json:"action" jsonschema:"required,enum=restart_pod,enum=scale_deployment,enum=rollback_deployment"`
	Target     string                `json:"target" jsonschema:"required"`
	Parameters map[string]any        `json:"parameters" jsonschema:"required"`
	Reason     string                `json:"reason" jsonschema:"required"`
	Risk       string                `json:"risk" jsonschema:"required"`
	Diff       string                `json:"diff" jsonschema:"required"`
	Rollback   string                `json:"rollback" jsonschema:"required"`
	Confidence float64               `json:"confidence" jsonschema:"required,minimum=0,maximum=1"`
}

type VerificationState struct {
	StartedAt          time.Time `json:"started_at,omitempty"`
	ConsecutiveSuccess int       `json:"consecutive_success"`
	Attempts           int       `json:"attempts"`
}

type graphBuildRequest struct{}

type causalPathRequest struct {
	PatternID    string `json:"pattern_id,omitempty"`
	HypothesisID string `json:"hypothesis_id,omitempty"`
}

type causalPatternProposalRequest struct {
	Cause       string   `json:"cause" jsonschema:"required"`
	Path        []string `json:"causal_path" jsonschema:"required,minItems=2"`
	EvidenceIDs []string `json:"evidence_ids" jsonschema:"required,minItems=2"`
}
type Executor interface {
	Execute(context.Context, *domain.Incident, domain.RecoveryProposal) error
	Verify(context.Context, *domain.Incident) (domain.Verification, error)
}
