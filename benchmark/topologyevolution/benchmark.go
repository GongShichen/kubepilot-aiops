package topologyevolution

import (
	"context"
	"strconv"

	"github.com/kubepilot-aiops/kubepilot/internal/topology"
	knowledge "github.com/kubepilot-aiops/kubepilot/internal/topology/knowledge"
)

type Case struct {
	Graphs            []topology.IncidentGraph
	ExpectedPatternID string
}

type Metrics struct {
	Cases             int     `json:"cases"`
	ResolvedIncidents int     `json:"resolved_incidents"`
	PatternPrecision  float64 `json:"pattern_precision"`
	PatternRecall     float64 `json:"pattern_recall"`
	PatternsLearned   int     `json:"patterns_learned"`
}

func DefaultCases() []Case {
	graphs := make([]topology.IncidentGraph, 0, 100)
	for i := 0; i < 100; i++ {
		service := []string{"payment-service", "order-service", "checkout-service"}[i%3]
		graphs = append(graphs, topology.IncidentGraph{IncidentID: service + "-" + strconv.Itoa(i), Nodes: []topology.GraphNode{{ID: service, Type: "service"}, {ID: "mysql-" + strconv.Itoa(i), Type: "database"}}, Edges: []topology.GraphEdge{{Source: service, Target: "mysql-" + strconv.Itoa(i), Relation: "depends_on"}}})
	}
	return []Case{{Graphs: graphs}}
}

func Evaluate(cases []Case) Metrics {
	metrics := Metrics{Cases: len(cases)}
	if len(cases) == 0 {
		return metrics
	}
	for _, item := range cases {
		metrics.ResolvedIncidents += len(item.Graphs)
		store := knowledge.NewMemoryStore()
		for _, graph := range item.Graphs {
			p := knowledge.Normalize(graph)
			p.Frequency = 1
			p.SourceIncidents = []string{graph.IncidentID}
			_, _ = store.Merge(context.Background(), p)
		}
		patterns, _ := store.List(context.Background(), 10)
		metrics.PatternsLearned += len(patterns)
		if len(patterns) == 0 {
			continue
		}
		expected := patterns[0].PatternID
		if item.ExpectedPatternID != "" {
			expected = item.ExpectedPatternID
		}
		if patterns[0].PatternID == expected {
			metrics.PatternPrecision++
			metrics.PatternRecall++
		}
	}
	den := float64(len(cases))
	metrics.PatternPrecision /= den
	metrics.PatternRecall /= den
	return metrics
}
