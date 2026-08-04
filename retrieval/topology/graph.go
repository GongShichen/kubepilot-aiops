package topology

import (
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	internaltopology "github.com/kubepilot-aiops/kubepilot/internal/retrieval/topology"
)

// Public aliases keep the topology contract available to retrieval clients
// while the server continues to enforce the internal capability boundary.
type ServiceNode = internaltopology.ServiceNode
type ServiceEdge = internaltopology.ServiceEdge
type ServiceGraph = internaltopology.ServiceGraph

func FromIncidentGraph(graph domain.IncidentDependencyGraph, namespace string) ServiceGraph {
	return internaltopology.FromIncidentGraph(graph, namespace)
}

func ToIncidentGraph(graph ServiceGraph) domain.IncidentDependencyGraph {
	return internaltopology.ToIncidentGraph(graph)
}
