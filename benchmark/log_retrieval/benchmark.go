// Package log_retrieval evaluates log/template intelligence only. It accepts
// ranked template IDs and never contains topology, causal, incident or
// root-cause features. Those capabilities belong to the incident and
// diagnosis benchmark suites.
package log_retrieval

import (
	"fmt"
	"sort"
	"time"

	"github.com/kubepilot-aiops/kubepilot/benchmark/evaluator"
)

type Query struct {
	ID   string `json:"id"`
	Text string `json:"text,omitempty"`
}

// Expected is evaluator-only and must not be serialized into an Agent or
// retrieval-service request.
type Expected struct {
	TemplateID string `json:"template_id"`
}

type Observation struct {
	QueryID           string        `json:"query_id"`
	RankedTemplateIDs []string      `json:"ranked_template_ids"`
	Latency           time.Duration `json:"latency"`
}

type Metrics struct {
	Queries      int     `json:"queries"`
	RecallAt1    float64 `json:"recall_at_1"`
	RecallAt5    float64 `json:"recall_at_5"`
	RecallAt10   float64 `json:"recall_at_10"`
	MRR          float64 `json:"mrr"`
	NDCG         float64 `json:"ndcg"`
	P50LatencyMS float64 `json:"p50_latency_ms"`
	P95LatencyMS float64 `json:"p95_latency_ms"`
	P99LatencyMS float64 `json:"p99_latency_ms"`
}

type Report struct {
	Dataset    string    `json:"dataset"`
	Strategies []Metrics `json:"strategies"`
}

// Evaluate scores template retrieval. The ranked list is the only signal
// accepted by this evaluator, preventing accidental topology/causal scoring.
func Evaluate(observations []Observation, expected map[string]Expected) Metrics {
	metrics := Metrics{Queries: len(observations)}
	if len(observations) == 0 {
		return metrics
	}
	rankings := make([][]string, 0, len(observations))
	relevant := make([]map[string]float64, 0, len(observations))
	latencies := make([]float64, 0, len(observations))
	for _, observation := range observations {
		rankings = append(rankings, observation.RankedTemplateIDs)
		truth := expected[observation.QueryID]
		relevant = append(relevant, map[string]float64{truth.TemplateID: 1})
		latencies = append(latencies, float64(observation.Latency.Microseconds())/1000)
	}
	ranking := evaluator.EvaluateRanking(rankings, relevant)
	metrics.RecallAt1 = ranking.RecallAt1
	metrics.RecallAt5 = ranking.RecallAt5
	metrics.RecallAt10 = ranking.RecallAt10
	metrics.MRR = ranking.MRR
	metrics.NDCG = ranking.NDCG
	sort.Float64s(latencies)
	metrics.P50LatencyMS = percentile(latencies, .50)
	metrics.P95LatencyMS = percentile(latencies, .95)
	metrics.P99LatencyMS = percentile(latencies, .99)
	return metrics
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 1 {
		return sorted[len(sorted)-1]
	}
	index := int(float64(len(sorted)-1) * p)
	return sorted[index]
}

// ValidateObservation rejects fields that would indicate that a log query
// has been contaminated with incident reasoning signals.
func ValidateObservation(observation Observation) error {
	if observation.QueryID == "" {
		return fmt.Errorf("query_id is required")
	}
	if observation.RankedTemplateIDs == nil {
		return fmt.Errorf("ranked_template_ids are required")
	}
	return nil
}
