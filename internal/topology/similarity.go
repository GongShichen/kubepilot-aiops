package topology

import "strings"

type SimilarityBreakdown struct {
	Neighbor float64 `json:"neighbor_similarity"`
	Path     float64 `json:"path_similarity"`
	NodeType float64 `json:"node_type_similarity"`
	Score    float64 `json:"score"`
}

// Similarity deliberately compares graph roles and relations rather than
// concrete service names, allowing payment->mysql to match order->mysql.
func Similarity(current, historical IncidentGraph) SimilarityBreakdown {
	current = current.Normalize()
	historical = historical.Normalize()
	neighbor := neighborSimilarity(current, historical)
	path := pathSimilarity(current, historical)
	nodeType := nodeTypeSimilarity(current, historical)
	return SimilarityBreakdown{Neighbor: neighbor, Path: path, NodeType: nodeType, Score: clamp(.40*neighbor + .30*path + .30*nodeType)}
}

func neighborSimilarity(a, b IncidentGraph) float64 {
	left, right := map[string]bool{}, map[string]bool{}
	for _, e := range a.Edges {
		left[neighborSignature(a, e)] = true
	}
	for _, e := range b.Edges {
		right[neighborSignature(b, e)] = true
	}
	return jaccard(left, right)
}

func neighborSignature(g IncidentGraph, e GraphEdge) string {
	types := map[string]string{}
	for _, n := range g.Nodes {
		types[n.ID] = n.Type
	}
	return types[e.Source] + ">" + types[e.Target] + ":" + e.Relation
}

func pathSimilarity(a, b IncidentGraph) float64 {
	left, right := map[string]bool{}, map[string]bool{}
	for _, e := range a.Edges {
		left[normalizeRelation(e.Relation)+":"+nodeType(a, e.Source)+">"+nodeType(a, e.Target)] = true
	}
	for _, e := range b.Edges {
		right[normalizeRelation(e.Relation)+":"+nodeType(b, e.Source)+">"+nodeType(b, e.Target)] = true
	}
	return jaccard(left, right)
}

func nodeTypeSimilarity(a, b IncidentGraph) float64 {
	left, right := map[string]bool{}, map[string]bool{}
	for _, n := range a.Nodes {
		left[strings.ToLower(n.Type)] = true
	}
	for _, n := range b.Nodes {
		right[strings.ToLower(n.Type)] = true
	}
	return jaccard(left, right)
}

func nodeType(g IncidentGraph, id string) string {
	for _, n := range g.Nodes {
		if n.ID == id {
			return n.Type
		}
	}
	return "service"
}
func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1
	}
	union, intersection := 0, 0
	for k := range a {
		union++
		if b[k] {
			intersection++
		}
	}
	for k := range b {
		if !a[k] {
			union++
		}
	}
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}
func clamp(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
