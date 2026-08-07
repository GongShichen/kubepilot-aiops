package domain

import "time"

// BrainPhase is the server-owned phase used to resolve mandatory Skills and
// constrain the tool catalogue exposed to one Eino model turn.
type BrainPhase string

const (
	BrainPhaseIntake        BrainPhase = "INTAKE"
	BrainPhasePlanning      BrainPhase = "PLANNING"
	BrainPhaseInvestigation BrainPhase = "INVESTIGATION"
	BrainPhaseReflection    BrainPhase = "REFLECTION"
	BrainPhaseDiagnosis     BrainPhase = "DIAGNOSIS"
	BrainPhaseRecovery      BrainPhase = "RECOVERY"
	BrainPhaseVerification  BrainPhase = "VERIFICATION"
	BrainPhaseEscalation    BrainPhase = "ESCALATION"
)

type BrainToolCategory string

const (
	BrainToolEvidence  BrainToolCategory = "EVIDENCE"
	BrainToolRetrieval BrainToolCategory = "RETRIEVAL"
	BrainToolReasoning BrainToolCategory = "REASONING"
	BrainToolRecovery  BrainToolCategory = "RECOVERY"
	BrainToolControl   BrainToolCategory = "CONTROL"
)

type ToolResultClass string

const (
	ToolResultEvidence    ToolResultClass = "EVIDENCE"
	ToolResultValidation  ToolResultClass = "VALIDATION"
	ToolResultConstraint  ToolResultClass = "CONSTRAINT"
	ToolResultError       ToolResultClass = "ERROR"
	ToolResultStateChange ToolResultClass = "STATE_CHANGE"
)

type ToolResultProvenance struct {
	ToolCallID       string        `json:"tool_call_id"`
	ToolName         string        `json:"tool_name"`
	ToolSchemaHash   string        `json:"tool_schema_hash"`
	Collector        string        `json:"collector,omitempty"`
	TargetRefs       []ResourceRef `json:"target_refs"`
	WindowStart      time.Time     `json:"window_start"`
	WindowEnd        time.Time     `json:"window_end"`
	ObservedAt       time.Time     `json:"observed_at"`
	RawArtifactHash  string        `json:"raw_artifact_hash,omitempty"`
	ParserVersion    string        `json:"parser_version,omitempty"`
	EvidenceIDs      []string      `json:"evidence_ids"`
	StateChangeID    string        `json:"state_change_id,omitempty"`
	ApprovalID       string        `json:"approval_id,omitempty"`
	MutationSpecHash string        `json:"mutation_spec_hash,omitempty"`
	TargetUID        string        `json:"target_uid,omitempty"`
	ResourceVersion  string        `json:"resource_version,omitempty"`
}

type ToolResultRecord struct {
	Class          ToolResultClass      `json:"class"`
	Provenance     ToolResultProvenance `json:"provenance"`
	Status         string               `json:"status"`
	Summary        string               `json:"summary,omitempty"`
	ValidationIDs  []string             `json:"validation_ids,omitempty"`
	NewInformation bool                 `json:"new_information"`
	ConstraintCode string               `json:"constraint_code,omitempty"`
	Infrastructure bool                 `json:"infrastructure_failure,omitempty"`
	OccurredAt     time.Time            `json:"occurred_at"`
}

// BrainEvidenceView is the only evidence projection admitted to LLM context.
// Canonical Facts, Content, Data, and raw collector artifacts remain in the
// Runtime evidence store and are addressable through the provenance links.
type BrainEvidenceView struct {
	ID                    string           `json:"id"`
	Source                string           `json:"source"`
	Kind                  string           `json:"kind"`
	Namespace             string           `json:"namespace,omitempty"`
	Service               string           `json:"service,omitempty"`
	Resource              string           `json:"resource,omitempty"`
	WindowStart           time.Time        `json:"window_start,omitempty"`
	WindowEnd             time.Time        `json:"window_end,omitempty"`
	ObservedAt            time.Time        `json:"observed_at,omitempty"`
	Summary               string           `json:"summary"`
	Signals               []EvidenceSignal `json:"signals,omitempty"`
	CausalNodeIDs         []string         `json:"causal_node_ids,omitempty"`
	ContextRelevance      float64          `json:"context_relevance,omitempty"`
	AnomalyScore          float64          `json:"anomaly_score,omitempty"`
	QualityScore          float64          `json:"quality_score,omitempty"`
	HypothesisRevisionIDs []string         `json:"hypothesis_revision_ids,omitempty"`
	ToolCallIDs           []string         `json:"tool_call_ids,omitempty"`
	RawArtifactHashes     []string         `json:"raw_artifact_hashes,omitempty"`
}

// AssistantTurnRecord is the audit projection of one provider Assistant
// response. Hidden reasoning is represented only by its presence bit; its
// content is never copied into conversation state, checkpoints, or the API.
type AssistantTurnRecord struct {
	TurnID           string    `json:"turn_id"`
	ContentPresent   bool      `json:"content_present"`
	ToolCallPresent  bool      `json:"tool_call_present"`
	ReasoningPresent bool      `json:"reasoning_present"`
	Persisted        bool      `json:"persisted"`
	ObservedAt       time.Time `json:"observed_at"`
}

type GroundingLevel string

const (
	GroundingSupported GroundingLevel = "SUPPORTED"
	GroundingPartial   GroundingLevel = "PARTIAL"
	GroundingRefuted   GroundingLevel = "REFUTED"
	GroundingUnknown   GroundingLevel = "UNKNOWN"
)

type GroundingEvidence struct {
	SupportingEvidenceIDs    []string `json:"supporting_evidence_ids,omitempty"`
	ContradictingEvidenceIDs []string `json:"contradicting_evidence_ids,omitempty"`
	IndependentSourceCount   int      `json:"independent_source_count"`
	EvidenceSupport          float64  `json:"evidence_support"`
	ContradictionRatio       float64  `json:"contradiction_ratio"`
}

type GroundingCoverage struct {
	EvidenceNeedCoverage float64 `json:"evidence_need_coverage"`
	CausalPathCoverage   float64 `json:"causal_path_coverage"`
	TargetScopeCoverage  float64 `json:"target_scope_coverage"`
	TemporalCoverage     float64 `json:"temporal_coverage"`
}

type HypothesisGrounding struct {
	ID                       string            `json:"id"`
	HypothesisRevisionID     string            `json:"hypothesis_revision_id"`
	Level                    GroundingLevel    `json:"level"`
	Evidence                 GroundingEvidence `json:"evidence"`
	Coverage                 GroundingCoverage `json:"coverage"`
	MissingObservations      []string          `json:"missing_observations,omitempty"`
	ValidatedAt              time.Time         `json:"validated_at"`
	EvidenceSnapshotHash     string            `json:"evidence_snapshot_hash"`
	CausalCoverageApplicable bool              `json:"causal_coverage_applicable"`
}

type GroundingDelta struct {
	HypothesisRevisionID        string            `json:"hypothesis_revision_id"`
	EvidenceChange              []string          `json:"evidence_change,omitempty"`
	PreviousLevel               GroundingLevel    `json:"previous_level,omitempty"`
	CurrentLevel                GroundingLevel    `json:"current_level"`
	PreviousCoverage            GroundingCoverage `json:"previous_coverage"`
	CurrentCoverage             GroundingCoverage `json:"current_coverage"`
	NewSupportingEvidenceIDs    []string          `json:"new_supporting_evidence_ids,omitempty"`
	NewContradictingEvidenceIDs []string          `json:"new_contradicting_evidence_ids,omitempty"`
	ConflictDetected            bool              `json:"conflict_detected"`
	SuggestedRevisionNeed       bool              `json:"suggested_revision_need"`
	OccurredAt                  time.Time         `json:"occurred_at"`
}

type BeliefDelta struct {
	HypothesisRevisionID string    `json:"hypothesis_revision_id"`
	PreviousConfidence   float64   `json:"previous_confidence"`
	NewConfidence        float64   `json:"new_confidence"`
	Direction            string    `json:"direction"`
	EvidenceIDs          []string  `json:"evidence_ids,omitempty"`
	ValidationResultIDs  []string  `json:"validation_result_ids,omitempty"`
	RevisionRequired     bool      `json:"revision_required"`
	RevisionReason       string    `json:"revision_reason,omitempty"`
	Committed            bool      `json:"committed"`
	OccurredAt           time.Time `json:"occurred_at"`
}

type ReflectionTrigger string

const (
	ReflectionCriticalEvidence  ReflectionTrigger = "CRITICAL_EVIDENCE"
	ReflectionHypothesisRefuted ReflectionTrigger = "HYPOTHESIS_REFUTED"
	ReflectionCandidateConflict ReflectionTrigger = "CANDIDATE_CONFLICT"
	ReflectionToolFailure       ReflectionTrigger = "TOOL_FAILURE"
	ReflectionConstraintFailure ReflectionTrigger = "CONSTRAINT_FAILURE"
	ReflectionGroundingFailure  ReflectionTrigger = "GROUNDING_FAILURE"
	ReflectionRecoveryFailure   ReflectionTrigger = "RECOVERY_FAILURE"
	ReflectionVerificationFail  ReflectionTrigger = "VERIFICATION_FAILURE"
)

type ReflectionRecord struct {
	ID                    string            `json:"id"`
	Trigger               ReflectionTrigger `json:"trigger"`
	TriggerToolCallID     string            `json:"trigger_tool_call_id,omitempty"`
	HypothesisRevisionIDs []string          `json:"hypothesis_revision_ids,omitempty"`
	EvidenceIDs           []string          `json:"evidence_ids,omitempty"`
	BeliefDeltas          []BeliefDelta     `json:"belief_deltas,omitempty"`
	NextGoal              string            `json:"next_goal,omitempty"`
	CostUnits             int               `json:"cost_units"`
	Accepted              bool              `json:"accepted"`
	RejectedReason        string            `json:"rejected_reason,omitempty"`
	OccurredAt            time.Time         `json:"occurred_at"`
}

type ExecutionSnapshot struct {
	SkillSnapshotHash string `json:"skill_snapshot_hash"`
	ModelConfigHash   string `json:"model_config_hash"`
	ToolSchemaHash    string `json:"tool_schema_hash"`
	PolicyHash        string `json:"policy_hash"`
}

type WorkflowAttemptStatus string

const (
	WorkflowAttemptActive      WorkflowAttemptStatus = "ACTIVE"
	WorkflowAttemptInterrupted WorkflowAttemptStatus = "INTERRUPTED"
	WorkflowAttemptCompleted   WorkflowAttemptStatus = "COMPLETED"
	WorkflowAttemptInvalidated WorkflowAttemptStatus = "INVALIDATED"
)

// WorkflowAttempt freezes every executable dependency for one checkpoint
// lineage. Explicit migration creates a new attempt; it never mutates the
// snapshot associated with an existing diagnosis or recovery authorization.
type WorkflowAttempt struct {
	ID                     string                `json:"id"`
	IncidentID             string                `json:"incident_id"`
	Sequence               int                   `json:"sequence"`
	CheckpointID           string                `json:"checkpoint_id"`
	Status                 WorkflowAttemptStatus `json:"status"`
	ExecutionSnapshot      ExecutionSnapshot     `json:"execution_snapshot"`
	EvidenceSnapshotHash   string                `json:"evidence_snapshot_hash,omitempty"`
	MigratedFromAttemptID  string                `json:"migrated_from_attempt_id,omitempty"`
	InvalidatedArtifactIDs []string              `json:"invalidated_artifact_ids,omitempty"`
	StartedAt              time.Time             `json:"started_at"`
	InterruptedAt          time.Time             `json:"interrupted_at,omitempty"`
	CompletedAt            time.Time             `json:"completed_at,omitempty"`
}

type SkillRef struct {
	ID          string `json:"id"`
	Version     string `json:"version"`
	ContentHash string `json:"content_hash"`
}

type SkillActivation struct {
	SkillID        string     `json:"skill_id"`
	Version        string     `json:"version"`
	ContentHash    string     `json:"content_hash"`
	Phase          BrainPhase `json:"phase"`
	Reason         string     `json:"reason"`
	Trigger        string     `json:"trigger"`
	RequestedBy    string     `json:"requested_by"`
	RequestedTurn  string     `json:"requested_turn,omitempty"`
	Status         string     `json:"status"`
	RejectedReason string     `json:"rejected_reason,omitempty"`
	ActivatedAt    time.Time  `json:"activated_at"`
}

type HypothesisRelation string

const (
	HypothesisRoot    HypothesisRelation = "ROOT"
	HypothesisRefine  HypothesisRelation = "REFINE"
	HypothesisReplace HypothesisRelation = "REPLACE"
	HypothesisSplit   HypothesisRelation = "SPLIT"
	HypothesisMerge   HypothesisRelation = "MERGE"
)

type AdmissionGroundingLevel string

const (
	AdmissionDirect     AdmissionGroundingLevel = "DIRECT"
	AdmissionIndirect   AdmissionGroundingLevel = "INDIRECT"
	AdmissionUnresolved AdmissionGroundingLevel = "UNRESOLVED"
	AdmissionRejected   AdmissionGroundingLevel = "REJECTED"
)

type ResourceScopeDecision struct {
	Requested ResourceRef `json:"requested"`
	Resolved  ResourceRef `json:"resolved,omitempty"`
	Allowed   bool        `json:"allowed"`
	Reason    string      `json:"reason"`
}

type HypothesisAdmission struct {
	HypothesisRevisionID  string                  `json:"hypothesis_revision_id"`
	Decision              string                  `json:"decision"`
	GroundingLevel        AdmissionGroundingLevel `json:"grounding_level"`
	ReasonCodes           []string                `json:"reason_codes,omitempty"`
	AllowedToolCategories []BrainToolCategory     `json:"allowed_tool_categories,omitempty"`
	ResourceScope         []ResourceScopeDecision `json:"resource_scope,omitempty"`
	OccurredAt            time.Time               `json:"occurred_at"`
}

type AgentHypothesis struct {
	ID                        string             `json:"id"`
	LineageID                 string             `json:"lineage_id"`
	Version                   int                `json:"version"`
	ParentIDs                 []string           `json:"parent_ids,omitempty"`
	Relation                  HypothesisRelation `json:"relation"`
	RevisionReason            string             `json:"revision_reason,omitempty"`
	Statement                 string             `json:"statement"`
	Category                  string             `json:"category"`
	Mechanism                 string             `json:"mechanism"`
	TargetRefs                []ResourceRef      `json:"target_refs"`
	EvidenceNeeds             []string           `json:"evidence_needs"`
	FalsificationConditions   []string           `json:"falsification_conditions"`
	ModelConfidence           float64            `json:"model_confidence"`
	Status                    HypothesisStatus   `json:"status"`
	LastValidatedAt           time.Time          `json:"last_validated_at,omitempty"`
	LastValidatedSnapshotHash string             `json:"last_validated_snapshot_hash,omitempty"`
	CreatedByTurn             string             `json:"created_by_turn"`
	CreatedAt                 time.Time          `json:"created_at"`
}

type AgentActionIntent struct {
	Intent              string        `json:"intent"`
	TargetScope         []ResourceRef `json:"target_scope,omitempty"`
	HypothesisIDs       []string      `json:"hypothesis_ids,omitempty"`
	EvidenceNeed        []string      `json:"evidence_need,omitempty"`
	ExpectedObservation []string      `json:"expected_observation,omitempty"`
}

type AgentActionEnvelope struct {
	ActionID             string            `json:"action_id"`
	IncidentID           string            `json:"incident_id"`
	TurnID               string            `json:"turn_id"`
	Phase                BrainPhase        `json:"phase"`
	ToolName             string            `json:"tool_name"`
	ToolCategory         BrainToolCategory `json:"tool_category"`
	SkillRefs            []SkillRef        `json:"skill_refs,omitempty"`
	EvidenceSnapshotHash string            `json:"evidence_snapshot_hash"`
	IdempotencyKey       string            `json:"idempotency_key"`
	Intent               AgentActionIntent `json:"intent"`
}

type BrainToolExecution struct {
	Envelope AgentActionEnvelope `json:"envelope"`
	Result   ToolResultRecord    `json:"result"`
}

// ToolPolicyDecision is an authorization decision about one requested tool
// call. It is deliberately not evidence about the incident.
type ToolPolicyDecision struct {
	Allowed     bool     `json:"allowed"`
	ReasonCodes []string `json:"reason_codes,omitempty"`
	Fingerprint string   `json:"fingerprint"`
}

type BrainTurn struct {
	ID           string            `json:"id"`
	Sequence     int               `json:"sequence"`
	Phase        BrainPhase        `json:"phase"`
	SkillRefs    []SkillRef        `json:"skill_refs,omitempty"`
	ToolCategory BrainToolCategory `json:"tool_category,omitempty"`
	ModelUsage   *ModelUsageEvent  `json:"model_usage,omitempty"`
	StartedAt    time.Time         `json:"started_at"`
	CompletedAt  time.Time         `json:"completed_at,omitempty"`
}

type IncidentUnderstanding struct {
	Summary         string        `json:"summary"`
	AffectedTargets []ResourceRef `json:"affected_targets,omitempty"`
	PossibleDomains []string      `json:"possible_domains,omitempty"`
	Unknowns        []string      `json:"unknowns,omitempty"`
	SubmittedAt     time.Time     `json:"submitted_at"`
}

type BrainBudget struct {
	MaxTurns                 int `json:"max_turns"`
	MaxActiveHypotheses      int `json:"max_active_hypotheses"`
	MaxHypothesisBranches    int `json:"max_hypothesis_branches"`
	MaxRevisionsPerLineage   int `json:"max_revisions_per_lineage"`
	MaxToolCalls             int `json:"max_tool_calls"`
	MaxParallelReadTools     int `json:"max_parallel_read_tools"`
	MaxOptionalSkillsPerTurn int `json:"max_optional_skills_per_turn"`
	MaxStructuredCorrections int `json:"max_structured_corrections"`
	MaxReflectionCostUnits   int `json:"max_reflection_cost_units"`
}

type BrainBudgetUsage struct {
	Turns                 int `json:"turns"`
	ActiveHypotheses      int `json:"active_hypotheses"`
	HypothesisBranches    int `json:"hypothesis_branches"`
	ToolCalls             int `json:"tool_calls"`
	StructuredCorrections int `json:"structured_corrections"`
	ReflectionCostUnits   int `json:"reflection_cost_units"`
}

type BrainBudgetState struct {
	Limits             BrainBudget      `json:"limits"`
	Usage              BrainBudgetUsage `json:"usage"`
	ToolCallsExhausted bool             `json:"tool_calls_exhausted,omitempty"`
}

type ToolCallingPolicy struct {
	MaxSameToolRepeat                      int  `json:"max_same_tool_repeat" yaml:"max_same_tool_repeat"`
	MaxNoInformationStreak                 int  `json:"max_no_information_streak" yaml:"max_no_information_streak"`
	RequireReason                          bool `json:"require_reason" yaml:"require_reason"`
	RequireExpectedObservation             bool `json:"require_expected_observation" yaml:"require_expected_observation"`
	RequireHypothesisBindingAfterAdmission bool `json:"require_hypothesis_binding_after_admission" yaml:"require_hypothesis_binding_after_admission"`
	RejectExactRequestRepeat               bool `json:"reject_exact_request_repeat" yaml:"reject_exact_request_repeat"`
	RejectUnchangedConstraintRetry         bool `json:"reject_unchanged_constraint_retry" yaml:"reject_unchanged_constraint_retry"`
}

type RecoveryOption struct {
	Action     RecoveryAction `json:"action"`
	Target     string         `json:"target"`
	Parameters map[string]any `json:"parameters,omitempty"`
	Reason     string         `json:"reason"`
}

type AgentRecoveryPlan struct {
	ID                   string            `json:"id"`
	Goal                 string            `json:"goal"`
	PrimaryAction        RecoveryOption    `json:"primary_action"`
	Alternatives         []RecoveryOption  `json:"alternatives,omitempty"`
	ExpectedOutcome      string            `json:"expected_outcome"`
	RollbackPlan         string            `json:"rollback_plan"`
	VerificationPlan     string            `json:"verification_plan"`
	RiskReason           string            `json:"risk_reason"`
	DiagnosisVersion     string            `json:"diagnosis_version"`
	EvidenceSnapshotHash string            `json:"evidence_snapshot_hash"`
	ExecutionSnapshot    ExecutionSnapshot `json:"execution_snapshot"`
}

type AgentDiagnosis struct {
	ID                   string            `json:"id"`
	HypothesisRevisionID string            `json:"hypothesis_revision_id"`
	Statement            string            `json:"statement"`
	Category             string            `json:"category"`
	Mechanism            string            `json:"mechanism"`
	TargetRefs           []ResourceRef     `json:"target_refs"`
	ModelConfidence      float64           `json:"model_confidence"`
	EvidenceIDs          []string          `json:"evidence_ids"`
	ValidationResultIDs  []string          `json:"validation_result_ids"`
	EvidenceSnapshotHash string            `json:"evidence_snapshot_hash"`
	ExecutionSnapshot    ExecutionSnapshot `json:"execution_snapshot"`
	GroundingLevel       GroundingLevel    `json:"grounding_level"`
	Provisional          bool              `json:"provisional"`
	SubmittedAt          time.Time         `json:"submitted_at"`
}

type TerminationReason string

const (
	TerminationDiagnosisConfident      TerminationReason = "DIAGNOSIS_CONFIDENT"
	TerminationDiagnosisProvisional    TerminationReason = "DIAGNOSIS_PROVISIONAL"
	TerminationEvidenceSaturated       TerminationReason = "EVIDENCE_SATURATED"
	TerminationBudgetExhausted         TerminationReason = "BUDGET_EXHAUSTED"
	TerminationHumanEscalation         TerminationReason = "HUMAN_ESCALATION"
	TerminationSafetyBlocked           TerminationReason = "SAFETY_BLOCKED"
	TerminationApprovalRejected        TerminationReason = "APPROVAL_REJECTED"
	TerminationRecoverySucceeded       TerminationReason = "RECOVERY_SUCCEEDED"
	TerminationRecoveryFailed          TerminationReason = "RECOVERY_FAILED"
	TerminationExecutionOutcomeUnknown TerminationReason = "EXECUTION_OUTCOME_UNKNOWN"
	TerminationFatalInfrastructure     TerminationReason = "FATAL_INFRASTRUCTURE_ERROR"
)

type TerminationEvent struct {
	Reason                    TerminationReason  `json:"reason"`
	TriggerTurnID             string             `json:"trigger_turn_id,omitempty"`
	FinalHypothesisRevisionID string             `json:"final_hypothesis_revision_id,omitempty"`
	EvidenceSnapshotHash      string             `json:"evidence_snapshot_hash,omitempty"`
	ExecutionSnapshot         *ExecutionSnapshot `json:"execution_snapshot,omitempty"`
	UnresolvedGaps            []string           `json:"unresolved_gaps,omitempty"`
	RemainingBudget           BrainBudgetUsage   `json:"remaining_budget"`
	OccurredAt                time.Time          `json:"occurred_at"`
}

// RecoveryEligibility is the Safety Kernel's deterministic decision about a
// model-proposed Recovery Plan. It never changes the model Diagnosis.
type RecoveryEligibility struct {
	Allowed     bool     `json:"allowed"`
	ReasonCodes []string `json:"reason_codes,omitempty"`
}
