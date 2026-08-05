package retrieval

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/internal/retrieval/reranker"
	"github.com/kubepilot-aiops/kubepilot/reasoning"
)

type pipelineReranker struct{}

func (pipelineReranker) Enabled() bool               { return true }
func (pipelineReranker) Probe(context.Context) error { return nil }
func (pipelineReranker) ConfigHash() string          { return "test" }
func (pipelineReranker) Health() map[string]any      { return map[string]any{"configured": true} }
func (pipelineReranker) Rerank(context.Context, string, []string, int) ([]reranker.Result, error) {
	// Prefer the second reasoning candidate to prove the neural score changes
	// final order rather than being telemetry only.
	return []reranker.Result{{Index: 0, Score: .1}, {Index: 1, Score: .99}}, nil
}

func TestGenerateCandidatesDoesNotUseTopologyAsGenerator(t *testing.T) {
	lists := reasoning.CandidateLists{
		Semantic: []domain.RetrievalCandidate{{IncidentID: "semantic", SourceScores: map[string]float64{"semantic": .8}}},
		Lexical:  []domain.RetrievalCandidate{{IncidentID: "lexical", SourceScores: map[string]float64{"lexical": .9}}},
		Topology: []domain.RetrievalCandidate{{IncidentID: "topology-only", SourceScores: map[string]float64{"topology": 1}}},
	}
	got := GenerateCandidates(lists, DefaultPipelineConfig())
	if len(got) != 2 {
		t.Fatalf("candidate count=%d, topology-only candidate leaked into generation: %+v", len(got), got)
	}
	for _, candidate := range got {
		if candidate.IncidentID == "topology-only" {
			t.Fatal("topology was used as a candidate generator")
		}
	}
	if got[0].Rank.DeterministicScore <= 0 || got[0].Rank.DeterministicScore > 1 {
		t.Fatalf("candidate score is not bounded: %+v", got[0].Rank)
	}
}

func TestReasoningRerankKeepsCandidatesWhileAddingTopologyAndCausalFeatures(t *testing.T) {
	features := domain.IncidentFeatures{Namespace: "n", Service: "payment-service", TopologyServices: []string{"mysql"}, CausalNodeIDs: []string{"oom"}}
	candidates := []domain.RetrievalCandidate{
		{IncidentID: "a", Namespace: "n", Service: "other-service", Features: domain.IncidentFeatures{TopologyServices: []string{"mysql"}, CausalNodeIDs: []string{"oom"}}, Rank: domain.RankBreakdown{SemanticScore: .9, LexicalScore: .8, FinalScore: .86}},
		{IncidentID: "b", Namespace: "n", Service: "payment-service", Features: domain.IncidentFeatures{TopologyServices: []string{"frontend"}}, Rank: domain.RankBreakdown{SemanticScore: .7, LexicalScore: .7, FinalScore: .7}},
	}
	got := RerankReasoning(features, candidates, PipelineConfig{ReasoningTopK: 20})
	if len(got) != len(candidates) {
		t.Fatalf("reasoning rerank filtered candidates: %d", len(got))
	}
	if got[0].Rank.TopologyScore == 0 || got[0].Rank.CausalScore == 0 {
		t.Fatalf("reasoning features were not applied: %+v", got[0].Rank)
	}
}

func TestNeuralScoreChangesFinalRanking(t *testing.T) {
	features := domain.IncidentFeatures{Namespace: "n", Service: "payment-service", Terms: []string{"timeout"}}
	candidates := []domain.RetrievalCandidate{
		{IncidentID: "a", Summary: "a", Namespace: "n", Service: "payment-service", Rank: domain.RankBreakdown{ReasoningScore: .90, FinalScore: .90}},
		{IncidentID: "b", Summary: "b", Namespace: "n", Service: "payment-service", Rank: domain.RankBreakdown{ReasoningScore: .80, FinalScore: .80}},
	}
	got, err := RerankNeural(context.Background(), pipelineReranker{}, features, candidates, PipelineConfig{FinalTopK: 5})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].IncidentID != "b" || got[0].Rank.FinalScore <= got[1].Rank.FinalScore || !got[0].Rank.NeuralRanked {
		t.Fatalf("neural score did not change final ranking: %+v", got)
	}
}

func TestStructuredRerankerPayloadContainsIncidentFeatures(t *testing.T) {
	query := StructuredIncidentQuery(domain.IncidentFeatures{Namespace: "n", Service: "payment-service", Terms: []string{"500", "timeout"}})
	var decoded map[string]any
	if err := json.Unmarshal([]byte(query), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["service"] != "payment-service" || decoded["namespace"] != "n" {
		t.Fatalf("query payload=%s", query)
	}
	document := StructuredIncidentDocument(domain.RetrievalCandidate{IncidentID: "historical-1", Namespace: "n", Service: "payment-service", Summary: "OOMKilled", RootCause: "memory leak"})
	if !json.Valid([]byte(document)) {
		t.Fatalf("document is not JSON: %s", document)
	}
}
