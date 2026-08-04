package topology

import (
	"sort"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func Similarity(current, historical domain.IncidentDependencyGraph) float64 {
	edges := directedEdgeJaccard(current.Edges, historical.Edges)
	dependency := sharedCriticalDependency(current, historical)
	paths := pathSimilarity(current.ErrorPropagationPaths, historical.ErrorPropagationPaths)
	roles := failingRoleSimilarity(current, historical)
	return clamp(.40*edges + .30*dependency + .20*paths + .10*roles)
}

func directedEdgeJaccard(a, b []domain.DependencyEdge) float64 {
	left, right := edgeSet(a), edgeSet(b)
	if len(left) == 0 && len(right) == 0 {
		return 1
	}
	intersection := 0
	union := map[string]bool{}
	for key := range left {
		union[key] = true
		if right[key] {
			intersection++
		}
	}
	for key := range right {
		union[key] = true
	}
	return float64(intersection) / float64(len(union))
}

func edgeSet(edges []domain.DependencyEdge) map[string]bool {
	out := map[string]bool{}
	for _, edge := range edges {
		out[edge.From+">"+edge.To+":"+edge.Kind] = true
	}
	return out
}

func sharedCriticalDependency(a, b domain.IncidentDependencyGraph) float64 {
	left, right := failureSet(a), failureSet(b)
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	matched := 0
	for key := range left {
		if right[key] {
			matched++
		}
	}
	denominator := len(left)
	if len(right) > denominator {
		denominator = len(right)
	}
	return float64(matched) / float64(denominator)
}

func failureSet(graph domain.IncidentDependencyGraph) map[string]bool {
	out := map[string]bool{}
	for _, id := range graph.SuspectedFailureNodes {
		out[id] = true
	}
	return out
}

func pathSimilarity(a, b [][]string) float64 {
	best := 0.0
	for _, left := range a {
		for _, right := range b {
			if score := sequenceJaccard(left, right); score > best {
				best = score
			}
		}
	}
	return best
}

func sequenceJaccard(a, b []string) float64 {
	left, right := map[string]bool{}, map[string]bool{}
	for index := 0; index+1 < len(a); index++ {
		left[a[index]+">"+a[index+1]] = true
	}
	for index := 0; index+1 < len(b); index++ {
		right[b[index]+">"+b[index+1]] = true
	}
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	intersection, union := 0, map[string]bool{}
	for key := range left {
		union[key] = true
		if right[key] {
			intersection++
		}
	}
	for key := range right {
		union[key] = true
	}
	return float64(intersection) / float64(len(union))
}

func failingRoleSimilarity(a, b domain.IncidentDependencyGraph) float64 {
	roles := func(graph domain.IncidentDependencyGraph) []string {
		var out []string
		selected := failureSet(graph)
		for _, node := range graph.Nodes {
			if selected[node.ID] {
				out = append(out, node.Role)
			}
		}
		sort.Strings(out)
		return out
	}
	left, right := roles(a), roles(b)
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	for _, l := range left {
		for _, r := range right {
			if l != "" && l == r {
				return 1
			}
		}
	}
	return 0
}

func clamp(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}
