package discovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

// NodeType describes the observable role of a node in an incident causal graph.
// The vocabulary is intentionally small and stable so mined patterns can be
// compared across services and deployments.
type NodeType = string

const (
	NodeCause       NodeType = "cause"
	NodeMechanism   NodeType = "mechanism"
	NodeSymptom     NodeType = "symptom"
	NodeObservation NodeType = "observation"
	NodeAction      NodeType = "action"
	NodeOutcome     NodeType = "outcome"

	// Source-specific evidence names are compatibility aliases only. Persisted
	// node types use the canonical observation vocabulary.
	NodeMetric          = NodeObservation
	NodeLogPattern      = NodeObservation
	NodeTracePattern    = NodeObservation
	NodeKubernetesEvent = NodeObservation
	NodeRecoveryResult  = NodeOutcome
)

const (
	StatusDiscovered    = "DISCOVERED"
	StatusValidating    = "VALIDATING"
	StatusAccepted      = "ACCEPTED"
	StatusRejected      = "REJECTED"
	CandidateDiscovered = StatusDiscovered
	CandidateValidating = StatusValidating
	CandidateAccepted   = StatusAccepted
	CandidateRejected   = StatusRejected
)

type CausalNode = domain.CausalNode
type CausalEdge = domain.CausalEdge

type IncidentCausalGraph struct {
	IncidentID string       `json:"incident_id"`
	Nodes      []CausalNode `json:"nodes"`
	Edges      []CausalEdge `json:"edges"`
}

type CausalPatternCandidate struct {
	PatternID            string    `json:"pattern_id"`
	CausalPath           []string  `json:"causal_path"`
	SupportingIncidents  []string  `json:"supporting_incidents"`
	Frequency            int       `json:"frequency"`
	Coverage             float64   `json:"coverage"`
	EvidenceConfidence   float64   `json:"evidence_confidence"`
	CausalConsistency    float64   `json:"causal_consistency"`
	ContradictionPenalty float64   `json:"contradiction_penalty"`
	Confidence           float64   `json:"confidence"`
	Contradictions       []string  `json:"contradictions,omitempty"`
	Status               string    `json:"status"`
	Explanation          string    `json:"explanation,omitempty"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// Reader is the read-only boundary exposed to Diagnosis tools. Discovery and
// persistence are deliberately not part of the Agent capability surface.
type Reader interface {
	List(context.Context, string, int) ([]CausalPatternCandidate, error)
	Search(context.Context, []string, int) ([]CausalPatternCandidate, error)
}

type Store interface {
	Reader
	Upsert(context.Context, CausalPatternCandidate) error
}

func PatternID(path []string) string {
	canonical := make([]string, 0, len(path))
	for _, node := range path {
		canonical = append(canonical, normalizeName(node))
	}
	hash := sha256.Sum256([]byte(strings.Join(canonical, "->")))
	return "discovered-causal-" + hex.EncodeToString(hash[:8])
}

func NormalizeCandidate(candidate CausalPatternCandidate) CausalPatternCandidate {
	path := make([]string, 0, len(candidate.CausalPath))
	for _, node := range candidate.CausalPath {
		if value := normalizeName(node); value != "" {
			path = append(path, value)
		}
	}
	candidate.CausalPath = path
	candidate.PatternID = PatternID(path)
	candidate.SupportingIncidents = unique(candidate.SupportingIncidents)
	candidate.Contradictions = unique(candidate.Contradictions)
	if candidate.Status == "" {
		candidate.Status = StatusDiscovered
	}
	if candidate.CreatedAt.IsZero() {
		candidate.CreatedAt = time.Now().UTC()
	}
	if candidate.UpdatedAt.IsZero() {
		candidate.UpdatedAt = candidate.CreatedAt
	}
	return candidate
}

func normalizeName(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(value))), " ")
}

func unique(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

// MarshalPath is used by stores and tests to keep JSONB representation stable.
func MarshalPath(path []string) []byte {
	raw, _ := json.Marshal(path)
	return raw
}
