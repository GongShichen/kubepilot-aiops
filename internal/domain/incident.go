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
	SkillSnapshotHash    string            `json:"skill_snapshot_hash,omitempty"`
	RankingPolicyHash    string            `json:"ranking_policy_hash,omitempty"`
	RerankerConfigHash   string            `json:"reranker_config_hash,omitempty"`
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
	DiagnosisLedger      *DiagnosisLedger  `json:"diagnosis_ledger,omitempty"`
	AgentBudget          *AgentBudgetState `json:"agent_budget,omitempty"`
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
	ID             string                 `json:"id"`
	Source         EvidenceSource         `json:"source"`
	Type           EvidenceType           `json:"type,omitempty"`
	Kind           string                 `json:"kind,omitempty"` // backward-compatible alias for Type
	Timestamp      time.Time              `json:"timestamp,omitempty"`
	WindowStart    time.Time              `json:"window_start,omitempty"`
	WindowEnd      time.Time              `json:"window_end,omitempty"`
	Namespace      string                 `json:"namespace,omitempty"`
	Service        string                 `json:"service,omitempty"`
	Resource       string                 `json:"resource,omitempty"`
	Content        map[string]any         `json:"content,omitempty"`
	Data           map[string]any         `json:"data,omitempty"` // backward-compatible alias for Content
	Summary        string                 `json:"summary"`
	Confidence     float64                `json:"confidence,omitempty"`
	TraceID        string                 `json:"trace_id,omitempty"`
	TemplateID     string                 `json:"template_id,omitempty"`
	CollectedAt    time.Time              `json:"collected_at,omitempty"`
	ObservedAt     time.Time              `json:"observed_at,omitempty"` // backward-compatible alias for Timestamp
	RelevanceScore float64                `json:"relevance_score,omitempty"`
	NeuralScore    float64                `json:"neural_score,omitempty"`
	NeuralRanked   bool                   `json:"neural_ranked,omitempty"`
	RankBreakdown  *EvidenceRankBreakdown `json:"rank_breakdown,omitempty"`
	Attribution    *EvidenceAttribution   `json:"attribution,omitempty"`
	RankingReasons []string               `json:"ranking_reasons,omitempty"`
	CausalNodeIDs  []string               `json:"causal_node_ids,omitempty"`
}

type EvidenceRankBreakdown struct {
	EvidenceID          string             `json:"evidence_id"`
	DeterministicScore  float64            `json:"deterministic_score"`
	NeuralScore         float64            `json:"neural_score,omitempty"`
	DeterministicWeight float64            `json:"deterministic_weight"`
	NeuralWeight        float64            `json:"neural_weight"`
	FinalScore          float64            `json:"final_score"`
	NeuralUsed          bool               `json:"neural_used"`
	Factors             map[string]float64 `json:"factors,omitempty"`
}

// IncidentFeatures is the bounded, deterministic representation used by all
// historical retrievers. It contains observed facts only and never benchmark
// labels or model-generated ground truth.
type IncidentFeatures struct {
	IncidentID       string                  `json:"incident_id"`
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
	DeterministicScore  float64            `json:"deterministic_score"`
	TopologyScore       float64            `json:"topology_score"`
	NeuralScore         float64            `json:"neural_score,omitempty"`
	DeterministicWeight float64            `json:"deterministic_weight"`
	NeuralWeight        float64            `json:"neural_weight"`
	FinalScore          float64            `json:"final_score"`
	NeuralUsed          bool               `json:"neural_used"`
	Factors             map[string]float64 `json:"factors,omitempty"`
}

type RetrievalCandidate struct {
	IncidentID     string             `json:"incident_id"`
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
	ID    string   `json:"id" yaml:"id"`
	Type  string   `json:"type" yaml:"type"`
	Match []string `json:"match" yaml:"match"`
}

type CausalEdge struct {
	From string `json:"from" yaml:"from"`
	To   string `json:"to" yaml:"to"`
}

type CausalPattern struct {
	ID           string       `json:"id" yaml:"id"`
	Category     string       `json:"category" yaml:"category"`
	Cause        string       `json:"cause" yaml:"cause"`
	Nodes        []CausalNode `json:"nodes" yaml:"nodes"`
	Edges        []CausalEdge `json:"edges" yaml:"edges"`
	Source       string       `json:"source" yaml:"source"`
	Confidence   float64      `json:"confidence" yaml:"confidence"`
	Status       string       `json:"status" yaml:"status"`
	Version      int          `json:"version" yaml:"version"`
	SupportCount int          `json:"support_count,omitempty" yaml:"support_count,omitempty"`
	CreatedAt    time.Time    `json:"created_at,omitempty" yaml:"-"`
	UpdatedAt    time.Time    `json:"updated_at,omitempty" yaml:"-"`
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
	ExpectedCausalPath       []string `json:"expected_causal_path" jsonschema:"required,minItems=1"`
	FalsificationConditions  []string `json:"falsification_conditions,omitempty"`
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
	MaxIterations  int `json:"max_iterations"`
	MaxToolUses    int `json:"max_tool_uses"`
	MaxToolCost    int `json:"max_tool_cost"`
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
	Limits         map[string]AgentBudget      `json:"limits"`
	Usage          map[string]AgentBudgetUsage `json:"usage"`
	IncidentUses   int                         `json:"incident_tool_uses"`
	IncidentCost   int                         `json:"incident_tool_cost"`
	IncidentTokens int                         `json:"incident_tokens"`
}

type HypothesisStatus string

const (
	HypothesisCreated           HypothesisStatus = "CREATED"
	HypothesisEvidenceSearching HypothesisStatus = "EVIDENCE_SEARCHING"
	HypothesisSupported         HypothesisStatus = "SUPPORTED"
	HypothesisRefuted           HypothesisStatus = "REFUTED"
	HypothesisAccepted          HypothesisStatus = "ACCEPTED"
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
