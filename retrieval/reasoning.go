package retrieval

import (
	"context"
	"fmt"
	"strings"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/internal/retrieval/reranker"
	"github.com/kubepilot-aiops/kubepilot/reasoning"
	"golang.org/x/sync/errgroup"
)

type StructuredKnowledge interface {
	SearchLexicalIncidents(context.Context, domain.IncidentFeatures, int) ([]domain.RetrievalCandidate, error)
	SearchTopologyIncidents(context.Context, domain.IncidentFeatures, int) ([]domain.RetrievalCandidate, error)
}

type HistoricalRetriever struct {
	Embedder  EmbeddingClient
	Vectors   VectorStore
	Knowledge StructuredKnowledge
}

// IncidentRetrievalEngine is the single production historical-retrieval
// boundary. ReAct tools may still explore topology explicitly, but the
// canonical Search path keeps semantic and lexical retrieval responsible for
// recall. Topology and causal signals are soft reranking features applied only
// after candidate generation.
type IncidentRetrievalEngine struct {
	HistoricalRetriever
	Reranker reranker.Service
}

func (h IncidentRetrievalEngine) Search(ctx context.Context, features domain.IncidentFeatures) (reasoning.CandidateLists, []domain.RetrievalCandidate, error) {
	result, err := h.RunPipeline(ctx, features, DefaultPipelineConfig())
	if err != nil {
		return reasoning.CandidateLists{}, nil, err
	}
	return result.Sources, result.Final, nil
}

// IncidentPipelineResult exposes the auditable stages of the one production
// retrieval pipeline. It is used by runtime telemetry and evaluators alike;
// no caller is expected to reproduce the stage formulas.
type IncidentPipelineResult struct {
	Sources         reasoning.CandidateLists
	Semantic        []domain.RetrievalCandidate
	SemanticLexical []domain.RetrievalCandidate
	Topology        []domain.RetrievalCandidate
	Causal          []domain.RetrievalCandidate
	Final           []domain.RetrievalCandidate
}

// RunPipeline executes the canonical production retrieval implementation and
// returns immutable snapshots after each stage.
func (h IncidentRetrievalEngine) RunPipeline(ctx context.Context, features domain.IncidentFeatures, config PipelineConfig) (IncidentPipelineResult, error) {
	config = normalizePipelineConfig(config)
	var semantic, lexical []domain.RetrievalCandidate
	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error { var err error; semantic, err = h.Semantic(groupCtx, features, 50); return err })
	group.Go(func() error { var err error; lexical, err = h.Lexical(groupCtx, features, 50); return err })
	if err := group.Wait(); err != nil {
		return IncidentPipelineResult{}, err
	}
	semantic = excludeIncident(semantic, features.IncidentID)
	lexical = excludeIncident(lexical, features.IncidentID)
	lists := reasoning.CandidateLists{Semantic: semantic, Lexical: lexical}
	semanticOnly := GenerateCandidates(reasoning.CandidateLists{Semantic: semantic}, config)
	candidates := GenerateCandidates(lists, config)
	topology := RerankTopology(features, candidates, config)
	reasoningCandidates := RerankReasoning(features, candidates, config)
	final := reasoningCandidates
	if h.Reranker != nil && h.Reranker.Enabled() {
		var err error
		final, err = RerankNeural(ctx, h.Reranker, features, reasoningCandidates, config)
		if err != nil {
			return IncidentPipelineResult{}, err
		}
	} else if len(final) > config.FinalTopK {
		final = append([]domain.RetrievalCandidate(nil), final[:config.FinalTopK]...)
	}
	return IncidentPipelineResult{
		Sources: lists, Semantic: cloneCandidates(semanticOnly),
		SemanticLexical: cloneCandidates(candidates), Topology: cloneCandidates(topology),
		Causal: cloneCandidates(reasoningCandidates), Final: cloneCandidates(final),
	}, nil
}

func excludeIncident(items []domain.RetrievalCandidate, incidentID string) []domain.RetrievalCandidate {
	if incidentID == "" {
		return items
	}
	out := make([]domain.RetrievalCandidate, 0, len(items))
	for _, item := range items {
		if item.IncidentID != incidentID {
			out = append(out, item)
		}
	}
	return out
}

func cloneCandidates(items []domain.RetrievalCandidate) []domain.RetrievalCandidate {
	return append([]domain.RetrievalCandidate(nil), items...)
}

func (r HistoricalRetriever) Semantic(ctx context.Context, features domain.IncidentFeatures, limit int) ([]domain.RetrievalCandidate, error) {
	if r.Embedder == nil || r.Vectors == nil {
		return []domain.RetrievalCandidate{}, nil
	}
	query := strings.Join(append([]string{features.Service, features.Resource}, features.Terms...), " ")
	vectors, err := r.Embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, err
	}
	if len(vectors) != 1 || len(vectors[0]) == 0 {
		return nil, fmt.Errorf("embedding provider returned %d vectors for one incident query", len(vectors))
	}
	docs, err := r.Vectors.Search(ctx, vectors[0], map[string]string{"namespace": features.Namespace}, limit)
	if err != nil {
		return nil, err
	}
	out := make([]domain.RetrievalCandidate, 0, len(docs))
	for _, doc := range docs {
		score := doc.Score
		if score == 0 {
			score = .5
		}
		if doc.Service == features.Service {
			score += .1
		}
		out = append(out, domain.RetrievalCandidate{IncidentID: doc.ID, Namespace: doc.Namespace, Service: doc.Service, Category: doc.Category, RootCause: doc.RootCause, Summary: doc.Template, Features: domain.IncidentFeatures{IncidentID: doc.ID, Namespace: doc.Namespace, Service: doc.Service, Terms: strings.Fields(strings.ToLower(doc.Template + " " + doc.RootCause)), TopologyServices: []string{doc.Service}}, SourceScores: map[string]float64{"semantic": score}})
	}
	return out, nil
}

func (r HistoricalRetriever) Lexical(ctx context.Context, features domain.IncidentFeatures, limit int) ([]domain.RetrievalCandidate, error) {
	if r.Knowledge == nil {
		return []domain.RetrievalCandidate{}, nil
	}
	return r.Knowledge.SearchLexicalIncidents(ctx, features, limit)
}

func (r HistoricalRetriever) Topology(ctx context.Context, features domain.IncidentFeatures, limit int) ([]domain.RetrievalCandidate, error) {
	if r.Knowledge == nil {
		return []domain.RetrievalCandidate{}, nil
	}
	return r.Knowledge.SearchTopologyIncidents(ctx, features, limit)
}
