package topology

import (
	"github.com/kubepilot-aiops/kubepilot/benchmark/evaluator"
	"github.com/kubepilot-aiops/kubepilot/internal/topology"
)

type Case struct {
	Query    topology.IncidentGraph
	Ranked   []topology.IncidentGraph
	Relevant []string
}
type Metrics struct {
	Cases                   int                      `json:"cases"`
	TopologyRecall          float64                  `json:"topology_recall"`
	GraphSimilarityAccuracy float64                  `json:"graph_similarity_accuracy"`
	Ranking                 evaluator.RankingMetrics `json:"ranking"`
}

func Evaluate(cases []Case) Metrics {
	result := Metrics{Cases: len(cases)}
	rankings := make([][]string, 0, len(cases))
	relevant := make([]map[string]float64, 0, len(cases))
	for _, c := range cases {
		ids := make([]string, 0, len(c.Ranked))
		for _, g := range c.Ranked {
			ids = append(ids, g.IncidentID)
		}
		rankings = append(rankings, ids)
		rel := map[string]float64{}
		for _, id := range c.Relevant {
			rel[id] = 1
		}
		relevant = append(relevant, rel)
		if len(ids) > 0 {
			for i, id := range ids {
				if rel[id] > 0 {
					if i < 5 {
						result.TopologyRecall++
					}
					if i == 0 {
						result.GraphSimilarityAccuracy++
					}
					break
				}
			}
		}
	}
	if result.Cases > 0 {
		result.TopologyRecall /= float64(result.Cases)
		result.GraphSimilarityAccuracy /= float64(result.Cases)
	}
	result.Ranking = evaluator.EvaluateRanking(rankings, relevant)
	return result
}
