package topology

import "github.com/kubepilot-aiops/kubepilot/internal/domain"

// Similarity is the domain-graph entry point for the canonical topology
// score. GraphCandidateScore owns the only scoring implementation so every
// production caller uses neighbor, dependency-path, and node-type similarity.
func Similarity(current, historical domain.IncidentDependencyGraph) float64 {
	return GraphCandidateScore(current, historical)
}
