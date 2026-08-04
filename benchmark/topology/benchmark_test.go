package topology

import (
	"testing"

	graph "github.com/kubepilot-aiops/kubepilot/internal/topology"
)

func TestEvaluateCrossServiceSharedDependency(t *testing.T) {
	query := graph.IncidentGraph{IncidentID: "query", Nodes: []graph.GraphNode{{ID: "payment-service", Type: "service"}, {ID: "mysql", Type: "database"}}, Edges: []graph.GraphEdge{{Source: "payment-service", Target: "mysql", Relation: "depends_on"}}}
	match := graph.IncidentGraph{IncidentID: "match", Nodes: []graph.GraphNode{{ID: "order-service", Type: "service"}, {ID: "mysql", Type: "database"}}, Edges: []graph.GraphEdge{{Source: "order-service", Target: "mysql", Relation: "depends_on"}}}
	metrics := Evaluate([]Case{{Query: query, Candidates: []graph.IncidentGraph{match}, RelevantIDs: []string{"match"}}})
	if metrics.RecallAt1 != 1 || metrics.MRR != 1 {
		t.Fatalf("unexpected topology benchmark metrics: %+v", metrics)
	}
}
