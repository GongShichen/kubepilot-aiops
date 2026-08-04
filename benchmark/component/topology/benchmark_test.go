package topology

import (
	"github.com/kubepilot-aiops/kubepilot/internal/topology"
	"testing"
)

func TestTopologyBenchmarkRecognizesSharedDependency(t *testing.T) {
	q := topology.IncidentGraph{IncidentID: "q", Nodes: []topology.GraphNode{{ID: "payment-service", Type: "service"}, {ID: "mysql", Type: "database"}}, Edges: []topology.GraphEdge{{Source: "payment-service", Target: "mysql", Relation: "depends_on"}}}
	h := topology.IncidentGraph{IncidentID: "h", Nodes: []topology.GraphNode{{ID: "order-service", Type: "service"}, {ID: "mysql", Type: "database"}}, Edges: []topology.GraphEdge{{Source: "order-service", Target: "mysql", Relation: "depends_on"}}}
	m := Evaluate([]Case{{Query: q, Ranked: []topology.IncidentGraph{h}, Relevant: []string{"h"}}})
	if m.TopologyRecall != 1 || m.GraphSimilarityAccuracy != 1 {
		t.Fatalf("unexpected topology metrics: %+v", m)
	}
}
