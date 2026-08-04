package topology

import (
	"testing"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func TestTopologySimilarityRecognizesSharedDependencyAcrossServices(t *testing.T) {
	current := domain.IncidentDependencyGraph{RootService: "payment", Nodes: []domain.DependencyNode{{ID: "mysql", Role: "database"}}, Edges: []domain.DependencyEdge{{From: "payment", To: "mysql", Kind: "calls"}}, SuspectedFailureNodes: []string{"mysql"}, ErrorPropagationPaths: [][]string{{"payment", "mysql"}}}
	historical := domain.IncidentDependencyGraph{RootService: "order", Nodes: []domain.DependencyNode{{ID: "mysql", Role: "database"}}, Edges: []domain.DependencyEdge{{From: "order", To: "mysql", Kind: "calls"}}, SuspectedFailureNodes: []string{"mysql"}, ErrorPropagationPaths: [][]string{{"order", "mysql"}}}
	score := Similarity(current, historical)
	if score < .39 || score > .41 {
		t.Fatalf("expected shared dependency and role score near .4, got %.4f", score)
	}
	if identical := Similarity(current, current); identical < .999999 {
		t.Fatalf("identical topology must score 1, got %.4f", identical)
	}
}
