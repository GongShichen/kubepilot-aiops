package causal

import (
	"path/filepath"
	"testing"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func TestLoadPatternDirectoryUsesExternalKnowledge(t *testing.T) {
	matcher, err := Load(filepath.Join("..", "..", "knowledge", "patterns", "memory.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matcher.Patterns()) != 1 || matcher.Patterns()[0].Cause != "memory_leak" {
		t.Fatalf("unexpected loaded patterns: %+v", matcher.Patterns())
	}
}

func TestMatcherFindsMemoryLeakChain(t *testing.T) {
	m := DefaultMatcher()
	items := []domain.Evidence{
		{ID: "m", Source: "prometheus", Type: "memory_metric", Summary: "memory growth"},
		{ID: "k", Source: "kubernetes", Type: "kubernetes_event", Summary: "OOMKilled pod restart"},
		{ID: "l", Source: "loki", Type: "log", Summary: "error rate increase"},
	}
	matched := m.MatchEvidence(items)
	if len(matched) == 0 || matched[0].Cause != "memory_leak" || matched[0].Coverage < .75 {
		t.Fatalf("unexpected causal match: %+v", matched)
	}
}

func TestScoreHypothesisUsesCausalCoverage(t *testing.T) {
	low := ScoreHypothesis(ScoreInput{ModelPrior: .9, EvidenceSupport: .5, CausalCoverage: .1, TopologyMatch: .5, HistoricalSimilarity: .5})
	high := ScoreHypothesis(ScoreInput{ModelPrior: .1, EvidenceSupport: .5, CausalCoverage: .9, TopologyMatch: .5, HistoricalSimilarity: .5})
	if high.Score <= low.Score {
		t.Fatalf("causal coverage did not affect score: low=%+v high=%+v", low, high)
	}
}
