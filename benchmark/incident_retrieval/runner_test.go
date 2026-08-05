package incident_retrieval

import (
	"context"
	"testing"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/retrieval"
)

type runnerEmbedder struct{}

func (runnerEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	return [][]float32{{1, 0}}, nil
}

type runnerVectors struct{}

func (runnerVectors) Upsert(context.Context, []retrieval.Document) error { return nil }
func (runnerVectors) Search(context.Context, []float32, map[string]string, int) ([]retrieval.Document, error) {
	return []retrieval.Document{{ID: "related", Namespace: "demo", Service: "payment", Template: "memory growth", RootCause: "memory leak", Score: .9}}, nil
}

type runnerKnowledge struct{}

func (runnerKnowledge) SearchLexicalIncidents(context.Context, domain.IncidentFeatures, int) ([]domain.RetrievalCandidate, error) {
	return []domain.RetrievalCandidate{{IncidentID: "related", Namespace: "demo", Service: "payment", SourceScores: map[string]float64{"lexical": .9}}}, nil
}
func (runnerKnowledge) SearchTopologyIncidents(context.Context, domain.IncidentFeatures, int) ([]domain.RetrievalCandidate, error) {
	return nil, nil
}

func TestRunUsesProductionIncidentRetrievalEngine(t *testing.T) {
	dataset := Dataset{Version: "test", Incidents: []Incident{{
		IncidentID: "query", Category: "memory", Service: "payment", Namespace: "demo",
		Symptoms: []string{"memory growth"}, RootCause: "memory leak", RelatedIncidents: []string{"related"},
	}}}
	engine := &retrieval.IncidentRetrievalEngine{
		HistoricalRetriever: retrieval.HistoricalRetriever{Embedder: runnerEmbedder{}, Vectors: runnerVectors{}, Knowledge: runnerKnowledge{}},
	}
	report, err := Run(context.Background(), RunnerConfig{Dataset: dataset, Engine: engine})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Strategies) != len(AblationStrategies) {
		t.Fatalf("strategies=%d, want %d", len(report.Strategies), len(AblationStrategies))
	}
	for _, metrics := range report.Strategies {
		if metrics.RecallAt1 != 1 {
			t.Fatalf("strategy %s did not receive production-ranked candidate: %+v", metrics.Strategy, metrics)
		}
	}
}
