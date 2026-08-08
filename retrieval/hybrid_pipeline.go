package retrieval

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/brainruntime"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"golang.org/x/sync/errgroup"
)

const hybridRRFK = 60

// Retrieve is the KubePilot Brain retrieval boundary. All five operational
// channels participate in candidate generation before deduplication and the
// configured external reranker. The older baseline pipeline is intentionally
// not called from this path.
func (h IncidentRetrievalEngine) Retrieve(ctx context.Context, query domain.HybridRetrievalQuery) (domain.HybridRetrievalResult, error) {
	limit := query.Limit
	if limit <= 0 || limit > 10 {
		limit = 5
	}
	features := query.Features
	features = enrichFeaturesFromWorldModel(features, query.WorldModel)
	if features.IncidentID == "" {
		features.IncidentID = query.IncidentID
	}
	if len(query.Terms) > 0 {
		features.Terms = mergeStrings(features.Terms, query.Terms)
	}
	channelLimit := maxInt(limit*10, 50)
	type response struct {
		candidates []domain.RetrievalCandidate
		err        error
		available  bool
	}
	responses := map[domain.RetrievalChannel]*response{
		domain.RetrievalBM25:     {available: h.Knowledge != nil},
		domain.RetrievalVector:   {available: h.Embedder != nil && h.Vectors != nil},
		domain.RetrievalGraph:    {available: h.Knowledge != nil},
		domain.RetrievalTemporal: {},
		domain.RetrievalMetric:   {},
	}
	temporalSearcher, temporalAvailable := h.Knowledge.(TemporalIncidentSearcher)
	metricSearcher, metricAvailable := h.Knowledge.(MetricIncidentSearcher)
	responses[domain.RetrievalTemporal].available = temporalAvailable && hasTemporalFeatures(features)
	responses[domain.RetrievalMetric].available = metricAvailable && len(features.Observed) > 0
	group, groupCtx := errgroup.WithContext(ctx)
	if responses[domain.RetrievalBM25].available {
		group.Go(func() error {
			responses[domain.RetrievalBM25].candidates, responses[domain.RetrievalBM25].err = h.Lexical(groupCtx, features, channelLimit)
			return nil
		})
	}
	if responses[domain.RetrievalVector].available {
		group.Go(func() error {
			responses[domain.RetrievalVector].candidates, responses[domain.RetrievalVector].err = h.Semantic(groupCtx, features, channelLimit)
			return nil
		})
	}
	if responses[domain.RetrievalGraph].available {
		group.Go(func() error {
			responses[domain.RetrievalGraph].candidates, responses[domain.RetrievalGraph].err = h.Topology(groupCtx, features, channelLimit)
			return nil
		})
	}
	if responses[domain.RetrievalTemporal].available {
		group.Go(func() error {
			responses[domain.RetrievalTemporal].candidates, responses[domain.RetrievalTemporal].err = temporalSearcher.SearchTemporalIncidents(groupCtx, features, channelLimit)
			return nil
		})
	}
	if responses[domain.RetrievalMetric].available {
		group.Go(func() error {
			responses[domain.RetrievalMetric].candidates, responses[domain.RetrievalMetric].err = metricSearcher.SearchMetricIncidents(groupCtx, features, channelLimit)
			return nil
		})
	}
	_ = group.Wait()

	ordered := []domain.RetrievalChannel{domain.RetrievalBM25, domain.RetrievalVector, domain.RetrievalGraph, domain.RetrievalTemporal, domain.RetrievalMetric}
	result := domain.HybridRetrievalResult{RetrievedAt: time.Now().UTC()}
	lists := map[domain.RetrievalChannel][]domain.RetrievalCandidate{}
	for _, channel := range ordered {
		entry := responses[channel]
		channelResult := domain.HybridRetrievalChannelResult{Channel: channel, Available: entry.available, Candidates: excludeIncident(entry.candidates, query.IncidentID)}
		if entry.err != nil {
			channelResult.Error = entry.err.Error()
		}
		result.Channels = append(result.Channels, channelResult)
		if entry.available && entry.err == nil {
			lists[channel] = channelResult.Candidates
		}
	}
	result.Fused = fuseHybridChannels(lists, maxInt(limit*4, 20))
	result.Final = append([]domain.RetrievalCandidate(nil), result.Fused...)
	if h.Reranker != nil && h.Reranker.Enabled() && len(result.Final) > 0 {
		var err error
		result.Final, err = RerankNeural(ctx, h.Reranker, features, result.Final, PipelineConfig{CandidateTopK: len(result.Fused), ReasoningTopK: len(result.Fused), FinalTopK: limit})
		if err != nil {
			return domain.HybridRetrievalResult{}, err
		}
		result.RerankerUsed = true
	} else if len(result.Final) > limit {
		result.Final = result.Final[:limit]
	}
	result.SnapshotHash = brainruntime.Hash(struct {
		IncidentID         string
		Terms              []string
		WorldModelSnapshot string
		Channels           []domain.HybridRetrievalChannelResult
		Final              []domain.RetrievalCandidate
	}{query.IncidentID, features.Terms, worldModelSnapshot(query.WorldModel), result.Channels, result.Final})
	return result, nil
}

func enrichFeaturesFromWorldModel(features domain.IncidentFeatures, model *domain.OperationalWorldModel) domain.IncidentFeatures {
	if model == nil {
		return features
	}
	if features.Cluster == "" {
		features.Cluster = model.Cluster
	}
	if features.Namespace == "" {
		features.Namespace = model.Namespace
	}
	if features.Observed == nil {
		features.Observed = map[string]float64{}
	}
	for _, entity := range model.Entities {
		features.TopologyServices = mergeStrings(features.TopologyServices, []string{entity.Service, entity.Resource})
		features.Terms = mergeStrings(features.Terms, strings.Fields(strings.ToLower(entity.Kind+" "+entity.State)))
	}
	for _, signal := range model.AbnormalSignals {
		features.Terms = mergeStrings(features.Terms, strings.Fields(strings.ToLower(signal.Category+" "+signal.Signal+" "+signal.Direction)))
	}
	for _, metric := range model.MetricSignatures {
		value := metric.Value
		if value == 0 {
			value = metric.Strength
		}
		features.Observed[metric.Name] = value
	}
	for _, event := range model.Timeline {
		if event.OccurredAt.IsZero() {
			continue
		}
		if features.WindowStart.IsZero() || event.OccurredAt.Before(features.WindowStart) {
			features.WindowStart = event.OccurredAt
		}
		if features.WindowEnd.IsZero() || event.OccurredAt.After(features.WindowEnd) {
			features.WindowEnd = event.OccurredAt
		}
	}
	if len(features.TopologyGraph.Nodes) == 0 && len(model.Entities) > 0 {
		features.TopologyGraph.RootService = features.Service
		for _, entity := range model.Entities {
			features.TopologyGraph.Nodes = append(features.TopologyGraph.Nodes, domain.DependencyNode{ID: entity.ID, Kind: entity.Kind, Service: entity.Service, Resource: entity.Resource, Metadata: entity.Attributes})
		}
		for _, relation := range model.Relations {
			features.TopologyGraph.Edges = append(features.TopologyGraph.Edges, domain.DependencyEdge{From: relation.From, To: relation.To, Kind: relation.Kind})
		}
	}
	return features
}

func worldModelSnapshot(model *domain.OperationalWorldModel) string {
	if model == nil {
		return ""
	}
	return model.EvidenceSnapshotHash
}

func fuseHybridChannels(lists map[domain.RetrievalChannel][]domain.RetrievalCandidate, limit int) []domain.RetrievalCandidate {
	weights := map[domain.RetrievalChannel]float64{domain.RetrievalBM25: 1, domain.RetrievalVector: 1, domain.RetrievalGraph: .9, domain.RetrievalTemporal: .7, domain.RetrievalMetric: .9}
	merged := map[string]domain.RetrievalCandidate{}
	availableWeight := 0.0
	for channel, list := range lists {
		if len(list) == 0 {
			continue
		}
		availableWeight += weights[channel]
		for rank, incoming := range list {
			if incoming.IncidentID == "" {
				continue
			}
			candidate, exists := merged[incoming.IncidentID]
			if !exists {
				candidate = incoming
				candidate.SourceRanks = map[string]int{}
				candidate.SourceScores = map[string]float64{}
			} else {
				candidate = mergePipelineCandidate(candidate, incoming)
			}
			name := strings.ToLower(string(channel))
			candidate.SourceRanks[name] = rank + 1
			score := hybridSourceScore(incoming, channel, rank)
			candidate.SourceScores[name] = score
			candidate.RRFScore += weights[channel] / float64(hybridRRFK+rank+1)
			switch channel {
			case domain.RetrievalBM25:
				candidate.Rank.LexicalScore = score
			case domain.RetrievalVector:
				candidate.Rank.SemanticScore = score
			case domain.RetrievalGraph:
				candidate.Rank.TopologyScore = score
			case domain.RetrievalTemporal:
				candidate.Rank.TemporalScore = score
			case domain.RetrievalMetric:
				candidate.Rank.MetricScore = score
			}
			merged[candidate.IncidentID] = candidate
		}
	}
	denominator := availableWeight / float64(hybridRRFK+1)
	out := make([]domain.RetrievalCandidate, 0, len(merged))
	for _, candidate := range merged {
		if denominator > 0 {
			candidate.Rank.NormalizedRRF = bounded(candidate.RRFScore / denominator)
		}
		candidate.Rank.ReasoningScore = bounded(.40*candidate.Rank.NormalizedRRF + .15*candidate.Rank.LexicalScore + .15*candidate.Rank.SemanticScore + .12*candidate.Rank.TopologyScore + .08*candidate.Rank.TemporalScore + .10*candidate.Rank.MetricScore)
		candidate.Rank.DeterministicScore = candidate.Rank.ReasoningScore
		candidate.Rank.FinalScore = candidate.Rank.ReasoningScore
		candidate.RankingReasons = []string{
			"hybrid_rrf=" + formatScore(candidate.Rank.NormalizedRRF), "bm25=" + formatScore(candidate.Rank.LexicalScore),
			"vector=" + formatScore(candidate.Rank.SemanticScore), "graph=" + formatScore(candidate.Rank.TopologyScore),
			"temporal=" + formatScore(candidate.Rank.TemporalScore), "metric=" + formatScore(candidate.Rank.MetricScore),
		}
		out = append(out, candidate)
	}
	sortPipelineCandidates(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func hybridSourceScore(candidate domain.RetrievalCandidate, channel domain.RetrievalChannel, rank int) float64 {
	keys := map[domain.RetrievalChannel][]string{
		domain.RetrievalBM25: {"bm25", "lexical"}, domain.RetrievalVector: {"vector", "semantic"},
		domain.RetrievalGraph: {"graph", "topology"}, domain.RetrievalTemporal: {"temporal"}, domain.RetrievalMetric: {"metric"},
	}[channel]
	for _, key := range keys {
		if value := candidate.SourceScores[key]; value > 0 {
			return bounded(value)
		}
	}
	return 1 / float64(rank+1)
}

func hasTemporalFeatures(features domain.IncidentFeatures) bool {
	return !features.WindowStart.IsZero() && !features.WindowEnd.IsZero() && features.WindowEnd.After(features.WindowStart)
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func formatScore(value float64) string {
	return strings.TrimRight(strings.TrimRight(fmtFloat(value), "0"), ".")
}
func fmtFloat(value float64) string { return strconv.FormatFloat(value, 'f', 4, 64) }
