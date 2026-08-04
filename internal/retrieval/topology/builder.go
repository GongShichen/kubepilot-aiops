package topology

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

// ServiceNode is the storage-neutral representation of a service or a
// critical dependency in an Incident dependency graph.
type ServiceNode struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
}

// ServiceEdge describes a directed observed dependency.
type ServiceEdge struct {
	Source string  `json:"source"`
	Target string  `json:"target"`
	Type   string  `json:"type"`
	Weight float64 `json:"weight"`
}

// ServiceGraph is the topology-facing contract. Domain Incident graphs are
// converted to this form before persistence or similarity comparison.
type ServiceGraph struct {
	Nodes []ServiceNode `json:"nodes"`
	Edges []ServiceEdge `json:"edges"`
}

// FromIncidentGraph converts the domain graph to the topology contract while
// preserving namespaces and node metadata where present.
func FromIncidentGraph(graph domain.IncidentDependencyGraph, namespace string) ServiceGraph {
	out := ServiceGraph{Nodes: make([]ServiceNode, 0, len(graph.Nodes)), Edges: make([]ServiceEdge, 0, len(graph.Edges))}
	for _, node := range graph.Nodes {
		labels := map[string]string{}
		for key, value := range node.Metadata {
			labels[key] = value
		}
		if node.Role != "" {
			labels["role"] = node.Role
		}
		if node.Kind != "" {
			labels["kind"] = node.Kind
		}
		if node.Service != "" {
			labels["service"] = node.Service
		}
		if node.Resource != "" {
			labels["resource"] = node.Resource
		}
		out.Nodes = append(out.Nodes, ServiceNode{Name: node.ID, Namespace: firstNonEmpty(namespace, labels["namespace"]), Labels: labels})
	}
	for _, edge := range graph.Edges {
		weight := 1.0
		if edge.ErrorRate > 0 {
			weight += edge.ErrorRate
		}
		out.Edges = append(out.Edges, ServiceEdge{Source: edge.From, Target: edge.To, Type: edge.Kind, Weight: weight})
	}
	sort.Slice(out.Nodes, func(i, j int) bool { return out.Nodes[i].Name < out.Nodes[j].Name })
	sort.Slice(out.Edges, func(i, j int) bool {
		if out.Edges[i].Source == out.Edges[j].Source {
			return out.Edges[i].Target < out.Edges[j].Target
		}
		return out.Edges[i].Source < out.Edges[j].Source
	})
	return out
}

// ToIncidentGraph converts the topology contract into the persisted domain
// graph. The domain graph retains the richer failure/path fields; this
// adapter is intentionally lossless for nodes and edges.
func ToIncidentGraph(graph ServiceGraph) domain.IncidentDependencyGraph {
	out := domain.IncidentDependencyGraph{}
	for _, node := range graph.Nodes {
		metadata := map[string]string{}
		for key, value := range node.Labels {
			metadata[key] = value
		}
		if node.Namespace != "" {
			metadata["namespace"] = node.Namespace
		}
		role := metadata["role"]
		if role == "" && isCriticalDependency(node.Name) {
			role = "critical_dependency"
		}
		out.Nodes = append(out.Nodes, domain.DependencyNode{ID: node.Name, Kind: metadata["kind"], Service: metadata["service"], Resource: metadata["resource"], Role: role, Metadata: metadata})
		if role == "critical_dependency" || isCriticalDependency(node.Name) {
			out.SuspectedFailureNodes = append(out.SuspectedFailureNodes, node.Name)
		}
	}
	for _, edge := range graph.Edges {
		out.Edges = append(out.Edges, domain.DependencyEdge{From: edge.Source, To: edge.Target, Kind: edge.Type})
	}
	sort.Slice(out.Nodes, func(i, j int) bool { return out.Nodes[i].ID < out.Nodes[j].ID })
	sort.Slice(out.Edges, func(i, j int) bool {
		if out.Edges[i].From == out.Edges[j].From {
			return out.Edges[i].To < out.Edges[j].To
		}
		return out.Edges[i].From < out.Edges[j].From
	})
	return out
}

func isCriticalDependency(name string) bool {
	switch name {
	case "mysql", "postgres", "postgresql", "redis", "kafka", "database":
		return true
	default:
		return false
	}
}

var serviceTokenPattern = regexp.MustCompile(`(?i)\b[a-z0-9][a-z0-9-]*(?:-service|mysql|postgres(?:ql)?|redis|kafka|database)\b`)

// Build constructs a graph from observability evidence. Collectors normally
// provide richer metadata (upstream_service, dependency, endpoint_service),
// while the token fallback covers trace summaries and Kubernetes events.
func Build(incident *domain.Incident, evidence []domain.Evidence) ServiceGraph {
	graph := ServiceGraph{}
	if incident == nil {
		return graph
	}
	nodes := map[string]ServiceNode{}
	edges := map[string]ServiceEdge{}
	addNode := func(name string, labels map[string]string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if _, exists := nodes[name]; exists {
			return
		}
		copyLabels := map[string]string{}
		for key, value := range labels {
			copyLabels[key] = value
		}
		nodes[name] = ServiceNode{Name: name, Namespace: incident.Namespace, Labels: copyLabels}
	}
	addEdge := func(source, target, edgeType string) {
		if source == "" || target == "" || source == target {
			return
		}
		addNode(source, nil)
		addNode(target, nil)
		key := source + ">" + target + ":" + edgeType
		edges[key] = ServiceEdge{Source: source, Target: target, Type: edgeType, Weight: 1}
	}
	addNode(incident.Service, map[string]string{"role": "root"})
	for _, item := range evidence {
		source := item.Service
		if source == "" {
			source = incident.Service
		}
		addNode(source, map[string]string{"role": "service"})
		values := []string{item.Summary}
		for _, payload := range []map[string]any{item.Content, item.Data} {
			for _, key := range []string{"upstream_service", "downstream_service", "dependency", "target_service", "endpoint_service"} {
				if value, ok := payload[key].(string); ok {
					addEdge(source, value, key)
				}
			}
			if payload != nil {
				for key, value := range payload {
					if key != "query" {
						values = append(values, key, stringify(value))
					}
				}
			}
		}
		for _, dependency := range serviceTokenPattern.FindAllString(strings.Join(values, " "), -1) {
			if dependency != source {
				addEdge(source, dependency, "observed_call")
			}
		}
	}
	for _, node := range nodes {
		graph.Nodes = append(graph.Nodes, node)
	}
	for _, edge := range edges {
		graph.Edges = append(graph.Edges, edge)
	}
	sort.Slice(graph.Nodes, func(i, j int) bool { return graph.Nodes[i].Name < graph.Nodes[j].Name })
	sort.Slice(graph.Edges, func(i, j int) bool {
		if graph.Edges[i].Source == graph.Edges[j].Source {
			return graph.Edges[i].Target < graph.Edges[j].Target
		}
		return graph.Edges[i].Source < graph.Edges[j].Source
	})
	return graph
}

func stringify(value any) string {
	return strings.TrimSpace(fmt.Sprint(value))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
