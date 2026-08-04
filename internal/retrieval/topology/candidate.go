package topology

import (
	"sort"
	"strings"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	incidenttopology "github.com/kubepilot-aiops/kubepilot/internal/topology"
)

// GraphCandidate is the topology-first intermediate representation. The
// historical service is intentionally not part of the primary score.
type GraphCandidate struct {
	IncidentID string
	Namespace  string
	Service    string
	Graph      domain.IncidentDependencyGraph
	Score      float64
}

// GraphCandidateScore combines structure rather than service-name equality.
// The component scores are intentionally exposed through the intermediate
// candidate so callers can audit why a cross-service match was retained.
func GraphCandidateScore(current, historical domain.IncidentDependencyGraph) float64 {
	return incidenttopology.Similarity(incidenttopology.FromDependencyGraph(current.RootService, current), incidenttopology.FromDependencyGraph(historical.RootService, historical)).Score
}

// NeighborOverlap compares dependency roles and target nodes while treating
// each graph's root service as an interchangeable label.
func NeighborOverlap(a, b domain.IncidentDependencyGraph) float64 {
	left, right := neighborSet(a), neighborSet(b)
	return setJaccard(left, right)
}

// DependencyPathSimilarity normalizes the root service in propagation paths,
// so payment-service->mysql and order-service->mysql share the same pattern.
func DependencyPathSimilarity(a, b domain.IncidentDependencyGraph) float64 {
	left, right := normalizedPathSet(a), normalizedPathSet(b)
	return setJaccard(left, right)
}

// GraphDistance is a bounded structural similarity based on node and edge
// cardinality. It complements path/neighbor overlap for partial graphs.
func GraphDistance(a, b domain.IncidentDependencyGraph) float64 {
	nodes := cardinalitySimilarity(len(a.Nodes), len(b.Nodes))
	edges := cardinalitySimilarity(len(a.Edges), len(b.Edges))
	return .5*nodes + .5*edges
}

func neighborSet(graph domain.IncidentDependencyGraph) map[string]bool {
	nodes := map[string]domain.DependencyNode{}
	for _, node := range graph.Nodes {
		nodes[node.ID] = node
	}
	out := map[string]bool{}
	for _, edge := range graph.Edges {
		if edge.From != graph.RootService && edge.From != "" {
			continue
		}
		node := nodes[edge.To]
		role := firstNonEmpty(node.Role, node.Kind, "dependency")
		out[edge.To+":"+role+":"+edge.Kind] = true
	}
	if len(out) == 0 {
		for _, node := range graph.Nodes {
			if node.ID == graph.RootService {
				continue
			}
			out[node.ID+":"+firstNonEmpty(node.Role, node.Kind, "dependency")] = true
		}
	}
	return out
}

func normalizedPathSet(graph domain.IncidentDependencyGraph) map[string]bool {
	root := graph.RootService
	out := map[string]bool{}
	for _, path := range graph.ErrorPropagationPaths {
		if len(path) == 0 {
			continue
		}
		copyPath := append([]string(nil), path...)
		if root != "" && copyPath[0] == root {
			copyPath[0] = "<root>"
		}
		out[strings.Join(copyPath, ">")] = true
	}
	if len(out) == 0 {
		for _, edge := range graph.Edges {
			from := edge.From
			if root != "" && from == root {
				from = "<root>"
			}
			out[from+">"+edge.To] = true
		}
	}
	return out
}

func cardinalitySimilarity(left, right int) float64 {
	if left == 0 && right == 0 {
		return 1
	}
	maximum := left
	if right > maximum {
		maximum = right
	}
	if maximum == 0 {
		return 0
	}
	delta := left - right
	if delta < 0 {
		delta = -delta
	}
	return clamp(1 - float64(delta)/float64(maximum))
}

func setJaccard(left, right map[string]bool) float64 {
	if len(left) == 0 && len(right) == 0 {
		return 1
	}
	union := map[string]bool{}
	intersection := 0
	for value := range left {
		union[value] = true
		if right[value] {
			intersection++
		}
	}
	for value := range right {
		union[value] = true
	}
	if len(union) == 0 {
		return 0
	}
	return float64(intersection) / float64(len(union))
}

// RankGraphCandidates performs the in-memory graph-first phase after the
// store has returned node-overlap seeds. It is deterministic and stable.
func RankGraphCandidates(current domain.IncidentDependencyGraph, candidates []GraphCandidate, limit int) []GraphCandidate {
	out := append([]GraphCandidate(nil), candidates...)
	for index := range out {
		out[index].Score = GraphCandidateScore(current, out[index].Graph)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].IncidentID < out[j].IncidentID
		}
		return out[i].Score > out[j].Score
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}
