package retrieval

import (
	"context"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/kubepilot-aiops/kubepilot/internal/brainruntime"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/internal/retrieval/reranker"
)

// SkillHybridRetriever performs bounded capability retrieval over the frozen
// Skill snapshot. It selects Skill IDs only; activation and authority remain
// the resolver's responsibility.
type SkillHybridRetriever struct {
	Embedder EmbeddingClient
	Reranker reranker.Service
	mu       sync.RWMutex
	vectors  map[string][]float32
}

func NewSkillHybridRetriever(embedder EmbeddingClient, service reranker.Service) *SkillHybridRetriever {
	return &SkillHybridRetriever{Embedder: embedder, Reranker: service, vectors: map[string][]float32{}}
}

func (r *SkillHybridRetriever) Search(ctx context.Context, query domain.SkillRetrievalQuery) (domain.SkillRetrievalResult, error) {
	limit := query.Limit
	if limit <= 0 || limit > 8 {
		limit = 6
	}
	result := domain.SkillRetrievalResult{QueryHash: brainruntime.Hash(struct {
		IncidentID, Text string
		Phase            domain.BrainPhase
	}{query.IncidentID, query.Text, query.Phase})}
	if len(query.Documents) == 0 {
		result.RetrievedAt = nowUTC()
		result.SnapshotHash = brainruntime.Hash(result)
		return result, nil
	}
	queryTerms := tokenizeSkill(query.Text)
	docTerms := make([][]string, len(query.Documents))
	frequency := map[string]int{}
	for index, document := range query.Documents {
		docTerms[index] = tokenizeSkill(skillDocumentText(document))
		seen := map[string]bool{}
		for _, term := range docTerms[index] {
			if !seen[term] {
				frequency[term]++
				seen[term] = true
			}
		}
	}
	queryVector, documentVectors, vectorUsed, vectorErr := r.embeddingVectors(ctx, query, query.Documents)
	if vectorErr != nil {
		queryVector, documentVectors = nil, nil
	} else {
		result.VectorUsed = vectorUsed
	}
	for index, document := range query.Documents {
		bm25 := skillBM25(queryTerms, docTerms[index], frequency, len(query.Documents))
		vector := 0.0
		if len(queryVector) > 0 && index < len(documentVectors) {
			vector = cosine32(queryVector, documentVectors[index])
		}
		phase := 0.0
		for _, candidate := range document.CompatiblePhases {
			if candidate == query.Phase {
				phase = 1
				break
			}
		}
		result.Results = append(result.Results, domain.SkillSearchResult{ID: document.ID, Version: document.Version, ContentHash: document.ContentHash, Description: document.Description, BM25Score: bm25, VectorScore: vector, PhaseScore: phase, FinalScore: bounded(.50*bm25 + .35*vector + .15*phase)})
	}
	sortSkillResults(result.Results)
	if r.Reranker != nil && r.Reranker.Enabled() && len(result.Results) > 0 {
		docByID := map[string]domain.SkillSearchDocument{}
		for _, document := range query.Documents {
			docByID[document.ID] = document
		}
		documents := make([]string, len(result.Results))
		for index, item := range result.Results {
			documents[index] = skillDocumentText(docByID[item.ID])
		}
		scores, err := r.Reranker.Rerank(ctx, query.Text, documents, len(documents))
		if err == nil {
			for _, score := range scores {
				if score.Index >= 0 && score.Index < len(result.Results) {
					result.Results[score.Index].NeuralScore = bounded(score.Score)
					result.Results[score.Index].FinalScore = bounded(.60*result.Results[score.Index].FinalScore + .40*result.Results[score.Index].NeuralScore)
				}
			}
			result.RerankerUsed = true
			sortSkillResults(result.Results)
		}
	}
	// Phase compatibility is an authorization precondition, not evidence of
	// relevance. Do not expose optional Skills that matched only because they
	// are allowed in the current phase; at least one retrieval channel must
	// support the selection.
	result.Results = filterRelevantSkillResults(result.Results)
	if len(result.Results) > limit {
		result.Results = result.Results[:limit]
	}
	result.RetrievedAt = nowUTC()
	result.SnapshotHash = brainruntime.Hash(struct {
		QueryHash string
		Results   []domain.SkillSearchResult
	}{result.QueryHash, result.Results})
	return result, nil
}

func filterRelevantSkillResults(items []domain.SkillSearchResult) []domain.SkillSearchResult {
	out := make([]domain.SkillSearchResult, 0, len(items))
	for _, item := range items {
		if item.BM25Score <= 0 && item.VectorScore <= 0 && item.NeuralScore <= 0 {
			continue
		}
		out = append(out, item)
	}
	return out
}

func (r *SkillHybridRetriever) embeddingVectors(ctx context.Context, query domain.SkillRetrievalQuery, documents []domain.SkillSearchDocument) ([]float32, [][]float32, bool, error) {
	if r == nil || r.Embedder == nil {
		return nil, nil, false, nil
	}
	missingTexts, missingKeys := []string{}, []string{}
	r.mu.RLock()
	for _, document := range documents {
		if len(r.vectors[document.ContentHash]) == 0 {
			missingKeys = append(missingKeys, document.ContentHash)
			missingTexts = append(missingTexts, skillDocumentText(document))
		}
	}
	r.mu.RUnlock()
	if len(missingTexts) > 0 {
		vectors, err := r.Embedder.Embed(ctx, missingTexts)
		if err != nil {
			return nil, nil, false, err
		}
		r.mu.Lock()
		for index, key := range missingKeys {
			if index < len(vectors) {
				r.vectors[key] = vectors[index]
			}
		}
		r.mu.Unlock()
	}
	queryVectors, err := r.Embedder.Embed(ctx, []string{query.Text})
	if err != nil || len(queryVectors) != 1 {
		return nil, nil, false, err
	}
	documentVectors := make([][]float32, len(documents))
	r.mu.RLock()
	for index, document := range documents {
		documentVectors[index] = append([]float32(nil), r.vectors[document.ContentHash]...)
	}
	r.mu.RUnlock()
	return queryVectors[0], documentVectors, true, nil
}

func skillDocumentText(document domain.SkillSearchDocument) string {
	return strings.TrimSpace(document.ID + " " + document.Description + " " + document.OutputContract + " " + document.Procedure)
}
func tokenizeSkill(text string) []string {
	fields := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' })
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if len(field) > 1 {
			out = append(out, field)
		}
	}
	return out
}
func skillBM25(query, document []string, documentFrequency map[string]int, documentCount int) float64 {
	if len(query) == 0 || len(document) == 0 || documentCount == 0 {
		return 0
	}
	counts := map[string]int{}
	for _, term := range document {
		counts[term]++
	}
	raw := 0.0
	for _, term := range query {
		tf := float64(counts[term])
		if tf == 0 {
			continue
		}
		df := float64(documentFrequency[term])
		idf := math.Log(1 + (float64(documentCount)-df+.5)/(df+.5))
		raw += idf * (tf * 2.2) / (tf + 1.2)
	}
	return bounded(1 - math.Exp(-raw))
}
func cosine32(left, right []float32) float64 {
	if len(left) == 0 || len(left) != len(right) {
		return 0
	}
	dot, a, b := 0.0, 0.0, 0.0
	for index := range left {
		x, y := float64(left[index]), float64(right[index])
		dot += x * y
		a += x * x
		b += y * y
	}
	if a == 0 || b == 0 {
		return 0
	}
	return bounded((dot/math.Sqrt(a*b) + 1) / 2)
}
func sortSkillResults(items []domain.SkillSearchResult) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].FinalScore == items[j].FinalScore {
			return items[i].ID < items[j].ID
		}
		return items[i].FinalScore > items[j].FinalScore
	})
}
func nowUTC() time.Time { return time.Now().UTC() }
