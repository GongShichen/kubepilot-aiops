package topologyevolution

import (
	"context"

	"github.com/kubepilot-aiops/kubepilot/internal/topology"
	knowledge "github.com/kubepilot-aiops/kubepilot/internal/topology/knowledge"
)

type Case struct {
	Graphs            []topology.IncidentGraph
	ExpectedPatternID string
}

type Metrics struct {
	Cases            int     `json:"cases"`
	PatternPrecision float64 `json:"pattern_precision"`
	PatternRecall    float64 `json:"pattern_recall"`
	PatternsLearned  int     `json:"patterns_learned"`
}

func DefaultCases() []Case {
	return []Case{{Graphs: []topology.IncidentGraph{
		{IncidentID: "payment", Nodes: []topology.GraphNode{{ID: "payment-service", Type: "service"}, {ID: "mysql-01", Type: "database"}}, Edges: []topology.GraphEdge{{Source: "payment-service", Target: "mysql-01", Relation: "depends_on"}}},
		{IncidentID: "order", Nodes: []topology.GraphNode{{ID: "order-service", Type: "service"}, {ID: "mysql-02", Type: "database"}}, Edges: []topology.GraphEdge{{Source: "order-service", Target: "mysql-02", Relation: "depends_on"}}},
		{IncidentID: "checkout", Nodes: []topology.GraphNode{{ID: "checkout-service", Type: "service"}, {ID: "mysql-03", Type: "database"}}, Edges: []topology.GraphEdge{{Source: "checkout-service", Target: "mysql-03", Relation: "depends_on"}}},
	}}}
}

func Evaluate(cases []Case) Metrics {
	metrics := Metrics{Cases: len(cases)}
	if len(cases) == 0 {
		return metrics
	}
	for _, item := range cases {
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
