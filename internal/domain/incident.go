package domain

import (
	"fmt"
	"time"
)

type IncidentStatus string

const (
	StatusReceived         IncidentStatus = "RECEIVED"
	StatusCorrelating      IncidentStatus = "CORRELATING"
	StatusCollecting       IncidentStatus = "COLLECTING"
	StatusDiagnosing       IncidentStatus = "DIAGNOSING"
	StatusProposing        IncidentStatus = "PROPOSING"
	StatusAwaitingApproval IncidentStatus = "AWAITING_APPROVAL"
	StatusRecovering       IncidentStatus = "RECOVERING"
	StatusVerifying        IncidentStatus = "VERIFYING"
	StatusResolved         IncidentStatus = "RESOLVED"
	StatusNeedsAttention   IncidentStatus = "NEEDS_ATTENTION"
	StatusRejected         IncidentStatus = "REJECTED"
	StatusRecoveryFailed   IncidentStatus = "RECOVERY_FAILED"
	StatusCancelled        IncidentStatus = "CANCELLED"
)

type Incident struct {
	ID                   string            `json:"id"`
	Status               IncidentStatus    `json:"status"`
	Severity             string            `json:"severity"`
	Service              string            `json:"service"`
	Namespace            string            `json:"namespace"`
	Resource             string            `json:"resource"`
	Summary              string            `json:"summary"`
	DiagnosisMethod      string            `json:"diagnosis_method,omitempty"`
	DiagnosisError       string            `json:"diagnosis_error,omitempty"`
	EvidenceStartAt      time.Time         `json:"evidence_start_at,omitempty"`
	RootCause            string            `json:"root_cause,omitempty"`
	RootCauseCategory    string            `json:"root_cause_category,omitempty"`
	RootCauseVariant     string            `json:"root_cause_variant,omitempty"`
	RootCauseService     string            `json:"root_cause_service,omitempty"`
	RootCauseResource    string            `json:"root_cause_resource,omitempty"`
	RootCauseEvidenceIDs []string          `json:"root_cause_evidence_ids,omitempty"`
	Confidence           float64           `json:"confidence,omitempty"`
	ReasoningType        string            `json:"reasoning_type,omitempty"`
	ModelProtocol        string            `json:"model_protocol,omitempty"`
	ModelName            string            `json:"model_name,omitempty"`
	ModelConfigHash      string            `json:"model_config_hash,omitempty"`
	TraceID              string            `json:"trace_id,omitempty"`
	CreatedAt            time.Time         `json:"created_at"`
	UpdatedAt            time.Time         `json:"updated_at"`
	Alerts               []Alert           `json:"alerts,omitempty"`
	Evidence             []Evidence        `json:"evidence,omitempty"`
	Hypotheses           []Hypothesis      `json:"hypotheses,omitempty"`
	Proposal             *RecoveryProposal `json:"recovery_proposal,omitempty"`
	DryRun               *DryRunResult     `json:"dry_run,omitempty"`
	ExecutionContext     *ExecutionContext `json:"execution_context,omitempty"`
	WorkflowInterruptID  string            `json:"workflow_interrupt_id,omitempty"`
	Verification         *Verification     `json:"verification,omitempty"`
}

const (
	DiagnosisMethodLLMOnly   = "llm-only"
	DiagnosisMethodVectorRAG = "vector-rag"
	DiagnosisMethodKubePilot = "kubepilot"
)

func ValidDiagnosisMethod(value string) bool {
	return value == "" || value == DiagnosisMethodLLMOnly || value == DiagnosisMethodVectorRAG || value == DiagnosisMethodKubePilot
}

type Alert struct {
	Fingerprint string            `json:"fingerprint"`
	Name        string            `json:"name"`
	Status      string            `json:"status"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	StartsAt    time.Time         `json:"starts_at"`
	EndsAt      time.Time         `json:"ends_at,omitempty"`
}

type EvidenceSource = string
type EvidenceType = string

type Evidence struct {
	ID          string         `json:"id"`
	Source      EvidenceSource `json:"source"`
	Type        EvidenceType   `json:"type,omitempty"`
	Kind        string         `json:"kind,omitempty"` // backward-compatible alias for Type
	Timestamp   time.Time      `json:"timestamp,omitempty"`
	WindowStart time.Time      `json:"window_start,omitempty"`
	WindowEnd   time.Time      `json:"window_end,omitempty"`
	Namespace   string         `json:"namespace,omitempty"`
	Service     string         `json:"service,omitempty"`
	Resource    string         `json:"resource,omitempty"`
	Content     map[string]any `json:"content,omitempty"`
	Data        map[string]any `json:"data,omitempty"` // backward-compatible alias for Content
	Summary     string         `json:"summary"`
	Confidence  float64        `json:"confidence,omitempty"`
	TraceID     string         `json:"trace_id,omitempty"`
	TemplateID  string         `json:"template_id,omitempty"`
	CollectedAt time.Time      `json:"collected_at,omitempty"`
	ObservedAt  time.Time      `json:"observed_at,omitempty"` // backward-compatible alias for Timestamp
}

type Hypothesis struct {
	ID                      string   `json:"id"`
	Cause                   string   `json:"cause"`
	Probability             float64  `json:"probability"`
	SupportingEvidence      []string `json:"supporting_evidence"`
	ContradictingEvidence   []string `json:"contradicting_evidence,omitempty"`
	FalsificationConditions []string `json:"falsification_conditions,omitempty"`
}

type RecoveryAction string

const (
	ActionRestartPod         RecoveryAction = "restart_pod"
	ActionScaleDeployment    RecoveryAction = "scale_deployment"
	ActionRollbackDeployment RecoveryAction = "rollback_deployment"
)

type RecoveryProposal struct {
	ID              string         `json:"id"`
	Action          RecoveryAction `json:"action"`
	Namespace       string         `json:"namespace"`
	Target          string         `json:"target"`
	TargetUID       string         `json:"target_uid"`
	ResourceVersion string         `json:"resource_version"`
	Parameters      map[string]any `json:"parameters,omitempty"`
	Reason          string         `json:"reason"`
	Risk            string         `json:"risk"`
	Diff            string         `json:"diff"`
	Rollback        string         `json:"rollback"`
	Confidence      float64        `json:"confidence"`
	ExpiresAt       time.Time      `json:"expires_at"`
}

type DryRunResult struct {
	Success          bool           `json:"success"`
	Action           RecoveryAction `json:"action"`
	Target           string         `json:"target"`
	Before           map[string]any `json:"before,omitempty"`
	After            map[string]any `json:"after,omitempty"`
	Risks            []string       `json:"risks,omitempty"`
	MutationSpecHash string         `json:"mutation_spec_hash"`
	ValidatedAt      time.Time      `json:"validated_at"`
	Error            string         `json:"error,omitempty"`
}

type ExecutionContext struct {
	NamespaceAllowlist []string  `json:"namespace_allowlist"`
	IncidentID         string    `json:"incident_id"`
	ProposalID         string    `json:"proposal_id"`
	ApprovalID         string    `json:"approval_id"`
	IdempotencyKey     string    `json:"idempotency_key"`
	Operator           string    `json:"operator"`
	TargetUID          string    `json:"target_uid"`
	ResourceVersion    string    `json:"resource_version"`
	MutationSpecHash   string    `json:"mutation_spec_hash"`
	ApprovedAt         time.Time `json:"approved_at"`
	ExpiresAt          time.Time `json:"expires_at"`
}

type Verification struct {
	Success     bool            `json:"success"`
	Checks      map[string]bool `json:"checks"`
	Message     string          `json:"message"`
	CompletedAt time.Time       `json:"completed_at"`
}

type AuditEvent struct {
	ID         string         `json:"id"`
	IncidentID string         `json:"incident_id"`
	Type       string         `json:"type"`
	Message    string         `json:"message"`
	Data       map[string]any `json:"data,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
}

var transitions = map[IncidentStatus]map[IncidentStatus]bool{
	StatusReceived:         {StatusCorrelating: true, StatusNeedsAttention: true, StatusCancelled: true},
	StatusCorrelating:      {StatusCollecting: true, StatusNeedsAttention: true, StatusCancelled: true},
	StatusCollecting:       {StatusDiagnosing: true, StatusNeedsAttention: true, StatusCancelled: true},
	StatusDiagnosing:       {StatusCollecting: true, StatusProposing: true, StatusNeedsAttention: true, StatusCancelled: true},
	StatusProposing:        {StatusAwaitingApproval: true, StatusNeedsAttention: true, StatusCancelled: true},
	StatusAwaitingApproval: {StatusRecovering: true, StatusNeedsAttention: true, StatusRejected: true, StatusCancelled: true},
	StatusRecovering:       {StatusVerifying: true, StatusNeedsAttention: true, StatusRecoveryFailed: true},
	StatusVerifying:        {StatusResolved: true, StatusNeedsAttention: true, StatusRecoveryFailed: true},
	StatusNeedsAttention:   {StatusReceived: true},
	StatusRecoveryFailed:   {StatusReceived: true},
}

func CanTransition(from, to IncidentStatus) bool { return transitions[from][to] }

func Transition(in *Incident, to IncidentStatus) error {
	if in == nil {
		return fmt.Errorf("incident is required")
	}
	if in.Status == to {
		return nil
	}
	if !CanTransition(in.Status, to) {
		return fmt.Errorf("invalid incident transition %s -> %s", in.Status, to)
	}
	in.Status = to
	in.UpdatedAt = time.Now().UTC()
	return nil
}
