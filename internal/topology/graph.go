package topology

import (
	"sort"
	"strings"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

// IncidentGraph is the storage-neutral topology observed for one Incident.
// Node identifiers may contain concrete service names, while Type and edge
// relations provide the stable signal used for cross-service retrieval.
type IncidentGraph struct {
	IncidentID string      `json:"incident_id"`
	Nodes      []GraphNode `json:"nodes"`
	Edges      []GraphEdge `json:"edges"`
}

type GraphNode struct {
	ID       string            `json:"id"`
	Type     string            `json:"type"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type GraphEdge struct {
	Source   string  `json:"source"`
	Target   string  `json:"target"`
	Relation string  `json:"relation"`
	Weight   float64 `json:"weight"`
}

func (g IncidentGraph) Normalize() IncidentGraph {
	for i := range g.Nodes {
		g.Nodes[i].ID = strings.TrimSpace(g.Nodes[i].ID)
		g.Nodes[i].Type = normalizeType(g.Nodes[i].Type)
	}
	for i := range g.Edges {
		g.Edges[i].Source = strings.TrimSpace(g.Edges[i].Source)
		g.Edges[i].Target = strings.TrimSpace(g.Edges[i].Target)
		g.Edges[i].Relation = normalizeRelation(g.Edges[i].Relation)
		if g.Edges[i].Weight <= 0 {
			g.Edges[i].Weight = 1
		}
	}
	sort.Slice(g.Nodes, func(i, j int) bool { return g.Nodes[i].ID < g.Nodes[j].ID })
	sort.Slice(g.Edges, func(i, j int) bool {
		if g.Edges[i].Source != g.Edges[j].Source {
			return g.Edges[i].Source < g.Edges[j].Source
		}
		if g.Edges[i].Target != g.Edges[j].Target {
			return g.Edges[i].Target < g.Edges[j].Target
		}
		return g.Edges[i].Relation < g.Edges[j].Relation
	})
	return g
}

// FromDependencyGraph preserves the existing domain graph while exposing the
// richer node vocabulary used by topology-aware retrieval.
func FromDependencyGraph(incidentID string, graph domain.IncidentDependencyGraph) IncidentGraph {
	out := IncidentGraph{IncidentID: incidentID}
	for _, node := range graph.Nodes {
		typ := node.Kind
		if typ == "datastore" || typ == "critical_dependency" || node.Role == "critical_dependency" {
			typ = inferType(node.ID)
			if typ == "service" {
				typ = "database"
			}
		}
		if typ == "" || typ == "dependency" || typ == "root" {
			typ = inferType(node.ID)
		}
		metadata := map[string]string{}
		for k, v := range node.Metadata {
			metadata[k] = v
		}
		if node.Service != "" {
			metadata["service"] = node.Service
		}
		if node.Resource != "" {
			metadata["resource"] = node.Resource
		}
		out.Nodes = append(out.Nodes, GraphNode{ID: node.ID, Type: typ, Metadata: metadata})
	}
	for _, edge := range graph.Edges {
		relation := edge.Kind
		if relation == "" {
			relation = "depends_on"
		}
		weight := 1.0 + edge.ErrorRate
		if edge.LatencyMS > 0 {
			weight += edge.LatencyMS / 1000
		}
		out.Edges = append(out.Edges, GraphEdge{Source: edge.From, Target: edge.To, Relation: relation, Weight: weight})
	}
	return out.Normalize()
}

func (g IncidentGraph) ToDependencyGraph(rootService string) domain.IncidentDependencyGraph {
	out := domain.IncidentDependencyGraph{RootService: rootService}
	for _, node := range g.Nodes {
		metadata := map[string]string{}
		for k, v := range node.Metadata {
			metadata[k] = v
		}
		out.Nodes = append(out.Nodes, domain.DependencyNode{ID: node.ID, Kind: node.Type, Service: metadata["service"], Resource: metadata["resource"], Role: metadata["role"], Metadata: metadata})
		if node.Type == "database" || node.Type == "cache" || node.Type == "queue" {
			out.SuspectedFailureNodes = append(out.SuspectedFailureNodes, node.ID)
		}
	}
	for _, edge := range g.Edges {
		out.Edges = append(out.Edges, domain.DependencyEdge{From: edge.Source, To: edge.Target, Kind: edge.Relation})
	}
	return out
}

// Merge combines independently observed Trace/Kubernetes graphs with the
// evidence-derived graph. It is observational only and keeps the union
// deterministic so the same resolved Incident produces the same pattern.
func Merge(graphs ...IncidentGraph) IncidentGraph {
	out := IncidentGraph{}
	seenNodes := map[string]bool{}
	seenEdges := map[string]bool{}
	for _, graph := range graphs {
		if out.IncidentID == "" {
			out.IncidentID = graph.IncidentID
		}
		for _, node := range graph.Nodes {
			if node.ID == "" || seenNodes[node.ID] {
				continue
			}
			seenNodes[node.ID] = true
			out.Nodes = append(out.Nodes, node)
		}
		for _, edge := range graph.Edges {
			key := edge.Source + ">" + edge.Target + ":" + edge.Relation
			if edge.Source == "" || edge.Target == "" || seenEdges[key] {
				continue
			}
			seenEdges[key] = true
			out.Edges = append(out.Edges, edge)
		}
	}
	return out.Normalize()
}

func normalizeType(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "critical_dependency" {
		return "database"
	}
	return value
}

func normalizeRelation(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "observed_call" || value == "calls" {
		return "calls"
	}
	if value == "owns" || value == "runs_on" || value == "depends_on" {
		return value
	}
	return "depends_on"
}

func inferType(id string) string {
	lower := strings.ToLower(id)
	switch {
	case strings.Contains(lower, "mysql"), strings.Contains(lower, "postgres"), strings.Contains(lower, "database"):
		return "database"
	case strings.Contains(lower, "redis"), strings.Contains(lower, "memcache"):
		return "cache"
	case strings.Contains(lower, "kafka"), strings.Contains(lower, "queue"):
		return "queue"
	case strings.Contains(lower, "pod"):
		return "pod"
	case strings.Contains(lower, "deployment"):
		return "deployment"
	default:
		return "service"
	}
}
