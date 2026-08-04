package topology

// SimilarityGraph is the public topology-contract adapter. Keeping this
// function next to the graph builder makes the production retrieval contract
// explicit while the domain-level Similarity function remains compatible with
// existing reasoning callers.
func SimilarityGraph(current, historical ServiceGraph) float64 {
	return Similarity(ToIncidentGraph(current), ToIncidentGraph(historical))
}
