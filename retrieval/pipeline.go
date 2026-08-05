package retrieval

// This file contains the single historical-incident ranking pipeline.  The
// important boundary is intentional: semantic and lexical retrieval generate
// a high-recall candidate set; topology and causal knowledge only score that
// set.  Neither sparse signal can therefore remove a candidate before the
// reasoning stage.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/internal/retrieval/reranker"
	"github.com/kubepilot-aiops/kubepilot/reasoning"
	topologyretrieval "github.com/kubepilot-aiops/kubepilot/retrieval/topology"
)

// PipelineConfig bounds each stage independently.  Keeping the limits in one
// value makes the stage contract testable and prevents a feature reranker from
// accidentally becoming another unbounded candidate generator.
type PipelineConfig struct {
	CandidateTopK int
	ReasoningTopK int
	FinalTopK     int
}

func DefaultPipelineConfig() PipelineConfig {
	return PipelineConfig{CandidateTopK: 100, ReasoningTopK: 20, FinalTopK: 5}
}

func normalizePipelineConfig(config PipelineConfig) PipelineConfig {
	d := DefaultPipelineConfig()
	if config.CandidateTopK > 0 {
		d.CandidateTopK = config.CandidateTopK
	}
	if config.ReasoningTopK > 0 {
		d.ReasoningTopK = config.ReasoningTopK
	}
	if config.FinalTopK > 0 {
		d.FinalTopK = config.FinalTopK
	}
	return d
}

// GenerateCandidates is the high-recall stage.  Topology lists are accepted
// in CandidateLists for compatibility with ReAct exploration, but deliberately
// ignored here.  A topology-only result must never be able to evict a
// semantic/lexical candidate before reasoning reranking.
func GenerateCandidates(lists reasoning.CandidateLists, config PipelineConfig) []domain.RetrievalCandidate {
	config = normalizePipelineConfig(config)
	merged := make(map[string]domain.RetrievalCandidate)
	add := func(source string, list []domain.RetrievalCandidate) {
		for index, incoming := range list {
			if incoming.IncidentID == "" {
				continue
			}
			candidate, exists := merged[incoming.IncidentID]
			if !exists {
				candidate = incoming
				candidate.SourceRanks = map[string]int{}
				candidate.SourceScores = map[string]float64{}
			}
			candidate.SourceRanks[source] = index + 1
			score := sourceScore(incoming, source, index)
			candidate.SourceScores[source] = score
			if source == "semantic" {
				candidate.Rank.SemanticScore = score
			}
			if source == "lexical" {
				candidate.Rank.LexicalScore = score
			}
			candidate = mergePipelineCandidate(candidate, incoming)
			candidate.Rank.SemanticScore = maxFloat(candidate.Rank.SemanticScore, sourceScore(candidate, "semantic", rankOf(candidate, "semantic")))
			candidate.Rank.LexicalScore = maxFloat(candidate.Rank.LexicalScore, sourceScore(candidate, "lexical", rankOf(candidate, "lexical")))
			candidate.Rank.DeterministicScore = .6*candidate.Rank.SemanticScore + .4*candidate.Rank.LexicalScore
			candidate.Rank.FinalScore = candidate.Rank.DeterministicScore
			candidate.Rank.ReasoningScore = 0
			candidate.RankingReasons = []string{fmt.Sprintf("candidate_generation=0.6*semantic(%.4f)+0.4*lexical(%.4f)", candidate.Rank.SemanticScore, candidate.Rank.LexicalScore)}
			merged[candidate.IncidentID] = candidate
		}
	}
	add("semantic", lists.Semantic)
	add("lexical", lists.Lexical)
	// Explicitly do not call add("topology", lists.Topology).
	out := make([]domain.RetrievalCandidate, 0, len(merged))
	for _, candidate := range merged {
		out = append(out, candidate)
	}
	sortPipelineCandidates(out)
	if len(out) > config.CandidateTopK {
		out = out[:config.CandidateTopK]
	}
	return out
}

// RerankReasoning adds topology and causal features to generated candidates.
// Both scores are soft features; candidates are never filtered because a
// graph or causal pattern is sparse or unavailable.
func RerankReasoning(features domain.IncidentFeatures, candidates []domain.RetrievalCandidate, config PipelineConfig) []domain.RetrievalCandidate {
	return rerankReasoning(features, candidates, config, true)
}

// RerankTopology exposes the topology-only ablation.  It has the same
// candidate set and metadata as the full reasoning stage, but leaves the
// causal feature at zero so the benchmark can measure incremental value
// without changing the generator.
func RerankTopology(features domain.IncidentFeatures, candidates []domain.RetrievalCandidate, config PipelineConfig) []domain.RetrievalCandidate {
	return rerankReasoning(features, candidates, config, false)
}

func rerankReasoning(features domain.IncidentFeatures, candidates []domain.RetrievalCandidate, config PipelineConfig, includeCausal bool) []domain.RetrievalCandidate {
	config = normalizePipelineConfig(config)
	out := append([]domain.RetrievalCandidate(nil), candidates...)
	for index := range out {
		candidate := &out[index]
		candidate.Rank.TopologyScore = topologyScore(features, candidate.Features)
		if includeCausal {
			candidate.Rank.CausalScore = causalScore(features, candidate.Features)
		} else {
			candidate.Rank.CausalScore = 0
		}
		candidate.Rank.MetadataScore = metadataScore(features, *candidate)
		// Keep legacy consumers (hypothesis verification and audit readers)
		// aligned with the explicit stage fields. These aliases do not change
		// ranking; they preserve the observed feature semantics.
		candidate.Rank.TopologySimilarity = candidate.Rank.TopologyScore
		candidate.Rank.CausalPathCoverage = candidate.Rank.CausalScore
		candidate.Rank.ServiceResourceProximity = candidate.Rank.MetadataScore
		candidate.Rank.SemanticScore = bounded(candidate.Rank.SemanticScore)
		candidate.Rank.LexicalScore = bounded(candidate.Rank.LexicalScore)
		candidate.Rank.ReasoningScore = .35*candidate.Rank.SemanticScore +
			.20*candidate.Rank.LexicalScore + .20*candidate.Rank.TopologyScore +
			.15*candidate.Rank.CausalScore + .10*candidate.Rank.MetadataScore
		candidate.Rank.DeterministicScore = candidate.Rank.ReasoningScore
		candidate.Rank.FinalScore = candidate.Rank.ReasoningScore
		candidate.Rank.IncidentRank = &domain.IncidentRankBreakdown{
			IncidentID: candidate.IncidentID, SemanticScore: candidate.Rank.SemanticScore,
			LexicalScore: candidate.Rank.LexicalScore, TopologyScore: candidate.Rank.TopologyScore,
			CausalScore: candidate.Rank.CausalScore, MetadataScore: candidate.Rank.MetadataScore,
			ReasoningScore: candidate.Rank.ReasoningScore, DeterministicScore: candidate.Rank.DeterministicScore,
			DeterministicWeight: 1, Factors: map[string]float64{
				"semantic": candidate.Rank.SemanticScore, "lexical": candidate.Rank.LexicalScore,
				"topology": candidate.Rank.TopologyScore, "causal": candidate.Rank.CausalScore,
				"metadata": candidate.Rank.MetadataScore,
			},
		}
		candidate.RankingReasons = []string{
			fmt.Sprintf("semantic=%.4f", candidate.Rank.SemanticScore),
			fmt.Sprintf("lexical=%.4f", candidate.Rank.LexicalScore),
			fmt.Sprintf("topology_feature=%.4f", candidate.Rank.TopologyScore),
			fmt.Sprintf("causal_feature=%.4f", candidate.Rank.CausalScore),
			fmt.Sprintf("metadata=%.4f", candidate.Rank.MetadataScore),
			fmt.Sprintf("reasoning_score=%.4f", candidate.Rank.ReasoningScore),
		}
	}
	sortPipelineCandidates(out)
	if len(out) > config.ReasoningTopK {
		out = out[:config.ReasoningTopK]
	}
	return out
}

// RerankNeural performs the final external rerank over only the reasoning
// shortlist.  The neural score is fused into FinalScore, never merely logged.
// If the service is unavailable, the deterministic reasoning order is returned
// with NeuralRanked=false; no synthetic neural score is fabricated.
func RerankNeural(ctx context.Context, service reranker.Service, features domain.IncidentFeatures, candidates []domain.RetrievalCandidate, config PipelineConfig) ([]domain.RetrievalCandidate, error) {
	config = normalizePipelineConfig(config)
	out := append([]domain.RetrievalCandidate(nil), candidates...)
	if len(out) == 0 {
		return out, nil
	}
	if service == nil || !service.Enabled() {
		if len(out) > config.FinalTopK {
			out = out[:config.FinalTopK]
		}
		return out, nil
	}
	documents := make([]string, len(out))
	for index, candidate := range out {
		documents[index] = StructuredIncidentDocument(candidate)
	}
	scores, err := service.Rerank(ctx, StructuredIncidentQuery(features), documents, len(documents))
	if err != nil {
		return nil, err
	}
	for _, score := range scores {
		if score.Index < 0 || score.Index >= len(out) {
			return nil, fmt.Errorf("reranker returned invalid candidate index %d", score.Index)
		}
		out[score.Index].Rank.NeuralSimilarity = bounded(score.Score)
		out[score.Index].Rank.NeuralRanked = true
		out[score.Index].Rank.FinalScore = .60*out[score.Index].Rank.ReasoningScore + .40*out[score.Index].Rank.NeuralSimilarity
		if out[score.Index].Rank.IncidentRank == nil {
			out[score.Index].Rank.IncidentRank = &domain.IncidentRankBreakdown{IncidentID: out[score.Index].IncidentID}
		}
		out[score.Index].Rank.IncidentRank.NeuralScore = out[score.Index].Rank.NeuralSimilarity
		out[score.Index].Rank.IncidentRank.NeuralUsed = true
		out[score.Index].Rank.IncidentRank.NeuralWeight = .40
		out[score.Index].Rank.IncidentRank.DeterministicWeight = .60
		out[score.Index].Rank.IncidentRank.DeterministicScore = out[score.Index].Rank.ReasoningScore
		out[score.Index].Rank.IncidentRank.FinalScore = out[score.Index].Rank.FinalScore
		out[score.Index].RankingReasons = append(out[score.Index].RankingReasons,
			fmt.Sprintf("neural=%.4f", out[score.Index].Rank.NeuralSimilarity),
			fmt.Sprintf("final=0.6*reasoning+0.4*neural=%.4f", out[score.Index].Rank.FinalScore))
	}
	sortPipelineCandidates(out)
	if len(out) > config.FinalTopK {
		out = out[:config.FinalTopK]
	}
	return out, nil
}

func StructuredIncidentQuery(features domain.IncidentFeatures) string {
	payload := map[string]any{
		"service": features.Service, "namespace": features.Namespace,
		"symptoms": features.Terms, "evidence": features.EvidenceTypes,
		"topology": features.TopologyGraph,
	}
	return marshalStructured(payload)
}

func StructuredIncidentDocument(candidate domain.RetrievalCandidate) string {
	payload := map[string]any{
		"incident_id": candidate.IncidentID, "service": candidate.Service,
		"namespace": candidate.Namespace, "symptoms": []string{candidate.Summary},
		"metrics": candidate.Features.Observed, "logs": []string{candidate.Summary},
		"topology": candidate.Features.TopologyGraph, "rootcause": candidate.RootCause,
	}
	return marshalStructured(payload)
}

func marshalStructured(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func topologyScore(current, candidate domain.IncidentFeatures) float64 {
	if hasGraph(current.TopologyGraph) && hasGraph(candidate.TopologyGraph) {
		return bounded(topologyretrieval.GraphCandidateScore(current.TopologyGraph, candidate.TopologyGraph))
	}
	return bounded(jaccard(current.TopologyServices, candidate.TopologyServices))
}

func causalScore(current, candidate domain.IncidentFeatures) float64 {
	return bounded(jaccard(current.CausalNodeIDs, candidate.CausalNodeIDs))
}

func metadataScore(features domain.IncidentFeatures, candidate domain.RetrievalCandidate) float64 {
	score := 0.0
	if features.Namespace != "" && candidate.Namespace == features.Namespace {
		score += .4
	}
	if features.Service != "" && candidate.Service == features.Service {
		score += .4
	}
	if features.Resource != "" && candidate.Resource == features.Resource {
		score += .2
	}
	return score
}

func sourceScore(candidate domain.RetrievalCandidate, source string, rank int) float64 {
	if candidate.SourceScores != nil {
		if value, ok := candidate.SourceScores[source]; ok && value > 0 {
			return bounded(value)
		}
	}
	if rank <= 0 {
		return 0
	}
	return 1 / float64(rank)
}

func rankOf(candidate domain.RetrievalCandidate, source string) int {
	if candidate.SourceRanks == nil {
		return 0
	}
	return candidate.SourceRanks[source]
}

func mergePipelineCandidate(current, incoming domain.RetrievalCandidate) domain.RetrievalCandidate {
	if current.Namespace == "" {
		current.Namespace = incoming.Namespace
	}
	if current.Service == "" {
		current.Service = incoming.Service
	}
	if current.Resource == "" {
		current.Resource = incoming.Resource
	}
	if current.Category == "" {
		current.Category = incoming.Category
	}
	if current.RootCause == "" {
		current.RootCause = incoming.RootCause
	}
	if current.Summary == "" {
		current.Summary = incoming.Summary
	}
	if current.Features.TopologyGraph.RootService == "" {
		current.Features.TopologyGraph = incoming.Features.TopologyGraph
	}
	current.Features.Terms = mergeStrings(current.Features.Terms, incoming.Features.Terms)
	current.Features.TopologyServices = mergeStrings(current.Features.TopologyServices, incoming.Features.TopologyServices)
	current.Features.CausalNodeIDs = mergeStrings(current.Features.CausalNodeIDs, incoming.Features.CausalNodeIDs)
	return current
}

func sortPipelineCandidates(items []domain.RetrievalCandidate) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Rank.FinalScore == items[j].Rank.FinalScore {
			return items[i].IncidentID < items[j].IncidentID
		}
		return items[i].Rank.FinalScore > items[j].Rank.FinalScore
	})
}

func hasGraph(graph domain.IncidentDependencyGraph) bool {
	return len(graph.Nodes) > 0 && len(graph.Edges) > 0
}

func jaccard(left, right []string) float64 {
	a, b := map[string]bool{}, map[string]bool{}
	for _, item := range left {
		if item != "" {
			a[item] = true
		}
	}
	for _, item := range right {
		if item != "" {
			b[item] = true
		}
	}
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	intersection, union := 0, len(a)
	for item := range b {
		if a[item] {
			intersection++
		} else {
			union++
		}
	}
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

func mergeStrings(left, right []string) []string {
	seen := map[string]bool{}
	for _, item := range append(append([]string(nil), left...), right...) {
		if item != "" {
			seen[item] = true
		}
	}
	out := make([]string, 0, len(seen))
	for item := range seen {
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}

func bounded(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func maxFloat(left, right float64) float64 {
	if left > right {
		return left
	}
	return right
}
