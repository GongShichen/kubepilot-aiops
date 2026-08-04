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

type CausalNode struct {
	ID   string `json:"id"`
	Type string `json:"type"` // cause, symptom, evidence, action
	Name string `json:"name"`
}

type CausalEdge struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	Relation string `json:"relation"` // causes, supports, contradicts
}

type CausalGraph struct {
	Nodes []CausalNode `json:"nodes"`
	Edges []CausalEdge `json:"edges"`
}

type EvidencePattern struct {
	Source string   `json:"source"`
	Type   string   `json:"type"`
	Tokens []string `json:"tokens,omitempty"`
}

type CausalPattern struct {
	PatternID             string            `json:"pattern_id"`
	Cause                 string            `json:"cause"`
	CausalGraph           CausalGraph       `json:"causal_graph"`
	SupportingEvidence    []EvidencePattern `json:"supporting_evidence,omitempty"`
	ContradictingEvidence []EvidencePattern `json:"contradicting_evidence,omitempty"`
	Confidence            float64           `json:"confidence"`
	SourceIncidents       []string          `json:"source_incidents,omitempty"`
	CreatedAt             time.Time         `json:"created_at"`
	UpdatedAt             time.Time         `json:"updated_at"`
	Status                string            `json:"status"` // pending, active, disabled
}

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
	copyPattern.PatternID = ""
	copyPattern.Confidence = 0
	copyPattern.SourceIncidents = nil
	copyPattern.CreatedAt = time.Time{}
	copyPattern.UpdatedAt = time.Time{}
	copyPattern.Status = ""
	raw, _ := json.Marshal(copyPattern)
	hash := sha256.Sum256(raw)
	return "causal-" + hex.EncodeToString(hash[:8])
}

func Canonicalize(pattern CausalPattern) CausalPattern {
	pattern.Cause = strings.ToLower(strings.TrimSpace(pattern.Cause))
	for i := range pattern.CausalGraph.Nodes {
		pattern.CausalGraph.Nodes[i].ID = strings.TrimSpace(pattern.CausalGraph.Nodes[i].ID)
		pattern.CausalGraph.Nodes[i].Type = strings.ToLower(strings.TrimSpace(pattern.CausalGraph.Nodes[i].Type))
		pattern.CausalGraph.Nodes[i].Name = strings.ToLower(strings.TrimSpace(pattern.CausalGraph.Nodes[i].Name))
	}
	sort.Slice(pattern.CausalGraph.Nodes, func(i, j int) bool { return pattern.CausalGraph.Nodes[i].ID < pattern.CausalGraph.Nodes[j].ID })
	for i := range pattern.CausalGraph.Edges {
		pattern.CausalGraph.Edges[i].Relation = strings.ToLower(strings.TrimSpace(pattern.CausalGraph.Edges[i].Relation))
	}
	sort.Slice(pattern.CausalGraph.Edges, func(i, j int) bool {
		return pattern.CausalGraph.Edges[i].Source+pattern.CausalGraph.Edges[i].Target+pattern.CausalGraph.Edges[i].Relation < pattern.CausalGraph.Edges[j].Source+pattern.CausalGraph.Edges[j].Target+pattern.CausalGraph.Edges[j].Relation
	})
	pattern.SupportingEvidence = uniqueEvidence(pattern.SupportingEvidence)
	pattern.ContradictingEvidence = uniqueEvidence(pattern.ContradictingEvidence)
	pattern.PatternID = PatternID(pattern)
	return pattern
}

func Merge(left, right CausalPattern) CausalPattern {
	if left.PatternID == "" {
		left = right
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
	left.Confidence = confidence(len(left.SourceIncidents), left.Confidence)
	if len(left.SourceIncidents) >= 2 && left.Confidence >= .80 {
		left.Status = "active"
	} else if left.Status != "disabled" {
		left.Status = "pending"
	}
	return left
}

func confidence(support int, prior float64) float64 {
	if prior < 0 {
		prior = 0
	}
	if prior > 1 {
		prior = 1
	}
	// A first observation remains tentative; repeated independent resolved
	// Incidents asymptotically approach high confidence.
	value := .25*prior + .75*(1-expApprox(-float64(support)/4))
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}
func expApprox(v float64) float64 {
	term, sum := 1.0, 1.0
	for i := 1; i < 18; i++ {
		term *= v / float64(i)
		sum += term
	}
	return sum
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
		edges = append(edges, CausalEdge{Source: nodes[i-1].ID, Target: nodes[i].ID, Relation: "causes"})
	}
	if in.Proposal != nil && in.Proposal.Action != "" {
		actionID := "a0"
		nodes = append(nodes, CausalNode{ID: actionID, Type: "action", Name: string(in.Proposal.Action)})
		edges = append(edges, CausalEdge{Source: nodes[len(path)-1].ID, Target: actionID, Relation: "causes"})
	}
	for i, item := range supporting {
		id := fmt.Sprintf("e%d", i)
		nodes = append(nodes, CausalNode{ID: id, Type: "evidence", Name: item.Type})
		edges = append(edges, CausalEdge{Source: id, Target: nodes[minInt(i, len(path)-1)].ID, Relation: "supports"})
	}
	pattern := Canonicalize(CausalPattern{Cause: cause, CausalGraph: CausalGraph{Nodes: nodes, Edges: edges}, SupportingEvidence: supporting, Confidence: prior, SourceIncidents: []string{in.ID}, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), Status: "pending"})
	return Proposal{Pattern: pattern, IncidentID: in.ID}, true
}
func minInt(a, b int) int {
	if a < b {
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
	old, ok := s.patterns[p.PatternID]
	if ok {
		p = Merge(old, p)
	} else {
		p.Confidence = confidence(len(p.SourceIncidents), p.Confidence)
		if p.Confidence >= .8 {
			p.Status = "active"
		} else {
			p.Status = "pending"
		}
	}
	s.patterns[p.PatternID] = p
	return p, nil
}
