package topology

import (
	"sort"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

// GraphQuery describes the bounded, graph-first candidate lookup performed by
// the historical store. Node IDs are used only as an inexpensive index seed;
// GraphCandidateScore remains the authoritative topology signal.
type GraphQuery struct {
	NodeIDs        []string `json:"node_ids"`
	CriticalNodes  []string `json:"critical_nodes,omitempty"`
	CandidateLimit int      `json:"candidate_limit"`
}

// BuildGraphQuery creates a query seed from the observed dependency graph. It
// deliberately does not include a namespace predicate: topology retrieval
// must be able to find the same dependency pattern in another namespace.
func BuildGraphQuery(features domain.IncidentFeatures, requestedLimit int) GraphQuery {
	seen := map[string]bool{}
	critical := map[string]bool{}
	add := func(value string) {
		if value != "" && !seen[value] {
			seen[value] = true
		}
	}
	for _, node := range features.TopologyGraph.Nodes {
		add(node.ID)
		if node.Role == "critical_dependency" || node.Role == "database" || node.Role == "cache" {
			critical[node.ID] = true
		}
	}
	for _, service := range features.TopologyServices {
		add(service)
	}
	add(features.Service)
	if len(seen) == 0 {
		// Keep the SQL predicate non-empty while returning no accidental broad
		// match for an incident without topology information.
		add("__missing_topology__")
	}
	limit := requestedLimit * 20
	if limit < 100 {
		limit = 100
	}
	nodes := make([]string, 0, len(seen))
	for node := range seen {
		nodes = append(nodes, node)
	}
	sort.Strings(nodes)
	criticalNodes := make([]string, 0, len(critical))
	for node := range critical {
		criticalNodes = append(criticalNodes, node)
	}
	sort.Strings(criticalNodes)
	return GraphQuery{NodeIDs: nodes, CriticalNodes: criticalNodes, CandidateLimit: limit}
}
