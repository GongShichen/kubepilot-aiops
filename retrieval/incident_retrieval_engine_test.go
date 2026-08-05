package retrieval

import (
	"context"
	"testing"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

type engineKnowledge struct{}

func (engineKnowledge) SearchLexicalIncidents(context.Context, domain.IncidentFeatures, int) ([]domain.RetrievalCandidate, error) {
	return []domain.RetrievalCandidate{
		{IncidentID: "lexical", Namespace: "kubepilot-demo", Service: "order-service", Features: domain.IncidentFeatures{TopologyServices: []string{"mysql"}}, SourceScores: map[string]float64{"lexical": .9}},
		{IncidentID: "shared-db", Namespace: "kubepilot-demo", Service: "order-service", Features: domain.IncidentFeatures{TopologyServices: []string{"mysql"}}, SourceScores: map[string]float64{"lexical": .8}},
	}, nil
}
func (engineKnowledge) SearchTopologyIncidents(context.Context, domain.IncidentFeatures, int) ([]domain.RetrievalCandidate, error) {
	return []domain.RetrievalCandidate{{IncidentID: "shared-db", Namespace: "kubepilot-demo", Service: "order-service", Features: domain.IncidentFeatures{TopologyServices: []string{"mysql"}}, SourceScores: map[string]float64{"topology": .95}}}, nil
}

type engineEmbedder struct{}

func (engineEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	return [][]float32{{1, 0}}, nil
}

type engineVectors struct{}

func (engineVectors) Upsert(context.Context, []Document) error { return nil }
func (engineVectors) Search(context.Context, []float32, map[string]string, int) ([]Document, error) {
	return []Document{{ID: "semantic", Namespace: "kubepilot-demo", Service: "payment-service", Template: "database timeout", Score: .9}}, nil
}

func TestIncidentRetrievalEngineRunsThreeSourcesAndBoundsCanonicalResult(t *testing.T) {
	engine := IncidentRetrievalEngine{
		HistoricalRetriever: HistoricalRetriever{Embedder: engineEmbedder{}, Vectors: engineVectors{}, Knowledge: engineKnowledge{}},
	}
	features := domain.IncidentFeatures{Namespace: "kubepilot-demo", Service: "payment-service", Resource: "payment-deployment", Terms: []string{"database", "timeout"}, TopologyServices: []string{"mysql"}}
	lists, ranked, err := engine.Search(context.Background(), features)
	if err != nil {
		t.Fatal(err)
	}
	if len(lists.Semantic) != 1 || len(lists.Lexical) != 2 || len(lists.Topology) != 0 {
		t.Fatalf("canonical facade should generate from semantic and lexical only: %+v", lists)
	}
	if len(ranked) == 0 || len(ranked) > 5 {
		t.Fatalf("canonical engine did not return bounded reranked results: %d", len(ranked))
	}
	foundSharedDependency := false
	for _, candidate := range ranked {
		if candidate.IncidentID == "shared-db" {
			foundSharedDependency = true
		}
	}
	if !foundSharedDependency {
		t.Fatalf("cross-service shared dependency candidate was lost: %+v", ranked)
	}
}
