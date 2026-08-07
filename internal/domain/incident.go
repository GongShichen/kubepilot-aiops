package domain

import (
	"encoding/json"
	"fmt"
	"time"
)

type IncidentStatus string
type DiagnosisMethod = string

const WorkflowRuntimeName = "eino-cognitive-diagnosis-runtime"

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
	ID                   string             `json:"id"`
	Status               IncidentStatus     `json:"status"`
	Severity             string             `json:"severity"`
	Service              string             `json:"service"`
	Cluster              string             `json:"cluster,omitempty"`
	Namespace            string             `json:"namespace"`
	Resource             string             `json:"resource"`
	Summary              string             `json:"summary"`
	DiagnosisMethod      string             `json:"diagnosis_method,omitempty"`
	CausalMode           string             `json:"causal_mode,omitempty"`
	DiagnosisError       string             `json:"diagnosis_error,omitempty"`
	EvidenceStartAt      time.Time          `json:"evidence_start_at,omitempty"`
	RootCause            string             `json:"root_cause,omitempty"`
	RootCauseCategory    string             `json:"root_cause_category,omitempty"`
	RootCauseVariant     string             `json:"root_cause_variant,omitempty"`
	RootCauseService     string             `json:"root_cause_service,omitempty"`
	RootCauseResource    string             `json:"root_cause_resource,omitempty"`
	RootCauseEvidenceIDs []string           `json:"root_cause_evidence_ids,omitempty"`
	Confidence           float64            `json:"confidence,omitempty"`
	ReasoningType        string             `json:"reasoning_type,omitempty"`
	ModelProtocol        string             `json:"model_protocol,omitempty"`
	ModelName            string             `json:"model_name,omitempty"`
	ModelConfigHash      string             `json:"model_config_hash,omitempty"`
	SkillSnapshotHash    string             `json:"skill_snapshot_hash,omitempty"`
	ExecutionSnapshot    *ExecutionSnapshot `json:"execution_snapshot,omitempty"`
	WorkflowAttempt      *WorkflowAttempt   `json:"workflow_attempt,omitempty"`
	RankingPolicyHash    string             `json:"ranking_policy_hash,omitempty"`
	RerankerConfigHash   string             `json:"reranker_config_hash,omitempty"`
	TraceID              string             `json:"trace_id,omitempty"`
	CreatedAt            time.Time          `json:"created_at"`
	UpdatedAt            time.Time          `json:"updated_at"`
	Alerts               []Alert            `json:"alerts,omitempty"`
	Evidence             []Evidence         `json:"evidence,omitempty"`
	Hypotheses           []Hypothesis       `json:"hypotheses,omitempty"`
	Proposal             *RecoveryProposal  `json:"recovery_proposal,omitempty"`
	DryRun               *DryRunResult      `json:"dry_run,omitempty"`
	ExecutionContext     *ExecutionContext  `json:"execution_context,omitempty"`
	RecoveryExecution    *RecoveryExecution `json:"recovery_execution,omitempty"`
	WorkflowInterruptID  string             `json:"workflow_interrupt_id,omitempty"`
	Verification         *Verification      `json:"verification,omitempty"`
	DiagnosisLedger      *DiagnosisLedger   `json:"diagnosis_ledger,omitempty"`
	AgentBudget          *AgentBudgetState  `json:"agent_budget,omitempty"`
	Investigation        *Investigation     `json:"investigation,omitempty"`
}

const (
	DiagnosisMethodDirect                    = "direct"
	DiagnosisMethodRAG                       = "rag"
	DiagnosisMethodReAct                     = "react"
	DiagnosisMethodRuleOnly                  = "rule-only"
	DiagnosisMethodEvidence                  = "evidence-only"
	DiagnosisMethodCognitive                 = "cognitive"
	DiagnosisMethodActive                    = "active-diagnosis"
	DiagnosisMethodKubePilot                 = "kubepilot"
	DiagnosisMethodKubePilotNoReflection     = "kubepilot-no-reflection"
	DiagnosisMethodKubePilotNoOptionalSkills = "kubepilot-no-optional-skills"
	// Deprecated request aliases are accepted at the API boundary only. All
	// persisted state and benchmark artifacts use the canonical identifiers.
	DiagnosisMethodLLMOnly   = "llm-only"
	DiagnosisMethodVectorRAG = "vector-rag"
)

const (
	CausalModeNone    = "no-causal"
	CausalModeStatic  = "static-causal"
	CausalModeLearned = "learned-causal"
	CausalModeFull    = "full"
)

func NormalizeCausalMode(value string) (string, bool) {
	switch value {
	case "", CausalModeFull:
		return CausalModeFull, true
	case CausalModeNone, CausalModeStatic, CausalModeLearned:
		return value, true
	default:
		return "", false
	}
}

func ValidDiagnosisMethod(value string) bool {
	_, ok := NormalizeDiagnosisMethod(value)
	return ok
}

func NormalizeDiagnosisMethod(value string) (string, bool) {
	switch value {
	case "":
		return DiagnosisMethodKubePilot, true
	case DiagnosisMethodLLMOnly:
		return DiagnosisMethodDirect, true
	case DiagnosisMethodVectorRAG:
		return DiagnosisMethodRAG, true
	case DiagnosisMethodDirect, DiagnosisMethodRAG, DiagnosisMethodReAct, DiagnosisMethodRuleOnly, DiagnosisMethodEvidence, DiagnosisMethodCognitive, DiagnosisMethodActive, DiagnosisMethodKubePilot, DiagnosisMethodKubePilotNoReflection, DiagnosisMethodKubePilotNoOptionalSkills:
		return value, true
	default:
		return "", false
	}
}

func IsKubePilotBrainMethod(value string) bool {
	normalized, ok := NormalizeDiagnosisMethod(value)
	return ok && (normalized == DiagnosisMethodKubePilot || normalized == DiagnosisMethodKubePilotNoReflection || normalized == DiagnosisMethodKubePilotNoOptionalSkills)
}

type Investigation struct {
	Architecture          string                      `json:"architecture"`
	Plan                  InvestigationPlan           `json:"plan"`
	Findings              []WorkerFinding             `json:"findings,omitempty"`
	Debate                []DebateRound               `json:"debate,omitempty"`
	Candidates            []HypothesisDraft           `json:"candidates,omitempty"`
	Verified              []VerifiedHypothesis        `json:"verified_hypotheses,omitempty"`
	Signals               []EvidenceSignal            `json:"signals,omitempty"`
	Assertions            []StateAssertion            `json:"state_assertions,omitempty"`
	CognitiveReasoning    []CognitiveReasoning        `json:"cognitive_reasoning,omitempty"`
	Falsification         []FalsificationResult       `json:"falsification,omitempty"`
	Pairwise              []PairwiseFalsification     `json:"pairwise_falsification,omitempty"`
	ExpansionRequests     []CandidateExpansionRequest `json:"candidate_expansion_requests,omitempty"`
	Arbitration           *ArbitrationResult          `json:"arbitration,omitempty"`
	RecoveryPermission    *RecoveryPermission         `json:"recovery_permission,omitempty"`
	MemoryReads           []MemoryAccessEvent         `json:"memory_reads,omitempty"`
	ModelUsage            []ModelUsageEvent           `json:"model_usage,omitempty"`
	BrainTurns            []BrainTurn                 `json:"brain_turns,omitempty"`
	AssistantTurns        []AssistantTurnRecord       `json:"assistant_turns,omitempty"`
	IncidentUnderstanding *IncidentUnderstanding      `json:"incident_understanding,omitempty"`
	SkillActivations      []SkillActivation           `json:"skill_activations,omitempty"`
	ToolExecutions        []BrainToolExecution        `json:"tool_executions,omitempty"`
	AgentHypotheses       []AgentHypothesis           `json:"agent_hypotheses,omitempty"`
	HypothesisAdmissions  []HypothesisAdmission       `json:"hypothesis_admissions,omitempty"`
	HypothesisGroundings  []HypothesisGrounding       `json:"hypothesis_groundings,omitempty"`
	GroundingDeltas       []GroundingDelta            `json:"grounding_deltas,omitempty"`
	BeliefDeltas          []BeliefDelta               `json:"belief_deltas,omitempty"`
	Reflections           []ReflectionRecord          `json:"reflections,omitempty"`
	AgentDiagnosis        *AgentDiagnosis             `json:"agent_diagnosis,omitempty"`
	AgentRecoveryPlan     *AgentRecoveryPlan          `json:"agent_recovery_plan,omitempty"`
	Termination           *TerminationEvent           `json:"termination,omitempty"`
	BrainBudget           *BrainBudgetState           `json:"brain_budget,omitempty"`
	ExecutionSnapshot     *ExecutionSnapshot          `json:"execution_snapshot,omitempty"`
	WorkflowAttempt       *WorkflowAttempt            `json:"workflow_attempt,omitempty"`
	DiagnosisRounds       int                         `json:"diagnosis_rounds,omitempty"`
	StartedAt             time.Time                   `json:"started_at"`
	CompletedAt           time.Time                   `json:"completed_at,omitempty"`
}

// StateAssertion is a server-owned statement about the live incident state.
// Signals are raw, typed measurements; assertions are the stable diagnostic
// facts that candidates, causal reasoning and arbitration consume.
type StateAssertion struct {
	ID                     string               `json:"id"`
	Subject                string               `json:"subject"`
	Property               string               `json:"property"`
	State                  string               `json:"state"`
	Confidence             float64              `json:"confidence"`
	SupportingSignalIDs    []string             `json:"supporting_signal_ids,omitempty"`
	ContradictingSignalIDs []string             `json:"contradicting_signal_ids,omitempty"`
	FirstSeen              time.Time            `json:"first_seen"`
	LastSeen               time.Time            `json:"last_seen"`
	Status                 StateAssertionStatus `json:"status"`
}

type StateAssertionStatus string

const (
	StateAssertionActive       StateAssertionStatus = "ACTIVE"
	StateAssertionResolved     StateAssertionStatus = "RESOLVED"
	StateAssertionStale        StateAssertionStatus = "STALE"
	StateAssertionContradicted StateAssertionStatus = "CONTRADICTED"
)

// CognitiveReasoning is an auditable, structured LLM proposal. It cannot
// modify evidence, objective scores, gates, or recovery permissions.
type CognitiveReasoning struct {
	Round                  int                        `json:"round"`
	Intent                 *InvestigationIntent       `json:"intent,omitempty"`
	Interpretations        []CognitiveInterpretation  `json:"interpretations,omitempty"`
	TieBreakingPreferences []TieBreakingPreference    `json:"tie_breaking_preferences,omitempty"`
	InvestigationPolicies  []InvestigationPolicy      `json:"investigation_policies,omitempty"`
	Counterarguments       []CognitiveCounterargument `json:"counterarguments,omitempty"`
	Accepted               bool                       `json:"accepted"`
	RejectedReasons        []string                   `json:"rejected_reasons,omitempty"`
	OccurredAt             time.Time                  `json:"occurred_at"`
}

type InvestigationIntent struct {
	Focus      []string `json:"focus,omitempty"`
	Priorities []string `json:"priorities,omitempty"`
}

type CognitiveInterpretation struct {
	CandidateIDs           []string `json:"candidate_ids,omitempty"`
	MechanismLabels        []string `json:"mechanism_labels,omitempty"`
	SupportingAssertionIDs []string `json:"supporting_assertion_ids,omitempty"`
	ReasoningPredicates    []string `json:"reasoning_predicates,omitempty"`
	RequiredObservations   []string `json:"required_observations,omitempty"`
}

// TieBreakingPreference is deliberately ordinal: it never mutates an
// objective score and is only usable among server-defined near ties.
type TieBreakingPreference struct {
	PreferredCandidateID string   `json:"preferred_candidate_id"`
	OtherCandidateID     string   `json:"other_candidate_id"`
	AssertionIDs         []string `json:"assertion_ids,omitempty"`
	Predicates           []string `json:"predicates,omitempty"`
}

type CognitiveCounterargument struct {
	CandidateID      string   `json:"candidate_id"`
	AssertionIDs     []string `json:"assertion_ids,omitempty"`
	ObservationKinds []string `json:"observation_kinds,omitempty"`
}

// InvestigationPolicy declares a requested discriminating observation. The
// server independently computes its value and compiles the actual query.
type InvestigationPolicy struct {
	CandidateIDs             []string `json:"candidate_ids"`
	ObservationKind          string   `json:"observation_kind"`
	RationalePredicates      []string `json:"rationale_predicates,omitempty"`
	ExpectedEntropyReduction float64  `json:"expected_entropy_reduction,omitempty"`
	DecisionImpact           float64  `json:"decision_impact,omitempty"`
	DiagnosticValue          float64  `json:"diagnostic_value,omitempty"`
	Status                   string   `json:"status,omitempty"`
}

// CandidateExpansionRequest represents an unresolved mechanism, not a new
// root cause. It can only lead to a non-actionable candidate and extra facts.
type CandidateExpansionRequest struct {
	AssertionIDs         []string `json:"assertion_ids"`
	RequiredObservations []string `json:"required_observations"`
	Status               string   `json:"status"`
	Reason               string   `json:"reason,omitempty"`
}

type FalsificationResult struct {
	CandidateID                    string   `json:"candidate_id"`
	SupportingAssertionIDs         []string `json:"supporting_assertion_ids,omitempty"`
	ContradictingAssertionIDs      []string `json:"contradicting_assertion_ids,omitempty"`
	MissingObservationKinds        []string `json:"missing_observation_kinds,omitempty"`
	CounterfactualObservationKinds []string `json:"counterfactual_observation_kinds,omitempty"`
}

type PairwiseFalsification struct {
	PreferredCandidateID       string   `json:"preferred_candidate_id"`
	OtherCandidateID           string   `json:"other_candidate_id"`
	DiscriminatingAssertionIDs []string `json:"discriminating_assertion_ids,omitempty"`
	Result                     string   `json:"result"`
}

type RecoveryPermission struct {
	ObjectiveDiagnosisConfidence float64 `json:"objective_diagnosis_confidence"`
	ActionSafety                 float64 `json:"action_safety"`
	VerificationConfidence       float64 `json:"verification_confidence"`
	DiagnosisStability           float64 `json:"diagnosis_stability"`
	AutonomyScore                float64 `json:"autonomy_score"`
	Level                        string  `json:"level"`
	Allowed                      bool    `json:"allowed"`
	Reason                       string  `json:"reason"`
}

type InvestigationPlan struct {
	Objective      string       `json:"objective"`
	Tasks          []WorkerTask `json:"tasks"`
	StopConditions []string     `json:"stop_conditions"`
	RoundLimit     int          `json:"round_limit"`
	CreatedAt      time.Time    `json:"created_at"`
}

type WorkerTask struct {
	ID            string          `json:"id"`
	Source        string          `json:"source"`
	Question      string          `json:"question"`
	HypothesisIDs []string        `json:"hypothesis_ids,omitempty"`
	Request       EvidenceRequest `json:"request"`
	Required      bool            `json:"required"`
}

type ResourceRef struct {
	Namespace string `json:"namespace"`
	Service   string `json:"service,omitempty"`
	Resource  string `json:"resource,omitempty"`
	Kind      string `json:"kind,omitempty"`
}

// EvidenceRequest is a server-validated collection request. Free-form worker
// questions are never used as collector input or as an authorization boundary.
type EvidenceRequest struct {
	Source        string        `json:"source"`
	Targets       []ResourceRef `json:"targets"`
	SignalKinds   []string      `json:"signal_kinds,omitempty"`
	WindowStart   time.Time     `json:"window_start"`
	WindowEnd     time.Time     `json:"window_end"`
	HypothesisIDs []string      `json:"hypothesis_ids,omitempty"`
}

type WorkerFinding struct {
	TaskID                     string    `json:"task_id"`
	Worker                     string    `json:"worker"`
	Source                     string    `json:"source"`
	Summary                    string    `json:"summary"`
	EvidenceIDs                []string  `json:"evidence_ids"`
	SupportingHypothesisIDs    []string  `json:"supporting_hypothesis_ids,omitempty"`
	ContradictingHypothesisIDs []string  `json:"contradicting_hypothesis_ids,omitempty"`
	Unknowns                   []string  `json:"unknowns,omitempty"`
	CompletedAt                time.Time `json:"completed_at"`
}

type HypothesisArgument struct {
	Author      string            `json:"author"`
	Hypotheses  []HypothesisDraft `json:"hypotheses"`
	EvidenceIDs []string          `json:"evidence_ids,omitempty"`
	Uncertainty string            `json:"uncertainty,omitempty"`
}

type Critique struct {
	HypothesisID       string   `json:"hypothesis_id"`
	Challenge          string   `json:"challenge"`
	MissingEvidence    []string `json:"missing_evidence,omitempty"`
	ContradictingIDs   []string `json:"contradicting_evidence_ids,omitempty"`
	RecommendedSources []string `json:"recommended_sources,omitempty"`
}

type DebateRound struct {
	Round       int                `json:"round"`
	Primary     HypothesisArgument `json:"primary"`
	Alternative HypothesisArgument `json:"alternative"`
	Critiques   []Critique         `json:"critiques,omitempty"`
	OccurredAt  time.Time          `json:"occurred_at"`
}

type ArbitrationResult struct {
	SelectedHypothesisID string                 `json:"selected_hypothesis_id,omitempty"`
	DisplayHypothesisID  string                 `json:"display_hypothesis_id,omitempty"`
	RankedHypothesisIDs  []string               `json:"ranked_hypothesis_ids,omitempty"`
	SelectedScore        float64                `json:"selected_score"`
	ScoreMargin          float64                `json:"score_margin"`
	Accepted             bool                   `json:"accepted"`
	NeedsMoreEvidence    bool                   `json:"needs_more_evidence"`
	Reason               string                 `json:"reason"`
	GateResults          []HypothesisGateResult `json:"gate_results,omitempty"`
}

type HypothesisGateResult struct {
	HypothesisID   string                     `json:"hypothesis_id"`
	ScoreBreakdown HypothesisConfidenceRecord `json:"score_breakdown"`
	FailedGates    []string                   `json:"failed_gates,omitempty"`
}

type MemoryKind string

const (
	MemoryWorking    MemoryKind = "working"
	MemoryEpisodic   MemoryKind = "episodic"
	MemorySemantic   MemoryKind = "semantic"
	MemoryProcedural MemoryKind = "procedural"
)

type MemoryScope struct {
	Cluster   string `json:"cluster,omitempty"`
	Namespace string `json:"namespace"`
}

type MemoryQuery struct {
	IncidentID string      `json:"incident_id"`
	Agent      string      `json:"agent"`
	Kind       MemoryKind  `json:"kind"`
	Scope      MemoryScope `json:"scope"`
	Terms      []string    `json:"terms,omitempty"`
	Limit      int         `json:"limit"`
}

type MemoryResult struct {
	ID         string         `json:"id"`
	Kind       MemoryKind     `json:"kind"`
	Scope      MemoryScope    `json:"scope"`
	Summary    string         `json:"summary"`
	Score      float64        `json:"score"`
	Version    string         `json:"version,omitempty"`
	Provenance map[string]any `json:"provenance,omitempty"`
	ObservedAt time.Time      `json:"observed_at,omitempty"`
}

type MemoryAccessResult struct {
	ID      string  `json:"id"`
	Score   float64 `json:"score"`
	Version string  `json:"version,omitempty"`
}

type MemoryAccessEvent struct {
	IncidentID    string               `json:"incident_id"`
	Agent         string               `json:"agent"`
	Kind          MemoryKind           `json:"kind"`
	Scope         MemoryScope          `json:"scope"`
	QueryHash     string               `json:"query_hash"`
	ResultIDs     []string             `json:"result_ids,omitempty"`
	Results       []MemoryAccessResult `json:"results,omitempty"`
	PolicyVersion string               `json:"policy_version,omitempty"`
	CreatedAt     time.Time            `json:"created_at"`
}

type IncidentLearningInput struct {
	Incident *Incident `json:"incident"`
	Source   string    `json:"source"`
}

type ModelUsageEvent struct {
	IncidentID      string    `json:"incident_id"`
	Agent           string    `json:"agent"`
	ParentAgent     string    `json:"parent_agent,omitempty"`
	Phase           string    `json:"phase"`
	InputTokens     int       `json:"input_tokens"`
	OutputTokens    int       `json:"output_tokens"`
	ReasoningTokens int       `json:"reasoning_tokens,omitempty"`
	DurationMS      float64   `json:"duration_ms"`
	EstimatedCost   float64   `json:"estimated_cost"`
	CreatedAt       time.Time `json:"created_at"`
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
	ID              string                 `json:"id"`
	Source          EvidenceSource         `json:"source"`
	Type            EvidenceType           `json:"type,omitempty"`
	Kind            string                 `json:"kind,omitempty"` // backward-compatible alias for Type
	Timestamp       time.Time              `json:"timestamp,omitempty"`
	WindowStart     time.Time              `json:"window_start,omitempty"`
	WindowEnd       time.Time              `json:"window_end,omitempty"`
	Namespace       string                 `json:"namespace,omitempty"`
	Service         string                 `json:"service,omitempty"`
	Resource        string                 `json:"resource,omitempty"`
	Content         map[string]any         `json:"content,omitempty"`
	Data            map[string]any         `json:"data,omitempty"` // backward-compatible alias for Content
	Facts           map[string]any         `json:"facts,omitempty"`
	TruncatedFields []string               `json:"truncated_fields,omitempty"`
	Summary         string                 `json:"summary"`
	Confidence      float64                `json:"confidence,omitempty"`
	TraceID         string                 `json:"trace_id,omitempty"`
	TemplateID      string                 `json:"template_id,omitempty"`
	CollectedAt     time.Time              `json:"collected_at,omitempty"`
	ObservedAt      time.Time              `json:"observed_at,omitempty"` // backward-compatible alias for Timestamp
	RelevanceScore  float64                `json:"relevance_score,omitempty"`
	NeuralScore     float64                `json:"neural_score,omitempty"`
	NeuralRanked    bool                   `json:"neural_ranked,omitempty"`
	RankBreakdown   *EvidenceRankBreakdown `json:"rank_breakdown,omitempty"`
	Attribution     *EvidenceAttribution   `json:"attribution,omitempty"`
	RankingReasons  []string               `json:"ranking_reasons,omitempty"`
	CausalNodeIDs   []string               `json:"causal_node_ids,omitempty"`
	AnomalyScore    float64                `json:"anomaly_score,omitempty"`
	Signals         []EvidenceSignal       `json:"signals,omitempty"`
	QualityScore    float64                `json:"quality_score,omitempty"`
}

// EvidenceSignal is a server-derived operational observation. It preserves
// the source fact while giving diagnosis and arbitration a common, typed
// vocabulary instead of relying on free-form evidence summaries.
type EvidenceSignal struct {
	ID                string    `json:"id"`
	EvidenceID        string    `json:"evidence_id"`
	Source            string    `json:"source"`
	Category          string    `json:"category"`
	Signal            string    `json:"signal"`
	Value             float64   `json:"value"`
	Strength          float64   `json:"strength"`
	Direction         string    `json:"direction"`
	Reliability       float64   `json:"reliability"`
	Independence      float64   `json:"independence"`
	TemporalAlignment float64   `json:"temporal_alignment"`
	DiagnosticWeight  float64   `json:"diagnostic_weight"`
	Extraction        string    `json:"extraction"`
	WindowStart       time.Time `json:"window_start,omitempty"`
	WindowEnd         time.Time `json:"window_end,omitempty"`
	ObservedAt        time.Time `json:"observed_at,omitempty"`
	Namespace         string    `json:"namespace,omitempty"`
	Service           string    `json:"service,omitempty"`
	Resource          string    `json:"resource,omitempty"`
}

// EvidenceView is the bounded model-facing representation shared by workers,
// diagnosis and critic roles. It never has a separate Data/Content alias.
type EvidenceView struct {
	ID               string           `json:"id"`
	Source           string           `json:"source"`
	Kind             string           `json:"kind"`
	Namespace        string           `json:"namespace,omitempty"`
	Service          string           `json:"service,omitempty"`
	Resource         string           `json:"resource,omitempty"`
	ObservedAt       time.Time        `json:"observed_at,omitempty"`
	Summary          string           `json:"summary"`
	Facts            map[string]any   `json:"facts,omitempty"`
	TruncatedFields  []string         `json:"truncated_fields,omitempty"`
	CausalNodeIDs    []string         `json:"causal_node_ids,omitempty"`
	ContextRelevance float64          `json:"context_relevance,omitempty"`
	AnomalyScore     float64          `json:"anomaly_score,omitempty"`
	Signals          []EvidenceSignal `json:"signals,omitempty"`
	QualityScore     float64          `json:"quality_score,omitempty"`
}

type EvidenceRankBreakdown struct {
	EvidenceID          string             `json:"evidence_id"`
	DeterministicScore  float64            `json:"deterministic_score"`
	NeuralScore         float64            `json:"neural_score,omitempty"`
	DeterministicWeight float64            `json:"deterministic_weight"`
	NeuralWeight        float64            `json:"neural_weight"`
	FinalScore          float64            `json:"final_score"`
	NeuralUsed          bool               `json:"neural_used"`
	SignalStrength      float64            `json:"signal_strength,omitempty"`
	SourceReliability   float64            `json:"source_reliability,omitempty"`
	TemporalAlignment   float64            `json:"temporal_alignment,omitempty"`
	EvidenceQuality     float64            `json:"evidence_quality,omitempty"`
	Factors             map[string]float64 `json:"factors,omitempty"`
}

// IncidentFeatures is the bounded, deterministic representation used by all
// historical retrievers. It contains observed facts only and never benchmark
// labels or model-generated ground truth.
type IncidentFeatures struct {
	IncidentID       string                  `json:"incident_id"`
	Cluster          string                  `json:"cluster,omitempty"`
	Namespace        string                  `json:"namespace"`
	Service          string                  `json:"service"`
	Resource         string                  `json:"resource"`
	WindowStart      time.Time               `json:"window_start"`
	WindowEnd        time.Time               `json:"window_end"`
	Terms            []string                `json:"terms"`
	EvidenceTypes    []string                `json:"evidence_types"`
	TraceIDs         []string                `json:"trace_ids"`
	TemplateIDs      []string                `json:"template_ids"`
	TopologyServices []string                `json:"topology_services"`
	TopologyGraph    IncidentDependencyGraph `json:"topology_graph,omitempty"`
	CausalNodeIDs    []string                `json:"causal_node_ids"`
	Revision         string                  `json:"revision,omitempty"`
	Observed         map[string]float64      `json:"observed,omitempty"`
}

type RankBreakdown struct {
	// Stage scores make the historical-incident ranking pipeline auditable.
	// Semantic and lexical scores are candidate-generation signals; topology
	// and causal scores are reasoning signals and must not be used as hard
	// filters.
	SemanticScore            float64                `json:"semantic_score,omitempty"`
	LexicalScore             float64                `json:"lexical_score,omitempty"`
	TopologyScore            float64                `json:"topology_score,omitempty"`
	CausalScore              float64                `json:"causal_score,omitempty"`
	MetadataScore            float64                `json:"metadata_score,omitempty"`
	ReasoningScore           float64                `json:"reasoning_score,omitempty"`
	NormalizedRRF            float64                `json:"normalized_rrf"`
	TopologySimilarity       float64                `json:"topology_similarity"`
	EvidenceFeatureOverlap   float64                `json:"evidence_feature_overlap"`
	ServiceResourceProximity float64                `json:"service_resource_proximity"`
	CausalPathCoverage       float64                `json:"causal_path_coverage"`
	RevisionTemporalContext  float64                `json:"revision_temporal_context"`
	NeuralSimilarity         float64                `json:"neural_similarity,omitempty"`
	DeterministicScore       float64                `json:"deterministic_score,omitempty"`
	NeuralRanked             bool                   `json:"neural_ranked,omitempty"`
	FinalScore               float64                `json:"final_score"`
	IncidentRank             *IncidentRankBreakdown `json:"incident_rank,omitempty"`
}

type IncidentRankBreakdown struct {
	IncidentID          string             `json:"incident_id"`
	SemanticScore       float64            `json:"semantic_score"`
	LexicalScore        float64            `json:"lexical_score"`
	TopologyScore       float64            `json:"topology_score"`
	CausalScore         float64            `json:"causal_score"`
	MetadataScore       float64            `json:"metadata_score"`
	ReasoningScore      float64            `json:"reasoning_score"`
	DeterministicScore  float64            `json:"deterministic_score"`
	NeuralScore         float64            `json:"neural_score,omitempty"`
	DeterministicWeight float64            `json:"deterministic_weight"`
	NeuralWeight        float64            `json:"neural_weight"`
	FinalScore          float64            `json:"final_score"`
	NeuralUsed          bool               `json:"neural_used"`
	Factors             map[string]float64 `json:"factors,omitempty"`
}

type RetrievalCandidate struct {
	IncidentID     string             `json:"incident_id"`
	Cluster        string             `json:"cluster,omitempty"`
	Namespace      string             `json:"namespace"`
	Service        string             `json:"service"`
	Resource       string             `json:"resource"`
	Category       string             `json:"category"`
	RootCause      string             `json:"root_cause"`
	Summary        string             `json:"summary,omitempty"`
	Revision       string             `json:"revision,omitempty"`
	Features       IncidentFeatures   `json:"features"`
	SourceRanks    map[string]int     `json:"source_ranks,omitempty"`
	SourceScores   map[string]float64 `json:"source_scores,omitempty"`
	RRFScore       float64            `json:"rrf_score,omitempty"`
	Rank           RankBreakdown      `json:"rank"`
	RankingReasons []string           `json:"ranking_reasons,omitempty"`
}

type CausalNode struct {
	ID     string `json:"id" yaml:"id"`
	Type   string `json:"type" yaml:"type"`
	Name   string `json:"name,omitempty" yaml:"name,omitempty"`
	Source string `json:"source,omitempty" yaml:"source,omitempty"`
	// Signals is the server-owned, canonical signal vocabulary that can
	// establish this node. Causal evaluation must use this field (and observed
	// EvidenceSignal records), never prose in an evidence summary, facts blob,
	// or an LLM response.
	Signals []string `json:"signals,omitempty" yaml:"signals,omitempty"`
	// Match is retained for backwards-compatible display and migration of older
	// pattern records. It is not an executable causal matching contract.
	Match             []string `json:"match,omitempty" yaml:"match,omitempty"`
	Confidence        float64  `json:"confidence,omitempty" yaml:"confidence,omitempty"`
	SourceEvidenceIDs []string `json:"source_evidence_ids,omitempty" yaml:"source_evidence_ids,omitempty"`
}

type CausalEdge struct {
	From       string  `json:"from" yaml:"from"`
	To         string  `json:"to" yaml:"to"`
	Relation   string  `json:"relation,omitempty" yaml:"relation,omitempty"`
	Confidence float64 `json:"confidence,omitempty" yaml:"confidence,omitempty"`
}

func (edge *CausalEdge) UnmarshalJSON(data []byte) error {
	type wire struct {
		From       string  `json:"from"`
		To         string  `json:"to"`
		Source     string  `json:"source"`
		Target     string  `json:"target"`
		Relation   string  `json:"relation"`
		Confidence float64 `json:"confidence"`
	}
	var value wire
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	edge.From, edge.To = value.From, value.To
	if edge.From == "" {
		edge.From = value.Source
	}
	if edge.To == "" {
		edge.To = value.Target
	}
	edge.Relation, edge.Confidence = value.Relation, value.Confidence
	return nil
}

type CausalGraph struct {
	Nodes []CausalNode `json:"nodes" yaml:"nodes"`
	Edges []CausalEdge `json:"edges" yaml:"edges"`
}

type CausalEvidencePattern struct {
	Source string   `json:"source" yaml:"source"`
	Type   string   `json:"type" yaml:"type"`
	Tokens []string `json:"tokens,omitempty" yaml:"tokens,omitempty"`
}

type CausalPattern struct {
	ID       string       `json:"id" yaml:"id"`
	Category string       `json:"category" yaml:"category"`
	Cause    string       `json:"cause" yaml:"cause"`
	Nodes    []CausalNode `json:"nodes" yaml:"nodes"`
	Edges    []CausalEdge `json:"edges" yaml:"edges"`
	// RequiredAdmissionNodeIDs expresses a conjunction of server-observed
	// causal nodes required before this pattern can create a candidate. It is
	// used for state transitions such as a rollout: observing the transition or
	// an application error alone is insufficient, while their verified pair is
	// diagnostically meaningful. It contains graph node IDs, never prose.
	RequiredAdmissionNodeIDs []string                `json:"required_admission_node_ids,omitempty" yaml:"required_admission_node_ids,omitempty"`
	SupportingEvidence       []CausalEvidencePattern `json:"supporting_evidence,omitempty" yaml:"supporting_evidence,omitempty"`
	ContradictingEvidence    []CausalEvidencePattern `json:"contradicting_evidence,omitempty" yaml:"contradicting_evidence,omitempty"`
	SourceIncidents          []string                `json:"source_incidents,omitempty" yaml:"source_incidents,omitempty"`
	Cluster                  string                  `json:"cluster,omitempty" yaml:"cluster,omitempty"`
	Namespace                string                  `json:"namespace,omitempty" yaml:"namespace,omitempty"`
	Source                   string                  `json:"source" yaml:"source"`
	Confidence               float64                 `json:"confidence" yaml:"confidence"`
	Status                   string                  `json:"status" yaml:"status"`
	Version                  int                     `json:"version" yaml:"version"`
	SupportCount             int                     `json:"support_count,omitempty" yaml:"support_count,omitempty"`
	CreatedAt                time.Time               `json:"created_at,omitempty" yaml:"-"`
	UpdatedAt                time.Time               `json:"updated_at,omitempty" yaml:"-"`
}

type HypothesisDraft struct {
	ID                       string   `json:"id" jsonschema:"required"`
	Category                 string   `json:"category" jsonschema:"required,enum=cpu,enum=memory,enum=database,enum=network,enum=deployment"`
	Variant                  string   `json:"variant" jsonschema:"required"`
	Cause                    string   `json:"cause" jsonschema:"required"`
	Service                  string   `json:"service" jsonschema:"required"`
	Resource                 string   `json:"resource" jsonschema:"required"`
	PriorProbability         float64  `json:"prior_probability" jsonschema:"required,minimum=0,maximum=1"`
	SupportingEvidenceIDs    []string `json:"supporting_evidence_ids" jsonschema:"required,minItems=1"`
	ContradictingEvidenceIDs []string `json:"contradicting_evidence_ids,omitempty"`
	ExpectedCausalPath       []string `json:"expected_causal_path,omitempty"`
	ExpectedCausalNodeIDs    []string `json:"expected_causal_node_ids,omitempty" jsonschema:"required,minItems=1"`
	// RequireCausalMechanism is set only by the deterministic candidate engine.
	// It prevents a symptom/observation-only path from becoming an accepted
	// root-cause diagnosis while preserving compatibility for non-KubePilot
	// baseline strategies that use the legacy verification contract.
	RequireCausalMechanism  bool     `json:"require_causal_mechanism,omitempty"`
	FalsificationConditions []string `json:"falsification_conditions,omitempty"`
}

type VerifiedHypothesis struct {
	Draft               HypothesisDraft              `json:"draft"`
	SupportingScore     float64                      `json:"supporting_score"`
	ContradictionScore  float64                      `json:"contradiction_score"`
	CausalPathCoverage  float64                      `json:"causal_path_coverage"`
	HistoricalRelevance float64                      `json:"historical_relevance"`
	TopologyRelevance   float64                      `json:"topology_relevance"`
	MissingCausalNodes  []string                     `json:"missing_causal_nodes,omitempty"`
	VerifiedEvidenceIDs []string                     `json:"verified_evidence_ids"`
	FinalScore          float64                      `json:"final_score"`
	ObjectiveScore      float64                      `json:"objective_score"`
	ObservationCoverage float64                      `json:"observation_coverage"`
	Status              HypothesisStatus             `json:"status,omitempty"`
	ConfidenceHistory   []HypothesisConfidenceRecord `json:"confidence_history,omitempty"`
}

type DiagnosisLedger struct {
	EvidenceOriginalCount int                    `json:"evidence_original_count"`
	EvidenceRetainedCount int                    `json:"evidence_retained_count"`
	EvidenceOriginalBytes int                    `json:"evidence_original_bytes"`
	EvidenceRetainedBytes int                    `json:"evidence_retained_bytes"`
	Candidates            []RetrievalCandidate   `json:"candidates,omitempty"`
	CausalPatterns        []CausalPattern        `json:"causal_patterns,omitempty"`
	Drafts                []HypothesisDraft      `json:"drafts,omitempty"`
	Verified              []VerifiedHypothesis   `json:"verified,omitempty"`
	SelectedHypothesisID  string                 `json:"selected_hypothesis_id,omitempty"`
	AdditionalCollection  bool                   `json:"additional_collection"`
	InfrastructureErrors  []string               `json:"infrastructure_errors,omitempty"`
	HypothesisTransitions []HypothesisTransition `json:"hypothesis_transitions,omitempty"`
	SafetyFeedback        []SafetyFeedback       `json:"safety_feedback,omitempty"`
	AgentDecisions        []AgentDecisionEvent   `json:"agent_decisions,omitempty"`
}

type SafetyCategory string

const (
	SafetyRepairable    SafetyCategory = "repairable"
	SafetyFatal         SafetyCategory = "fatal"
	SafetyHumanRequired SafetyCategory = "human_required"
)

type SafetyScope string

const (
	SafetyScopeSupervisor       SafetyScope = "supervisor"
	SafetyScopeDiagnosis        SafetyScope = "diagnosis"
	SafetyScopeRecoveryProposal SafetyScope = "recovery_proposal"
	SafetyScopeExecution        SafetyScope = "execution"
	SafetyScopeVerification     SafetyScope = "verification"
)

type SafetyCheck struct {
	Name    string `json:"name"`
	Passed  bool   `json:"passed"`
	Message string `json:"message,omitempty"`
}

type SafetyFeedback struct {
	Allowed              bool           `json:"allowed"`
	Scope                SafetyScope    `json:"scope"`
	Category             SafetyCategory `json:"category"`
	Code                 string         `json:"code"`
	Reason               string         `json:"reason"`
	FailedChecks         []SafetyCheck  `json:"failed_checks,omitempty"`
	MissingRequirements  []string       `json:"missing_requirements,omitempty"`
	RequiredCapabilities []string       `json:"required_capabilities,omitempty"`
	Retryable            bool           `json:"retryable"`
	RequiresHuman        bool           `json:"requires_human"`
	RemainingCorrections int            `json:"remaining_corrections"`
}

type AgentBudget struct {
	MaxIterations int `json:"max_iterations"`
	MaxToolUses   int `json:"max_tool_uses"`
	// MaxTokens is the generated-output cap for one Agent model response. Input
	// tokens and cumulative Agent/Incident totals are telemetry only.
	MaxTokens      int `json:"max_tokens"`
	MaxCorrections int `json:"max_corrections"`
}

type AgentBudgetUsage struct {
	Iterations  int `json:"iterations"`
	ToolUses    int `json:"tool_uses"`
	ToolCost    int `json:"tool_cost"`
	Tokens      int `json:"tokens"`
	Corrections int `json:"corrections"`
}

type AgentBudgetState struct {
	Limits map[string]AgentBudget      `json:"limits"`
	Usage  map[string]AgentBudgetUsage `json:"usage"`
	// Incident totals are telemetry only. They are never used to accept or
	// reject an Agent action; every enforceable budget is scoped per Agent.
	IncidentUses   int `json:"incident_tool_uses"`
	IncidentCost   int `json:"incident_tool_cost"`
	IncidentTokens int `json:"incident_tokens"`
}

type HypothesisStatus string

const (
	HypothesisProposed          HypothesisStatus = "PROPOSED"
	HypothesisAdmitted          HypothesisStatus = "ADMITTED"
	HypothesisInvestigating     HypothesisStatus = "INVESTIGATING"
	HypothesisCreated           HypothesisStatus = "CREATED"
	HypothesisEvidenceSearching HypothesisStatus = "EVIDENCE_SEARCHING"
	HypothesisSupported         HypothesisStatus = "SUPPORTED"
	HypothesisRefuted           HypothesisStatus = "REFUTED"
	HypothesisAccepted          HypothesisStatus = "ACCEPTED"
	HypothesisReplaced          HypothesisStatus = "REPLACED"
	HypothesisMerged            HypothesisStatus = "MERGED"
	HypothesisAbandoned         HypothesisStatus = "ABANDONED"
)

type HypothesisTransition struct {
	HypothesisID string           `json:"hypothesis_id"`
	From         HypothesisStatus `json:"from,omitempty"`
	To           HypothesisStatus `json:"to"`
	EvidenceIDs  []string         `json:"evidence_ids,omitempty"`
	ToolCallID   string           `json:"tool_call_id,omitempty"`
	Reason       string           `json:"reason"`
	OccurredAt   time.Time        `json:"occurred_at"`
}

type HypothesisConfidenceRecord struct {
	HypothesisID        string    `json:"hypothesis_id"`
	Sequence            int       `json:"sequence"`
	Score               float64   `json:"score"`
	ObjectiveScore      float64   `json:"objective_score,omitempty"`
	ObservationCoverage float64   `json:"observation_coverage,omitempty"`
	ModelPrior          float64   `json:"model_prior"`
	SupportingScore     float64   `json:"supporting_score"`
	ContradictionScore  float64   `json:"contradiction_score"`
	CausalPathCoverage  float64   `json:"causal_path_coverage"`
	HistoricalRelevance float64   `json:"historical_relevance"`
	TopologyRelevance   float64   `json:"topology_relevance"`
	TemporalConsistency float64   `json:"temporal_consistency"`
	AddedEvidenceIDs    []string  `json:"added_evidence_ids,omitempty"`
	RemovedEvidenceIDs  []string  `json:"removed_evidence_ids,omitempty"`
	EvidenceSourceCount int       `json:"evidence_source_count"`
	ToolCallID          string    `json:"tool_call_id,omitempty"`
	ComputedAt          time.Time `json:"computed_at"`
}

type EvidenceAttribution struct {
	TemporalAlignment    float64  `json:"temporal_alignment"`
	ServiceResourceMatch float64  `json:"service_resource_match"`
	TraceRequestPodMatch float64  `json:"trace_request_pod_match"`
	CausalContribution   float64  `json:"causal_contribution"`
	AnomalySpecificity   float64  `json:"anomaly_specificity"`
	AttributionScore     float64  `json:"attribution_score"`
	Reasons              []string `json:"reasons,omitempty"`
}

type DependencyNode struct {
	ID       string            `json:"id"`
	Kind     string            `json:"kind"`
	Service  string            `json:"service,omitempty"`
	Resource string            `json:"resource,omitempty"`
	Role     string            `json:"role,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type DependencyEdge struct {
	From      string  `json:"from"`
	To        string  `json:"to"`
	Kind      string  `json:"kind"`
	ErrorRate float64 `json:"error_rate,omitempty"`
	LatencyMS float64 `json:"latency_ms,omitempty"`
}

type IncidentDependencyGraph struct {
	Nodes                 []DependencyNode `json:"nodes"`
	Edges                 []DependencyEdge `json:"edges"`
	RootService           string           `json:"root_service"`
	SuspectedFailureNodes []string         `json:"suspected_failure_nodes,omitempty"`
	ErrorPropagationPaths [][]string       `json:"error_propagation_paths,omitempty"`
}

type AgentDecisionEvent struct {
	AgentID            string    `json:"agent_id"`
	Iteration          int       `json:"iteration"`
	ObservationSummary string    `json:"observation_summary,omitempty"`
	SelectedAction     string    `json:"selected_action"`
	ReasonCategory     string    `json:"reason_category"`
	OccurredAt         time.Time `json:"occurred_at"`
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

// RecoveryExecution is server-owned audit state for the deterministic action
// boundary. It records confirmed mutations separately from attempts whose
// result is unknown, allowing safety checks without exposing backend internals.
type RecoveryExecution struct {
	Attempts           int       `json:"attempts"`
	ConfirmedMutations int       `json:"confirmed_mutations"`
	Namespace          string    `json:"namespace,omitempty"`
	Target             string    `json:"target,omitempty"`
	Action             string    `json:"action,omitempty"`
	Outcome            string    `json:"outcome"`
	LastAttemptAt      time.Time `json:"last_attempt_at"`
	CompletedAt        time.Time `json:"completed_at,omitempty"`
}

type Verification struct {
	Success      bool            `json:"success"`
	Checks       map[string]bool `json:"checks"`
	Message      string          `json:"message"`
	RestartCount *int32          `json:"restart_count,omitempty"`
	CompletedAt  time.Time       `json:"completed_at"`
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
