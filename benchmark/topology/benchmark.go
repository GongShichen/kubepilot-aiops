package topology

import (
	"sort"

	"github.com/kubepilot-aiops/kubepilot/internal/topology"
)

type Case struct {
	Query       topology.IncidentGraph
	Candidates  []topology.IncidentGraph
	RelevantIDs []string
}

type Metrics struct {
	Cases     int     `json:"cases"`
	RecallAt1 float64 `json:"recall_at_1"`
	RecallAt5 float64 `json:"recall_at_5"`
	MRR       float64 `json:"mrr"`
}

func DefaultCases() []Case {
	query := topology.IncidentGraph{IncidentID: "query", Nodes: []topology.GraphNode{{ID: "payment-service", Type: "service"}, {ID: "mysql", Type: "database"}}, Edges: []topology.GraphEdge{{Source: "payment-service", Target: "mysql", Relation: "depends_on"}}}
	match := topology.IncidentGraph{IncidentID: "shared-database", Nodes: []topology.GraphNode{{ID: "order-service", Type: "service"}, {ID: "mysql", Type: "database"}}, Edges: []topology.GraphEdge{{Source: "order-service", Target: "mysql", Relation: "depends_on"}}}
	return []Case{{Query: query, Candidates: []topology.IncidentGraph{match}, RelevantIDs: []string{match.IncidentID}}}
}

func Evaluate(cases []Case) Metrics {
	metrics := Metrics{Cases: len(cases)}
	if len(cases) == 0 {
		return metrics
	}
	for _, item := range cases {
		candidates := append([]topology.IncidentGraph(nil), item.Candidates...)
		sort.SliceStable(candidates, func(i, j int) bool {
			return topology.Similarity(item.Query, candidates[i]).Score > topology.Similarity(item.Query, candidates[j]).Score
		})
		relevant := map[string]bool{}
		for _, id := range item.RelevantIDs {
			relevant[id] = true
		}
		for rank, candidate := range candidates {
			if relevant[candidate.IncidentID] {
				if rank == 0 {
					metrics.RecallAt1++
				}
				if rank < 5 {
					metrics.RecallAt5++
				}
				metrics.MRR += 1 / float64(rank+1)
				break
			}
		}
	}
	denominator := float64(len(cases))
	metrics.RecallAt1 /= denominator
	metrics.RecallAt5 /= denominator
	metrics.MRR /= denominator
	return metrics
}
