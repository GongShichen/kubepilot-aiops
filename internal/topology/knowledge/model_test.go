package knowledge

import (
	"context"
	"testing"

	"github.com/kubepilot-aiops/kubepilot/internal/topology"
)

func graph(id, service string) topology.IncidentGraph {
	return topology.IncidentGraph{IncidentID: id, Nodes: []topology.GraphNode{{ID: service, Type: "service"}, {ID: "mysql-01", Type: "database"}}, Edges: []topology.GraphEdge{{Source: service, Target: "mysql-01", Relation: "depends_on", Weight: 1}}}
}

func TestNormalizeAndMergeAbstractsServiceInstances(t *testing.T) {
	first := Normalize(graph("i1", "payment-service"))
	first.Frequency = 1
	first.SourceIncidents = []string{"i1"}
	second := Normalize(graph("i2", "order-service"))
	second.Frequency = 1
	second.SourceIncidents = []string{"i2"}
	if first.PatternID != second.PatternID {
		t.Fatalf("service-specific graphs did not merge: %s != %s", first.PatternID, second.PatternID)
	}
	if len(first.Edges) != 1 || first.Edges[0].Source != "business-service" || first.Edges[0].Target != "mysql" {
		t.Fatalf("unexpected normalized graph: %+v", first)
	}
	merged := Merge(first, ServiceTopologyPattern{PatternID: second.PatternID, Nodes: second.Nodes, Edges: second.Edges, Frequency: 1, SourceIncidents: []string{"i2"}})
	if merged.Frequency != 2 || len(merged.SourceIncidents) != 2 || merged.Confidence <= first.Confidence {
		t.Fatalf("merge did not evolve confidence: %+v", merged)
	}
}

func TestMemoryStoreSearchUsesGraphPattern(t *testing.T) {
	store := NewMemoryStore()
	p := Normalize(graph("i1", "payment-service"))
	p.Frequency = 3
	p.SourceIncidents = []string{"i1", "i2", "i3"}
	if _, err := store.Merge(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	items, err := store.Search(context.Background(), graph("current", "checkout-service"), 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].PatternID != p.PatternID {
		t.Fatalf("shared dependency pattern not recalled: %+v", items)
	}
}

func TestMergeIsIdempotentForSameIncident(t *testing.T) {
	store := NewMemoryStore()
	pattern := Normalize(graph("same", "payment-service"))
	pattern.Frequency = 1
	pattern.SourceIncidents = []string{"same"}
	if _, err := store.Merge(context.Background(), pattern); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Merge(context.Background(), pattern); err != nil {
		t.Fatal(err)
	}
	items, _ := store.List(context.Background(), 10)
	if len(items) != 1 || items[0].Frequency != 1 {
		t.Fatalf("same incident was counted twice: %+v", items)
	}
}
