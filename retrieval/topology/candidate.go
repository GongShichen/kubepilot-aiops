package topology

import (
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	internaltopology "github.com/kubepilot-aiops/kubepilot/internal/retrieval/topology"
)

type GraphQuery = internaltopology.GraphQuery
type GraphCandidate = internaltopology.GraphCandidate

func BuildGraphQuery(features domain.IncidentFeatures, limit int) GraphQuery {
	return internaltopology.BuildGraphQuery(features, limit)
}

func GraphCandidateScore(current, historical domain.IncidentDependencyGraph) float64 {
	return internaltopology.GraphCandidateScore(current, historical)
}

func NeighborOverlap(current, historical domain.IncidentDependencyGraph) float64 {
	return internaltopology.NeighborOverlap(current, historical)
}

func DependencyPathSimilarity(current, historical domain.IncidentDependencyGraph) float64 {
	return internaltopology.DependencyPathSimilarity(current, historical)
}

func GraphDistance(current, historical domain.IncidentDependencyGraph) float64 {
	return internaltopology.GraphDistance(current, historical)
}

func RankGraphCandidates(current domain.IncidentDependencyGraph, candidates []GraphCandidate, limit int) []GraphCandidate {
	return internaltopology.RankGraphCandidates(current, candidates, limit)
}
