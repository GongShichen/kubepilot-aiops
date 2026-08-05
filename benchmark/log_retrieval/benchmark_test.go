package log_retrieval

import (
	"testing"
	"time"
)

func TestLogEvaluatorOnlyScoresTemplates(t *testing.T) {
	metrics := Evaluate([]Observation{{QueryID: "q1", RankedTemplateIDs: []string{"noise", "target"}, Latency: 10 * time.Millisecond}}, map[string]Expected{"q1": {TemplateID: "target"}})
	if metrics.RecallAt5 != 1 || metrics.MRR != .5 || metrics.NDCG <= 0 {
		t.Fatalf("unexpected log metrics: %+v", metrics)
	}
}

func TestLogObservationRequiresTemplateRanking(t *testing.T) {
	if err := ValidateObservation(Observation{QueryID: "q1"}); err == nil {
		t.Fatal("expected missing template ranking error")
	}
}
