package topology

import (
	"testing"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func TestBuildServiceGraphUsesObservedDependencyMetadata(t *testing.T) {
	graph := Build(&domain.Incident{Namespace: "kubepilot-demo", Service: "payment-service"}, []domain.Evidence{{Service: "payment-service", Content: map[string]any{"dependency": "mysql"}}})
	if len(graph.Nodes) < 2 || len(graph.Edges) != 1 {
		t.Fatalf("dependency graph was not built from evidence: %+v", graph)
	}
	if graph.Edges[0].Source != "payment-service" || graph.Edges[0].Target != "mysql" {
		t.Fatalf("unexpected dependency edge: %+v", graph.Edges[0])
	}
}

func TestServiceGraphSimilarityRecognizesRenamedRootWithSharedDependency(t *testing.T) {
	current := ServiceGraph{Nodes: []ServiceNode{{Name: "payment-service"}, {Name: "mysql"}}, Edges: []ServiceEdge{{Source: "payment-service", Target: "mysql", Type: "observed_call", Weight: 1}}}
	historical := ServiceGraph{Nodes: []ServiceNode{{Name: "order-service"}, {Name: "mysql"}}, Edges: []ServiceEdge{{Source: "order-service", Target: "mysql", Type: "observed_call", Weight: 1}}}
	if score := SimilarityGraph(current, historical); score < .999999 {
		t.Fatalf("shared dependency similarity=%f, want a complete structural match", score)
	}
}
