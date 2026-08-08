package domain

import "time"

// OperationalWorldModel is the server-owned, replayable representation of the
// environment seen by the Brain. It contains observations and relationships;
// it never contains a selected root cause or a model-generated hypothesis.
type OperationalWorldModel struct {
	IncidentID           string                `json:"incident_id"`
	Cluster              string                `json:"cluster,omitempty"`
	Namespace            string                `json:"namespace"`
	RootEntityID         string                `json:"root_entity_id,omitempty"`
	Entities             []OperationalEntity   `json:"entities"`
	Relations            []OperationalRelation `json:"relations"`
	AbnormalSignals      []OperationalSignal   `json:"abnormal_signals"`
	Timeline             []OperationalEvent    `json:"timeline"`
	MetricSignatures     []MetricSignature     `json:"metric_signatures"`
	EvidenceSnapshotHash string                `json:"evidence_snapshot_hash"`
	BuiltAt              time.Time             `json:"built_at"`
}

type OperationalEntity struct {
	ID          string            `json:"id"`
	Kind        string            `json:"kind"`
	Namespace   string            `json:"namespace,omitempty"`
	Service     string            `json:"service,omitempty"`
	Resource    string            `json:"resource,omitempty"`
	State       string            `json:"state,omitempty"`
	ObservedAt  time.Time         `json:"observed_at,omitempty"`
	EvidenceIDs []string          `json:"evidence_ids"`
	Attributes  map[string]string `json:"attributes,omitempty"`
}

type OperationalRelation struct {
	From        string   `json:"from"`
	To          string   `json:"to"`
	Kind        string   `json:"kind"`
	EvidenceIDs []string `json:"evidence_ids"`
}

type OperationalSignal struct {
	ID                string    `json:"id"`
	EntityID          string    `json:"entity_id"`
	Category          string    `json:"category"`
	Signal            string    `json:"signal"`
	Direction         string    `json:"direction,omitempty"`
	Value             float64   `json:"value,omitempty"`
	Strength          float64   `json:"strength"`
	Reliability       float64   `json:"reliability"`
	TemporalAlignment float64   `json:"temporal_alignment"`
	EvidenceID        string    `json:"evidence_id"`
	ObservedAt        time.Time `json:"observed_at,omitempty"`
}

type OperationalEvent struct {
	ID         string    `json:"id"`
	EntityID   string    `json:"entity_id"`
	Kind       string    `json:"kind"`
	Summary    string    `json:"summary"`
	EvidenceID string    `json:"evidence_id"`
	OccurredAt time.Time `json:"occurred_at,omitempty"`
}

// MetricSignature is an observed numeric shape used by metric retrieval. It
// describes the metric and its direction without interpreting a root cause.
type MetricSignature struct {
	Name       string    `json:"name"`
	EntityID   string    `json:"entity_id"`
	Direction  string    `json:"direction,omitempty"`
	Value      float64   `json:"value,omitempty"`
	Strength   float64   `json:"strength"`
	EvidenceID string    `json:"evidence_id"`
	ObservedAt time.Time `json:"observed_at,omitempty"`
}

type RetrievalChannel string

const (
	RetrievalBM25     RetrievalChannel = "BM25"
	RetrievalVector   RetrievalChannel = "VECTOR"
	RetrievalGraph    RetrievalChannel = "GRAPH"
	RetrievalTemporal RetrievalChannel = "TEMPORAL"
	RetrievalMetric   RetrievalChannel = "METRIC"
)

type HybridRetrievalQuery struct {
	IncidentID    string                   `json:"incident_id"`
	Terms         []string                 `json:"terms"`
	Understanding HybridQueryUnderstanding `json:"understanding"`
	Features      IncidentFeatures         `json:"features"`
	WorldModel    *OperationalWorldModel   `json:"world_model,omitempty"`
	Limit         int                      `json:"limit"`
}

type RetrievalTimeRange struct {
	Start time.Time `json:"start,omitempty"`
	End   time.Time `json:"end,omitempty"`
}

// HybridQueryUnderstanding is a replayable operational interpretation of a
// retrieval request. It controls retrieval only and never asserts a root cause.
type HybridQueryUnderstanding struct {
	Entities []string           `json:"entities,omitempty"`
	Intent   string             `json:"intent"`
	Time     RetrievalTimeRange `json:"time"`
	Signals  []string           `json:"signals,omitempty"`
}

type RetrievalFusionProfile struct {
	ChannelWeights map[RetrievalChannel]float64 `json:"channel_weights"`
	Reasons        []string                     `json:"reasons"`
}

type HybridRetrievalChannelResult struct {
	Channel    RetrievalChannel     `json:"channel"`
	Candidates []RetrievalCandidate `json:"candidates"`
	Available  bool                 `json:"available"`
	Error      string               `json:"error,omitempty"`
}

type HybridRetrievalResult struct {
	Query         HybridQueryUnderstanding       `json:"query"`
	FusionProfile RetrievalFusionProfile         `json:"fusion_profile"`
	Channels      []HybridRetrievalChannelResult `json:"channels"`
	Fused         []RetrievalCandidate           `json:"fused"`
	Final         []RetrievalCandidate           `json:"final"`
	RerankerUsed  bool                           `json:"reranker_used"`
	SnapshotHash  string                         `json:"snapshot_hash"`
	RetrievedAt   time.Time                      `json:"retrieved_at"`
}

type SkillSearchDocument struct {
	ID                    string              `json:"id"`
	Version               string              `json:"version"`
	ContentHash           string              `json:"content_hash"`
	Description           string              `json:"description"`
	Procedure             string              `json:"procedure"`
	OutputContract        string              `json:"output_contract"`
	CompatiblePhases      []BrainPhase        `json:"compatible_phases"`
	AllowedToolCategories []BrainToolCategory `json:"allowed_tool_categories"`
}

type SkillSearchResult struct {
	ID          string  `json:"id"`
	Version     string  `json:"version"`
	ContentHash string  `json:"content_hash"`
	Description string  `json:"description"`
	BM25Score   float64 `json:"bm25_score"`
	VectorScore float64 `json:"vector_score"`
	PhaseScore  float64 `json:"phase_score"`
	NeuralScore float64 `json:"neural_score,omitempty"`
	FinalScore  float64 `json:"final_score"`
}

type SkillRetrievalQuery struct {
	IncidentID string                `json:"incident_id"`
	Phase      BrainPhase            `json:"phase"`
	Text       string                `json:"text"`
	Documents  []SkillSearchDocument `json:"documents"`
	Limit      int                   `json:"limit"`
}

type SkillRetrievalResult struct {
	QueryHash    string              `json:"query_hash"`
	Results      []SkillSearchResult `json:"results"`
	VectorUsed   bool                `json:"vector_used"`
	RerankerUsed bool                `json:"reranker_used"`
	SnapshotHash string              `json:"snapshot_hash"`
	RetrievedAt  time.Time           `json:"retrieved_at"`
}
