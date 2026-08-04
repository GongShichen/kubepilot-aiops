package topology

import (
	"testing"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func graph(root, dependency string) domain.IncidentDependencyGraph {
	return domain.IncidentDependencyGraph{
		RootService:           root,
		Nodes:                 []domain.DependencyNode{{ID: root, Role: "root"}, {ID: dependency, Role: "critical_dependency"}},
		Edges:                 []domain.DependencyEdge{{From: root, To: dependency, Kind: "observed_call"}},
		SuspectedFailureNodes: []string{dependency},
		ErrorPropagationPaths: [][]string{{root, dependency}},
	}
}

func TestGraphFirstCandidateGenerationIgnoresRenamedRootAndNamespace(t *testing.T) {
	current := graph("payment-service", "mysql")
	historical := graph("order-service", "mysql")
	unrelated := graph("gateway-service", "redis")
	if NeighborOverlap(current, historical) <= NeighborOverlap(current, unrelated) {
		t.Fatalf("neighbor overlap did not prefer shared dependency")
	}
	if DependencyPathSimilarity(current, historical) <= DependencyPathSimilarity(current, unrelated) {
		t.Fatalf("path similarity did not prefer shared dependency")
	}
	ranked := RankGraphCandidates(current, []GraphCandidate{{IncidentID: "unrelated", Namespace: "ns-a", Graph: unrelated}, {IncidentID: "shared", Namespace: "ns-b", Graph: historical}}, 1)
	if len(ranked) != 1 || ranked[0].IncidentID != "shared" {
		t.Fatalf("graph-first ranking=%+v", ranked)
	}
}

func TestBuildGraphQueryIncludesDependenciesWithoutNamespaceFilter(t *testing.T) {
	query := BuildGraphQuery(domain.IncidentFeatures{Namespace: "ns-a", Service: "payment-service", TopologyGraph: graph("payment-service", "mysql")}, 50)
	if query.CandidateLimit < 100 || len(query.NodeIDs) != 2 || query.NodeIDs[0] != "mysql" || query.NodeIDs[1] != "payment-service" {
		t.Fatalf("unexpected graph query: %+v", query)
	}
}
