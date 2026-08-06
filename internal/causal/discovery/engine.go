package discovery

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	causalknowledge "github.com/kubepilot-aiops/kubepilot/internal/causal/knowledge"
	causalvalidator "github.com/kubepilot-aiops/kubepilot/internal/causal/validator"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

type Engine struct {
	Candidates Store
	Knowledge  causalknowledge.Reader
	Patterns   causalknowledge.PatternStore
	Explainer  ExplanationProvider
	Extractor  Extractor
	Miner      PatternMiner
}

// ExplanationProvider is optional and has no authority over validation or
// persistence. A model-backed implementation may describe a candidate, but
// the candidate status remains exclusively deterministic.
type ExplanationProvider interface {
	Explain(context.Context, CausalPatternCandidate) (string, error)
}

type ExplainFunc func(context.Context, CausalPatternCandidate) (string, error)

func (f ExplainFunc) Explain(ctx context.Context, candidate CausalPatternCandidate) (string, error) {
	return f(ctx, candidate)
}

func NewEngine(candidates Store, knowledge causalknowledge.Reader) *Engine {
	return &Engine{Candidates: candidates, Knowledge: knowledge, Extractor: NewExtractor(), Miner: NewPatternMiner()}
}

// ValidateCandidate applies the discovery-specific multi-Incident gates. The
// Engine additionally invokes the existing CausalPatternValidator when a
// knowledge Reader is supplied.
func ValidateCandidate(ctx context.Context, candidate CausalPatternCandidate, graphs map[string]IncidentCausalGraph, incidents map[string]*domain.Incident, knowledge causalknowledge.Reader) bool {
	return validCandidate(ctx, NormalizeCandidate(candidate), graphs, incidents, knowledge)
}

func (e *Engine) Discover(ctx context.Context, incidents []*domain.Incident) ([]CausalPatternCandidate, error) {
	if e == nil || e.Candidates == nil {
		return nil, errors.New("causal discovery candidate store unavailable")
	}
	graphs, err := e.Extractor.ExtractMany(ctx, incidents)
	if err != nil {
		return nil, err
	}
	candidates := e.Miner.Mine(graphs)
	byID := map[string]*domain.Incident{}
	for _, incident := range incidents {
		if incident != nil {
			byID[incident.ID] = incident
		}
	}
	graphByID := map[string]IncidentCausalGraph{}
	for _, graph := range graphs {
		graphByID[graph.IncidentID] = graph
	}
	for index := range candidates {
		candidate := NormalizeCandidate(candidates[index])
		candidate.Status = StatusDiscovered
		candidate.UpdatedAt = nowUTC()
		if err := e.Candidates.Upsert(ctx, candidate); err != nil {
			return nil, err
		}
		candidate.Status = StatusValidating
		candidate.UpdatedAt = nowUTC()
		if e.Explainer != nil {
			if explanation, explainErr := e.Explainer.Explain(ctx, candidate); explainErr == nil {
				candidate.Explanation = explanation
			}
		}
		if err := e.Candidates.Upsert(ctx, candidate); err != nil {
			return nil, err
		}
		if validCandidate(ctx, candidate, graphByID, byID, e.Knowledge) {
			candidate.Status = StatusAccepted
			if err := e.mergeAcceptedPattern(ctx, candidate, graphByID, byID); err != nil {
				return nil, err
			}
		} else {
			candidate.Status = StatusRejected
		}
		if err := e.Candidates.Upsert(ctx, candidate); err != nil {
			return nil, err
		}
		candidates[index] = candidate
	}
	return candidates, nil
}

func (e *Engine) mergeAcceptedPattern(ctx context.Context, candidate CausalPatternCandidate, graphs map[string]IncidentCausalGraph, incidents map[string]*domain.Incident) error {
	if e.Patterns == nil || len(candidate.SupportingIncidents) == 0 {
		return nil
	}
	anchor := incidents[candidate.SupportingIncidents[0]]
	if anchor == nil {
		return nil
	}
	evidenceIDs := graphEvidenceIDs(graphs[anchor.ID])
	proposal, ok := causalknowledge.ProposalFromDraft(anchor, candidate.CausalPath[0], candidate.CausalPath, evidenceIDs, candidate.Confidence)
	if !ok {
		return nil
	}
	proposal.Pattern.Status = "active"
	proposal.Pattern.Confidence = candidate.Confidence
	proposal.Pattern.SourceIncidents = unique(candidate.SupportingIncidents)
	_, err := e.Patterns.Merge(ctx, proposal.Pattern)
	return err
}

func validCandidate(ctx context.Context, candidate CausalPatternCandidate, graphs map[string]IncidentCausalGraph, incidents map[string]*domain.Incident, knowledge causalknowledge.Reader) bool {
	if len(candidate.CausalPath) < 2 || len(candidate.SupportingIncidents) < 3 || (candidate.Frequency > 0 && float64(len(candidate.Contradictions))/float64(candidate.Frequency) > .10) {
		return false
	}
	if candidate.Confidence <= 0 {
		return false
	}
	for _, incidentID := range candidate.SupportingIncidents {
		graph, ok := graphs[incidentID]
		if !ok || !graphHasCausalPath(graph, candidate.CausalPath) {
			return false
		}
		if len(graphEvidenceTypes(graph)) < 2 {
			return false
		}
	}
	if knowledge == nil {
		return true
	}
	anchor := incidents[candidate.SupportingIncidents[0]]
	if anchor == nil {
		return false
	}
	evidenceIDs := graphEvidenceIDs(graphs[anchor.ID])
	proposal, ok := causalknowledge.ProposalFromDraft(anchor, candidate.CausalPath[0], candidate.CausalPath, evidenceIDs, candidate.Confidence)
	if !ok {
		return false
	}
	validator := causalvalidator.New(knowledge)
	// Existing Validator supplies structural, evidence-grounding and
	// contradiction checks for the anchor Incident. Cross-Incident support is
	// intentionally taken from the mined candidate, not from the legacy
	// accepted-pattern store, otherwise a genuinely new pattern could never be
	// discovered.
	validator.MinimumSupport = 1
	result, err := validator.Validate(ctx, anchor, proposal)
	return err == nil && result.Valid && result.Contradiction <= .10
}

func graphHasCausalPath(graph IncidentCausalGraph, path []string) bool {
	for _, mined := range causalPaths(graph, 6) {
		if pathKey(mined) == pathKey(path) {
			return true
		}
	}
	return false
}

func graphEvidenceTypes(graph IncidentCausalGraph) map[string]bool {
	result := map[string]bool{}
	for _, node := range graph.Nodes {
		if node.Type == NodeObservation && node.Source != "" && len(node.SourceEvidenceIDs) > 0 {
			result[node.Source] = true
		}
	}
	return result
}

func graphEvidenceIDs(graph IncidentCausalGraph) []string {
	ids := []string{}
	for _, node := range graph.Nodes {
		for _, id := range node.SourceEvidenceIDs {
			if !contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

type MemoryStore struct {
	mu         sync.RWMutex
	candidates map[string]CausalPatternCandidate
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{candidates: map[string]CausalPatternCandidate{}}
}

func (s *MemoryStore) Upsert(_ context.Context, candidate CausalPatternCandidate) error {
	if s == nil {
		return errors.New("candidate store unavailable")
	}
	candidate = NormalizeCandidate(candidate)
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.candidates[candidate.PatternID]; ok {
		if candidate.CreatedAt.IsZero() {
			candidate.CreatedAt = old.CreatedAt
		}
	}
	s.candidates[candidate.PatternID] = candidate
	return nil
}

func (s *MemoryStore) List(_ context.Context, status string, limit int) ([]CausalPatternCandidate, error) {
	if s == nil {
		return nil, errors.New("candidate store unavailable")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]CausalPatternCandidate, 0, len(s.candidates))
	for _, candidate := range s.candidates {
		if status == "" || candidate.Status == status {
			items = append(items, candidate)
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Confidence != items[j].Confidence {
			return items[i].Confidence > items[j].Confidence
		}
		return items[i].PatternID < items[j].PatternID
	})
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *MemoryStore) Search(ctx context.Context, terms []string, limit int) ([]CausalPatternCandidate, error) {
	items, err := s.List(ctx, StatusAccepted, 0)
	if err != nil {
		return nil, err
	}
	if len(terms) == 0 {
		if limit > 0 && len(items) > limit {
			items = items[:limit]
		}
		return items, nil
	}
	filtered := make([]CausalPatternCandidate, 0, len(items))
	for _, item := range items {
		text := strings.Join(item.CausalPath, " ")
		matched := false
		for _, term := range terms {
			if strings.Contains(text, strings.ToLower(strings.TrimSpace(term))) {
				matched = true
				break
			}
		}
		if matched {
			filtered = append(filtered, item)
		}
	}
	if limit > 0 && len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered, nil
}

func nowUTC() time.Time { return time.Now().UTC() }
