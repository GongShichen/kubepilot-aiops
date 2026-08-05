package incident_retrieval

import (
	"sort"
	"time"

	"github.com/kubepilot-aiops/kubepilot/benchmark/evaluator"
)

const (
	StrategySemantic                = "semantic"
	StrategySemanticLexical         = "semantic_lexical"
	StrategySemanticLexicalTopology = "semantic_lexical_topology"
	StrategySemanticLexicalCausal   = "semantic_lexical_topology_causal"
	StrategyFull                    = "full"
)

var AblationStrategies = []string{
	StrategySemantic,
	StrategySemanticLexical,
	StrategySemanticLexicalTopology,
	StrategySemanticLexicalCausal,
	StrategyFull,
}

type Observation struct {
	QueryID   string        `json:"query_id"`
	Strategy  string        `json:"strategy"`
	RankedIDs []string      `json:"ranked_ids"`
	Latency   time.Duration `json:"latency"`
}

type Metrics struct {
	Strategy     string  `json:"strategy"`
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
	Dataset        string         `json:"dataset"`
	QueriesCount   int            `json:"queries"`
	CategoryCounts map[string]int `json:"category_counts,omitempty"`
	Strategies     []Metrics      `json:"strategies"`
}

func (r Report) Queries() int { return r.QueriesCount }

// Evaluate scores historical incident IDs. Topology and causal fields are
// accepted only through the ranked output; they are not used to score log
// templates and are never mixed with the log_retrieval package.
func Evaluate(dataset Dataset, observations []Observation) Report {
	byStrategy := map[string][]Observation{}
	for _, observation := range observations {
		byStrategy[observation.Strategy] = append(byStrategy[observation.Strategy], observation)
	}
	strategies := make([]string, 0, len(byStrategy))
	for strategy := range byStrategy {
		strategies = append(strategies, strategy)
	}
	sort.Strings(strategies)
	out := Report{Dataset: dataset.Version, QueriesCount: len(dataset.Incidents), CategoryCounts: dataset.CategoryCounts()}
	for _, strategy := range strategies {
		items := byStrategy[strategy]
		truth := make(map[string]map[string]float64, len(dataset.Incidents))
		for _, incident := range dataset.Incidents {
			relevant := map[string]float64{incident.IncidentID: 1}
			for _, related := range incident.RelatedIncidents {
				relevant[related] = 1
			}
			truth[incident.IncidentID] = relevant
		}
		rankings := make([][]string, 0, len(items))
		relevant := make([]map[string]float64, 0, len(items))
		latencies := make([]float64, 0, len(items))
		for _, item := range items {
			rankings = append(rankings, item.RankedIDs)
			relevant = append(relevant, truth[item.QueryID])
			latencies = append(latencies, float64(item.Latency.Microseconds())/1000)
		}
		ranking := evaluator.EvaluateRanking(rankings, relevant)
		metric := Metrics{Strategy: strategy, Queries: len(items), RecallAt1: ranking.RecallAt1, RecallAt5: ranking.RecallAt5, RecallAt10: ranking.RecallAt10, MRR: ranking.MRR, NDCG: ranking.NDCG}
		sort.Float64s(latencies)
		metric.P50LatencyMS = percentile(latencies, .5)
		metric.P95LatencyMS = percentile(latencies, .95)
		metric.P99LatencyMS = percentile(latencies, .99)
		out.Strategies = append(out.Strategies, metric)
	}
	return out
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	index := int(float64(len(sorted)-1) * p)
	return sorted[index]
}
