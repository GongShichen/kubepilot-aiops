package extractor

import (
	"testing"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/internal/topology"
)

func TestEvaluationIncidentCannotProduceTopologyKnowledge(t *testing.T) {
	in := &domain.Incident{ID: "b", Namespace: "kubepilot-benchmark", Status: domain.StatusResolved}
	graph := topology.IncidentGraph{Nodes: []topology.GraphNode{{ID: "payment", Type: "service"}, {ID: "mysql", Type: "database"}}, Edges: []topology.GraphEdge{{Source: "payment", Target: "mysql", Relation: "depends_on"}}}
	if _, err := Extract(in, graph); err == nil {
		t.Fatal("benchmark topology was accepted")
	}
}
