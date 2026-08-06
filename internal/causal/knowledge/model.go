package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

type CausalNode = domain.CausalNode
type CausalEdge = domain.CausalEdge
type CausalGraph = domain.CausalGraph
type EvidencePattern = domain.CausalEvidencePattern
type CausalPattern = domain.CausalPattern

type Proposal struct {
	Pattern    CausalPattern `json:"pattern"`
	IncidentID string        `json:"incident_id"`
}

type ValidationResult struct {
	Valid         bool     `json:"valid"`
	Accepted      bool     `json:"accepted"`
	PatternID     string   `json:"pattern_id"`
	SupportCount  int      `json:"support_count"`
	Confidence    float64  `json:"confidence"`
	Contradiction float64  `json:"contradiction"`
	FailedChecks  []string `json:"failed_checks,omitempty"`
	Reason        string   `json:"reason,omitempty"`
}

type Reader interface {
	List(context.Context, string, int) ([]CausalPattern, error)
}

type PatternStore interface {
	Reader
	Merge(context.Context, CausalPattern) (CausalPattern, error)
}

func PatternID(pattern CausalPattern) string {
	copyPattern := pattern
	// Nodes and edges are slices. Copy them before removing incident-specific
	// fields so identity calculation can never mutate the audited graph held by
	// the caller.
	copyPattern.Nodes = append([]CausalNode(nil), pattern.Nodes...)
	copyPattern.Edges = append([]CausalEdge(nil), pattern.Edges...)
	copyPattern.ID = ""
	copyPattern.Category = ""
	copyPattern.Source = ""
	copyPattern.Confidence = 0
	copyPattern.SourceIncidents = nil
	copyPattern.SupportingEvidence = nil
	copyPattern.ContradictingEvidence = nil
	copyPattern.CreatedAt = time.Time{}
	copyPattern.UpdatedAt = time.Time{}
	copyPattern.Status = ""
	copyPattern.Version = 0
	copyPattern.SupportCount = 0
	for index := range copyPattern.Nodes {
		copyPattern.Nodes[index].Confidence = 0
		copyPattern.Nodes[index].SourceEvidenceIDs = nil
	}
	for index := range copyPattern.Edges {
		copyPattern.Edges[index].Confidence = 0
	}
	raw, _ := json.Marshal(copyPattern)
	hash := sha256.Sum256(raw)
	return "causal-" + hex.EncodeToString(hash[:8])
}

func Canonicalize(pattern CausalPattern) CausalPattern {
	pattern.Cause = strings.ToLower(strings.TrimSpace(pattern.Cause))
	for i := range pattern.Nodes {
		pattern.Nodes[i].ID = strings.TrimSpace(pattern.Nodes[i].ID)
		pattern.Nodes[i].Type = strings.ToLower(strings.TrimSpace(pattern.Nodes[i].Type))
		pattern.Nodes[i].Name = strings.ToLower(strings.TrimSpace(pattern.Nodes[i].Name))
	}
	sort.Slice(pattern.Nodes, func(i, j int) bool { return pattern.Nodes[i].ID < pattern.Nodes[j].ID })
	for i := range pattern.Edges {
		pattern.Edges[i].Relation = strings.ToLower(strings.TrimSpace(pattern.Edges[i].Relation))
	}
	sort.Slice(pattern.Edges, func(i, j int) bool {
		return pattern.Edges[i].From+pattern.Edges[i].To+pattern.Edges[i].Relation < pattern.Edges[j].From+pattern.Edges[j].To+pattern.Edges[j].Relation
	})
	pattern.SupportingEvidence = uniqueEvidence(pattern.SupportingEvidence)
	pattern.ContradictingEvidence = uniqueEvidence(pattern.ContradictingEvidence)
	pattern.ID = PatternID(pattern)
	return pattern
}

func Merge(left, right CausalPattern) CausalPattern {
	hasExisting := left.ID != ""
	if !hasExisting {
		left = right
	}
	nextVersion := maxInt(left.Version, right.Version)
	if hasExisting {
		nextVersion++
	} else if nextVersion <= 0 {
		nextVersion = 1
	}
	leftSupport := len(unique(left.SourceIncidents))
	rightSupport := 0
	leftSeen := map[string]bool{}
	for _, incidentID := range left.SourceIncidents {
		leftSeen[incidentID] = true
	}
	for _, incidentID := range right.SourceIncidents {
		if incidentID != "" && !leftSeen[incidentID] {
			rightSupport++
		}
	}
	combinedConfidence := left.Confidence
	if leftSupport+rightSupport > 0 {
		combinedConfidence = (left.Confidence*float64(leftSupport) + right.Confidence*float64(rightSupport)) / float64(leftSupport+rightSupport)
	}
	left = Canonicalize(left)
	left.SourceIncidents = unique(append(left.SourceIncidents, right.SourceIncidents...))
	left.SupportingEvidence = uniqueEvidence(append(left.SupportingEvidence, right.SupportingEvidence...))
	left.ContradictingEvidence = uniqueEvidence(append(left.ContradictingEvidence, right.ContradictingEvidence...))
	if right.UpdatedAt.After(left.UpdatedAt) {
		left.UpdatedAt = right.UpdatedAt
	}
	if left.CreatedAt.IsZero() {
		left.CreatedAt = right.CreatedAt
	}
	left.Confidence = combinedConfidence
	left.SupportCount = len(left.SourceIncidents)
	left.Version = nextVersion
	if eligibleForActivation(left) {
		left.Status = "active"
	} else if left.Status != "disabled" {
		left.Status = "validating"
	}
	return left
}

func eligibleForActivation(pattern CausalPattern) bool {
	if len(unique(pattern.SourceIncidents)) < 3 || pattern.Confidence < .80 {
		return false
	}
	sources := map[string]bool{}
	for _, evidence := range pattern.SupportingEvidence {
		if evidence.Source != "" {
			sources[evidence.Source] = true
		}
	}
	if len(sources) < 2 {
		return false
	}
	return float64(len(pattern.ContradictingEvidence))/float64(maxInt(1, len(pattern.SupportingEvidence))) <= .10
}
func unique(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, v := range values {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}
func uniqueEvidence(values []EvidencePattern) []EvidencePattern {
	seen := map[string]bool{}
	out := []EvidencePattern{}
	for _, v := range values {
		key := v.Source + ":" + v.Type + ":" + strings.Join(v.Tokens, " ")
		if !seen[key] {
			seen[key] = true
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Source+out[i].Type < out[j].Source+out[j].Type })
	return out
}

// ProposalFromIncident extracts a candidate from the accepted Diagnosis
// ledger. It is deterministic and does not write to a store; an extractor or
// service must validate it before merge.
func ProposalFromIncident(in *domain.Incident) (Proposal, bool) {
	if in == nil || in.Status != domain.StatusResolved || in.DiagnosisLedger == nil || in.DiagnosisLedger.SelectedHypothesisID == "" {
		return Proposal{}, false
	}
	var selected *domain.VerifiedHypothesis
	for i := range in.DiagnosisLedger.Verified {
		if in.DiagnosisLedger.Verified[i].Draft.ID == in.DiagnosisLedger.SelectedHypothesisID {
			selected = &in.DiagnosisLedger.Verified[i]
			break
		}
	}
	if selected == nil || selected.FinalScore < .80 || selected.ContradictionScore > .10 {
		return Proposal{}, false
	}
	if len(selected.VerifiedEvidenceIDs) < 2 || len(selected.Draft.ExpectedCausalPath) < 2 {
		return Proposal{}, false
	}
	return ProposalFromDraft(in, selected.Draft.Cause, selected.Draft.ExpectedCausalPath, selected.VerifiedEvidenceIDs, selected.FinalScore)
}

// ProposalFromDraft accepts an Agent-generated causal hypothesis without
// granting it persistence authority. Evidence IDs are resolved against the
// server-owned Incident before a proposal is returned.
func ProposalFromDraft(in *domain.Incident, cause string, path, evidenceIDs []string, prior float64) (Proposal, bool) {
	if in == nil || strings.TrimSpace(cause) == "" || len(path) < 2 {
		return Proposal{}, false
	}
	evidence := map[string]domain.Evidence{}
	for _, item := range in.Evidence {
		evidence[item.ID] = item
	}
	sources := map[string]bool{}
	supporting := []EvidencePattern{}
	supportingIDs := []string{}
	confidenceTotal := 0.0
	for _, id := range evidenceIDs {
		item, ok := evidence[id]
		if !ok {
			continue
		}
		src := item.Source
		if src == "" {
			src = item.Type
		}
		sources[src] = true
		tokens := strings.Fields(strings.ToLower(item.Summary))
		supporting = append(supporting, EvidencePattern{Source: src, Type: item.Type, Tokens: tokens[:minInt(6, len(tokens))]})
		supportingIDs = append(supportingIDs, item.ID)
		quality := item.Confidence
		if item.Attribution != nil && item.Attribution.AttributionScore > quality {
			quality = item.Attribution.AttributionScore
		}
		if quality <= 0 {
			quality = prior
		}
		confidenceTotal += quality
	}
	if len(sources) < 2 {
		return Proposal{}, false
	}
	nodes := []CausalNode{}
	for i, name := range path {
		typ := "symptom"
		if i == 0 {
			typ = "cause"
		}
		nodes = append(nodes, CausalNode{ID: fmt.Sprintf("n%d", i), Type: typ, Name: name})
	}
	edges := []CausalEdge{}
	for i := 1; i < len(nodes); i++ {
		edges = append(edges, CausalEdge{From: nodes[i-1].ID, To: nodes[i].ID, Relation: "causes"})
	}
	if in.Proposal != nil && in.Proposal.Action != "" {
		actionID := "a0"
		nodes = append(nodes, CausalNode{ID: actionID, Type: "action", Name: string(in.Proposal.Action)})
		edges = append(edges, CausalEdge{From: actionID, To: nodes[0].ID, Relation: "mitigates"})
	}
	for i, item := range supporting {
		id := fmt.Sprintf("e%d", i)
		nodes = append(nodes, CausalNode{ID: id, Type: "observation", Name: item.Type, Source: item.Source, SourceEvidenceIDs: []string{supportingIDs[i]}})
		edges = append(edges, CausalEdge{From: id, To: nodes[minInt(i, len(path)-1)].ID, Relation: "supports"})
	}
	averageConfidence := prior
	if len(supporting) > 0 {
		averageConfidence = confidenceTotal / float64(len(supporting))
	}
	pattern := Canonicalize(CausalPattern{Cause: cause, Nodes: nodes, Edges: edges, SupportingEvidence: supporting, Cluster: in.Cluster, Namespace: in.Namespace, Source: "learned", Confidence: averageConfidence, SourceIncidents: []string{in.ID}, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), Status: "validating", Version: 1, SupportCount: 1})
	return Proposal{Pattern: pattern, IncidentID: in.ID}, true
}
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

type MemoryStore struct {
	mu       sync.RWMutex
	patterns map[string]CausalPattern
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{patterns: map[string]CausalPattern{}} }
func (s *MemoryStore) List(_ context.Context, status string, limit int) ([]CausalPattern, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []CausalPattern{}
	for _, p := range s.patterns {
		if status != "" && p.Status != status {
			continue
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Confidence > out[j].Confidence })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (s *MemoryStore) Merge(_ context.Context, p CausalPattern) (CausalPattern, error) {
	if s == nil {
		return CausalPattern{}, fmt.Errorf("causal store is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p = Canonicalize(p)
	old, ok := s.patterns[p.ID]
	if ok {
		p = Merge(old, p)
	} else {
		if p.Version <= 0 {
			p.Version = 1
		}
		p.SupportCount = len(unique(p.SourceIncidents))
		if eligibleForActivation(p) {
			p.Status = "active"
		} else {
			p.Status = "candidate"
		}
	}
	s.patterns[p.ID] = p
	return p, nil
}
