package domain

import "time"

type IncidentStatus string

const (
	StatusReceived         IncidentStatus = "RECEIVED"
	StatusCorrelating      IncidentStatus = "CORRELATING"
	StatusCollecting       IncidentStatus = "COLLECTING"
	StatusDiagnosing       IncidentStatus = "DIAGNOSING"
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
	RootCauseEvidenceIDs []string          `json:"root_cause_evidence_ids,omitempty"`
	Confidence           float64           `json:"confidence,omitempty"`
	TraceID              string            `json:"trace_id,omitempty"`
	CreatedAt            time.Time         `json:"created_at"`
	UpdatedAt            time.Time         `json:"updated_at"`
	Alerts               []Alert           `json:"alerts,omitempty"`
	Evidence             []Evidence        `json:"evidence,omitempty"`
	Hypotheses           []Hypothesis      `json:"hypotheses,omitempty"`
	Proposal             *RecoveryProposal `json:"recovery_proposal,omitempty"`
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

type Evidence struct {
	ID         string         `json:"id"`
	Source     string         `json:"source"`
	Kind       string         `json:"kind"`
	Summary    string         `json:"summary"`
	Data       map[string]any `json:"data,omitempty"`
	ObservedAt time.Time      `json:"observed_at"`
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
	StatusReceived:         {StatusCorrelating: true, StatusCancelled: true},
	StatusCorrelating:      {StatusCollecting: true, StatusCancelled: true},
	StatusCollecting:       {StatusDiagnosing: true, StatusNeedsAttention: true, StatusCancelled: true},
	StatusDiagnosing:       {StatusAwaitingApproval: true, StatusNeedsAttention: true, StatusCancelled: true},
	StatusAwaitingApproval: {StatusRecovering: true, StatusRejected: true, StatusCancelled: true},
	StatusRecovering:       {StatusVerifying: true, StatusRecoveryFailed: true},
	StatusVerifying:        {StatusResolved: true, StatusRecoveryFailed: true},
}

func CanTransition(from, to IncidentStatus) bool { return transitions[from][to] }
