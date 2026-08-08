package store

import (
	"testing"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func TestTemporalAndMetricKnowledgeSimilarityAreIndependentSignals(t *testing.T) {
	now := time.Now().UTC()
	current := domain.IncidentFeatures{
		WindowStart: now.Add(-5 * time.Minute),
		WindowEnd:   now,
		Revision:    "release-a",
		Observed:    map[string]float64{"latency": .9, "errors": .4},
	}
	historical := domain.IncidentFeatures{
		WindowStart: now.Add(-10 * time.Minute),
		WindowEnd:   now,
		Revision:    "release-a",
		Observed:    map[string]float64{"latency": .8, "errors": .35},
	}
	if score := temporalKnowledgeSimilarity(current, historical); score <= .5 || score > 1 {
		t.Fatalf("unexpected temporal similarity: %f", score)
	}
	if score := metricKnowledgeSimilarity(current.Observed, historical.Observed); score <= .9 || score > 1 {
		t.Fatalf("unexpected metric similarity: %f", score)
	}
}

func TestRankKnowledgeCandidatesAdmitsChannelOnlyCandidate(t *testing.T) {
	items := []domain.RetrievalCandidate{
		{IncidentID: "metric-only", SourceScores: map[string]float64{}},
		{IncidentID: "no-match", SourceScores: map[string]float64{}},
	}
	ranked := rankKnowledgeCandidates(items, "metric", 10, func(candidate domain.RetrievalCandidate) float64 {
		if candidate.IncidentID == "metric-only" {
			return .91
		}
		return 0
	})
	if len(ranked) != 1 || ranked[0].IncidentID != "metric-only" || ranked[0].SourceScores["metric"] != .91 {
		t.Fatalf("independent channel candidate was not admitted: %+v", ranked)
	}
}
