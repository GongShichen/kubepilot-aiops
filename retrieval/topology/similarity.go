package topology

import (
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	internaltopology "github.com/kubepilot-aiops/kubepilot/internal/retrieval/topology"
)

func Similarity(current, historical domain.IncidentDependencyGraph) float64 {
	return internaltopology.Similarity(current, historical)
}

func SimilarityGraph(current, historical ServiceGraph) float64 {
	return internaltopology.SimilarityGraph(current, historical)
}
