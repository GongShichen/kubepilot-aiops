package topology

import "testing"

func TestSimilarityRecognizesSharedDatabaseAcrossServiceNames(t *testing.T) {
	current := IncidentGraph{IncidentID: "current", Nodes: []GraphNode{{ID: "payment-service", Type: "service"}, {ID: "mysql", Type: "database"}}, Edges: []GraphEdge{{Source: "payment-service", Target: "mysql", Relation: "depends_on"}}}
	historical := IncidentGraph{IncidentID: "historical", Nodes: []GraphNode{{ID: "order-service", Type: "service"}, {ID: "mysql", Type: "database"}}, Edges: []GraphEdge{{Source: "order-service", Target: "mysql", Relation: "depends_on"}}}
	got := Similarity(current, historical)
	if got.Score < .99 || got.Neighbor < .99 || got.Path < .99 || got.NodeType < .99 {
		t.Fatalf("unexpected graph similarity: %+v", got)
	}
}
