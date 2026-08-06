package memory

import (
	"context"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/reasoning"
)

const defaultRecencyHalfLife = 90 * 24 * time.Hour

type HistoricalReader interface {
	Semantic(context.Context, domain.IncidentFeatures, int) ([]domain.RetrievalCandidate, error)
	Lexical(context.Context, domain.IncidentFeatures, int) ([]domain.RetrievalCandidate, error)
}

type CausalReader interface {
	ListCausalPatterns(context.Context, string) ([]domain.CausalPattern, error)
}

type AccessRecorder interface {
	RecordMemoryAccess(context.Context, domain.MemoryAccessEvent) error
}

type VerifiedIncidentWriter interface {
	WriteVerifiedIncident(context.Context, domain.IncidentLearningInput) error
}

type Service struct {
	Historical HistoricalReader
	Causal     CausalReader
	Reasoning  *reasoning.Engine
	Recorder   AccessRecorder
	Writer     VerifiedIncidentWriter
	Procedures []domain.MemoryResult
	HalfLife   time.Duration
}

func (s *Service) WriteVerifiedIncident(ctx context.Context, input domain.IncidentLearningInput) error {
	if s.Writer == nil || input.Incident == nil {
		return nil
	}
	return s.Writer.WriteVerifiedIncident(ctx, input)
}

func (s *Service) Read(ctx context.Context, query domain.MemoryQuery) ([]domain.MemoryResult, error) {
	limit := query.Limit
	if limit <= 0 || limit > 20 {
		limit = 5
	}
	var results []domain.MemoryResult
	var err error
	switch query.Kind {
	case domain.MemoryEpisodic:
		results, err = s.readEpisodic(ctx, query, limit)
	case domain.MemorySemantic:
		results, err = s.readSemantic(ctx, query, limit)
	case domain.MemoryProcedural:
		results = s.readProcedural(query, limit)
	case domain.MemoryWorking:
		// Working memory is the checkpointed WorkflowState and is never queried
		// as an external retrieval corpus.
		return []domain.MemoryResult{}, nil
	default:
		return []domain.MemoryResult{}, nil
	}
	if err != nil {
		return nil, err
	}
	halfLife := s.HalfLife
	if halfLife <= 0 {
		halfLife = defaultRecencyHalfLife
	}
	for index := range results {
		results[index].Score = recencyAdjusted(results[index].Score, results[index].ObservedAt, halfLife)
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score == results[j].Score {
			return results[i].ID < results[j].ID
		}
		return results[i].Score > results[j].Score
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func (s *Service) RecordAccess(ctx context.Context, event domain.MemoryAccessEvent) error {
	if s.Recorder == nil {
		return nil
	}
	return s.Recorder.RecordMemoryAccess(ctx, event)
}

func (s *Service) readEpisodic(ctx context.Context, query domain.MemoryQuery, limit int) ([]domain.MemoryResult, error) {
	if s.Historical == nil {
		return []domain.MemoryResult{}, nil
	}
	features := domain.IncidentFeatures{IncidentID: query.IncidentID, Cluster: query.Scope.Cluster, Namespace: query.Scope.Namespace, Terms: query.Terms}
	if len(query.Terms) > 0 {
		features.Service = query.Terms[0]
	}
	semantic, err := s.Historical.Semantic(ctx, features, max(limit*4, 20))
	if err != nil {
		return nil, err
	}
	lexical, err := s.Historical.Lexical(ctx, features, max(limit*4, 20))
	if err != nil {
		return nil, err
	}
	engine := s.Reasoning
	if engine == nil {
		engine = reasoning.New(reasoning.DefaultConfig())
	}
	candidates := engine.Fuse(reasoning.CandidateLists{Semantic: semantic, Lexical: lexical})
	candidates = engine.Rerank(features, candidates)
	results := make([]domain.MemoryResult, 0, len(candidates))
	for _, candidate := range candidates {
		if query.Scope.Cluster != "" && candidate.Cluster != "" && candidate.Cluster != query.Scope.Cluster {
			continue
		}
		if candidate.Namespace != "" && candidate.Namespace != query.Scope.Namespace {
			continue
		}
		results = append(results, domain.MemoryResult{ID: candidate.IncidentID, Kind: domain.MemoryEpisodic, Scope: query.Scope, Summary: strings.TrimSpace(candidate.Summary + " " + candidate.RootCause), Score: candidate.Rank.FinalScore, Version: candidate.Revision, Provenance: map[string]any{"category": candidate.Category, "service": candidate.Service}})
	}
	return results, nil
}

func (s *Service) readSemantic(ctx context.Context, query domain.MemoryQuery, limit int) ([]domain.MemoryResult, error) {
	if s.Causal == nil {
		return []domain.MemoryResult{}, nil
	}
	patterns, err := s.Causal.ListCausalPatterns(ctx, "active")
	if err != nil {
		return nil, err
	}
	terms := strings.ToLower(strings.Join(query.Terms, " "))
	var results []domain.MemoryResult
	for _, pattern := range patterns {
		if pattern.Cluster != "" && pattern.Cluster != query.Scope.Cluster {
			continue
		}
		if pattern.Namespace != "" && pattern.Namespace != query.Scope.Namespace {
			continue
		}
		text := strings.ToLower(pattern.Category + " " + pattern.Cause)
		if terms != "" && !hasTermOverlap(terms, text) {
			continue
		}
		results = append(results, domain.MemoryResult{ID: pattern.ID, Kind: domain.MemorySemantic, Scope: query.Scope, Summary: pattern.Cause, Score: pattern.Confidence, Version: strconv.Itoa(pattern.Version), ObservedAt: pattern.UpdatedAt, Provenance: map[string]any{"source": pattern.Source, "support_count": pattern.SupportCount}})
		if len(results) >= limit*2 {
			break
		}
	}
	return results, nil
}

func (s *Service) readProcedural(query domain.MemoryQuery, limit int) []domain.MemoryResult {
	terms := strings.ToLower(strings.Join(query.Terms, " "))
	var results []domain.MemoryResult
	for _, item := range s.Procedures {
		if query.Scope.Cluster != "" && item.Scope.Cluster != "" && item.Scope.Cluster != query.Scope.Cluster {
			continue
		}
		if item.Scope.Namespace != "" && item.Scope.Namespace != query.Scope.Namespace {
			continue
		}
		if terms != "" && !hasTermOverlap(terms, strings.ToLower(item.Summary)) {
			continue
		}
		item.Kind = domain.MemoryProcedural
		results = append(results, item)
		if len(results) == limit {
			break
		}
	}
	return results
}

func recencyAdjusted(score float64, observedAt time.Time, halfLife time.Duration) float64 {
	if observedAt.IsZero() || halfLife <= 0 {
		return score
	}
	age := time.Since(observedAt)
	if age <= 0 {
		return score
	}
	return score * math.Pow(.5, float64(age)/float64(halfLife))
}

func hasTermOverlap(left, right string) bool {
	for _, term := range strings.Fields(left) {
		if len(term) > 2 && strings.Contains(right, term) {
			return true
		}
	}
	return false
}

func max(left, right int) int {
	if left > right {
		return left
	}
	return right
}
