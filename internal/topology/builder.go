package topology

import (
	"fmt"
	"strings"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

// Builder consumes only server-owned Incident and Evidence observations. The
// payload is treated as untrusted; it can add observed relationships but never
// changes the Incident namespace or grants mutation authority.
func Build(incident *domain.Incident, evidence []domain.Evidence) IncidentGraph {
	if incident == nil {
		return IncidentGraph{}
	}
	g := IncidentGraph{IncidentID: incident.ID}
	addNode := func(id, typ string, metadata map[string]string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		for _, n := range g.Nodes {
			if n.ID == id {
				return
			}
		}
		copyMeta := map[string]string{"namespace": incident.Namespace}
		for k, v := range metadata {
			copyMeta[k] = v
		}
		g.Nodes = append(g.Nodes, GraphNode{ID: id, Type: typ, Metadata: copyMeta})
	}
	addEdge := func(source, target, relation string) {
		if strings.TrimSpace(source) == "" || strings.TrimSpace(target) == "" || source == target {
			return
		}
		addNode(source, "service", nil)
		addNode(target, inferType(target), nil)
		for _, e := range g.Edges {
			if e.Source == source && e.Target == target && e.Relation == relation {
				return
			}
		}
		g.Edges = append(g.Edges, GraphEdge{Source: source, Target: target, Relation: relation, Weight: 1})
	}
	addNode(incident.Service, "service", map[string]string{"role": "root", "resource": incident.Resource})
	for _, ev := range evidence {
		source := ev.Service
		if source == "" {
			source = incident.Service
		}
		addNode(source, "service", map[string]string{"resource": ev.Resource})
		values := []map[string]any{ev.Content, ev.Data}
		for _, payload := range values {
			for _, key := range []string{"dependency", "downstream_service", "endpoint_service", "target_service", "upstream_service"} {
				if value, ok := payload[key].(string); ok {
					addEdge(source, value, relationForEvidence(key))
				}
			}
			for _, key := range []string{"pod", "pod_name"} {
				if value, ok := payload[key].(string); ok {
					addNode(value, "pod", nil)
					addEdge(value, source, "runs_on")
				}
			}
			for _, key := range []string{"deployment", "deployment_name"} {
				if value, ok := payload[key].(string); ok {
					addNode(value, "deployment", nil)
					addEdge(value, source, "owns")
				}
			}
		}
		text := strings.ToLower(fmt.Sprint(ev.Summary, " ", ev.Content, " ", ev.Data))
		for _, token := range []string{"mysql", "postgres", "postgresql", "redis", "kafka"} {
			if strings.Contains(text, token) {
				addEdge(source, token, "depends_on")
			}
		}
	}
	return g.Normalize()
}

func relationForEvidence(key string) string {
	switch key {
	case "upstream_service":
		return "calls"
	case "pod", "pod_name":
		return "runs_on"
	default:
		return "depends_on"
	}
}
