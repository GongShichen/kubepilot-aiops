package incident_retrieval

import (
	"context"
	"testing"

	rerankerclient "github.com/kubepilot-aiops/kubepilot/internal/retrieval/reranker"
)

type boundedReranker struct {
	maxDocuments int
}

func (r *boundedReranker) Enabled() bool               { return true }
func (r *boundedReranker) Probe(context.Context) error { return nil }
func (r *boundedReranker) ConfigHash() string          { return "test" }
func (r *boundedReranker) Health() map[string]any      { return map[string]any{"configured": true} }
func (r *boundedReranker) Rerank(_ context.Context, _ string, documents []string, _ int) ([]rerankerclient.Result, error) {
	if len(documents) > r.maxDocuments {
		r.maxDocuments = len(documents)
	}
	results := make([]rerankerclient.Result, len(documents))
	for i := range documents {
		results[i] = rerankerclient.Result{Index: i, Score: float64(len(documents)-i) / float64(len(documents))}
	}
	return results, nil
}

func TestFullIncidentRerankUsesBoundedShortlist(t *testing.T) {
	query := Incident{IncidentID: "query", Category: "memory", Service: "payment", Namespace: "demo", Symptoms: []string{"error"}, RootCause: "memory_leak"}
	candidates := make([]Incident, 0, 500)
	for i := 0; i < 500; i++ {
		candidates = append(candidates, Incident{IncidentID: "candidate-" + string(rune('a'+i%26)) + string(rune('a'+i/26)), Category: "memory", Service: "payment", Namespace: "demo", Symptoms: []string{"error"}, RootCause: "memory_leak"})
	}
	reranker := &boundedReranker{maxDocuments: 0}
	ranked, err := rankIncident(context.Background(), query, candidates, StrategyFull, reranker)
	if err != nil {
		t.Fatal(err)
	}
	if reranker.maxDocuments > incidentReasoningTopK {
		t.Fatalf("neural reranker received %d documents, want <= %d", reranker.maxDocuments, incidentReasoningTopK)
	}
	if len(ranked) != incidentCandidateTopK {
		t.Fatalf("ranked candidates = %d, want %d", len(ranked), incidentCandidateTopK)
	}
}
