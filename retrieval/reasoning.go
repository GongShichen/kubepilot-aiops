package retrieval

import (
	"context"
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
	Engine   *reasoning.Engine
	Reranker reranker.Service
}

func (h IncidentRetrievalEngine) Search(ctx context.Context, features domain.IncidentFeatures) (reasoning.CandidateLists, []domain.RetrievalCandidate, error) {
	var semantic, lexical []domain.RetrievalCandidate
	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error { var err error; semantic, err = h.Semantic(groupCtx, features, 50); return err })
	group.Go(func() error { var err error; lexical, err = h.Lexical(groupCtx, features, 50); return err })
	if err := group.Wait(); err != nil {
		return reasoning.CandidateLists{}, nil, err
	}
	// Topology remains an explicit ReAct capability through Topology(), but it
	// is not a candidate generator in the production facade.
	lists := reasoning.CandidateLists{Semantic: semantic, Lexical: lexical}
	config := DefaultPipelineConfig()
	candidates := GenerateCandidates(lists, config)
	if h.Engine == nil {
		return lists, candidates, nil
	}
	reasoningCandidates := RerankReasoning(features, candidates, config)
	if h.Reranker != nil && h.Reranker.Enabled() {
		final, err := RerankNeural(ctx, h.Reranker, features, reasoningCandidates, config)
		if err != nil {
			return lists, nil, err
		}
		return lists, final, nil
	}
	return lists, reasoningCandidates, nil
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
		out = append(out, domain.RetrievalCandidate{IncidentID: doc.ID, Namespace: doc.Namespace, Service: doc.Service, Category: doc.Category, RootCause: doc.RootCause, Summary: doc.Template, Features: domain.IncidentFeatures{Namespace: doc.Namespace, Service: doc.Service, Terms: strings.Fields(strings.ToLower(doc.Template + " " + doc.RootCause)), TopologyServices: []string{doc.Service}}, SourceScores: map[string]float64{"semantic": score}})
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
