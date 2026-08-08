package worldmodel

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/brainruntime"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/internal/topology"
)

// Build creates an operational state projection from canonical server-owned
// Evidence. Free-form model output is never an input to this builder.
func Build(incident *domain.Incident, evidence []domain.Evidence, graph topology.IncidentGraph) domain.OperationalWorldModel {
	now := time.Now().UTC()
	model := domain.OperationalWorldModel{BuiltAt: now}
	if incident == nil {
		return model
	}
	model.IncidentID, model.Cluster, model.Namespace = incident.ID, incident.Cluster, incident.Namespace
	model.RootEntityID = entityID("service", incident.Namespace, incident.Service)
	entities := map[string]domain.OperationalEntity{}
	relations := map[string]domain.OperationalRelation{}
	addEntity := func(id, kind, service, resource string, evidenceID string, observedAt time.Time, attributes map[string]string) {
		if id == "" {
			return
		}
		current := entities[id]
		if current.ID == "" {
			current = domain.OperationalEntity{ID: id, Kind: canonicalKind(kind), Namespace: incident.Namespace, Service: service, Resource: resource, Attributes: map[string]string{}}
		}
		if current.Service == "" {
			current.Service = service
		}
		if current.Resource == "" {
			current.Resource = resource
		}
		if observedAt.After(current.ObservedAt) {
			current.ObservedAt = observedAt
		}
		if evidenceID != "" {
			current.EvidenceIDs = appendUnique(current.EvidenceIDs, evidenceID)
		}
		for key, value := range attributes {
			if strings.TrimSpace(value) != "" {
				current.Attributes[key] = value
			}
		}
		entities[id] = current
	}
	addRelation := func(from, to, kind, evidenceID string) {
		if from == "" || to == "" || from == to {
			return
		}
		key := from + "\x00" + to + "\x00" + kind
		current := relations[key]
		if current.From == "" {
			current = domain.OperationalRelation{From: from, To: to, Kind: kind}
		}
		if evidenceID != "" {
			current.EvidenceIDs = appendUnique(current.EvidenceIDs, evidenceID)
		}
		relations[key] = current
	}
	addEntity(model.RootEntityID, "service", incident.Service, incident.Resource, "", incident.CreatedAt, map[string]string{"role": "incident_root"})
	for _, node := range graph.Nodes {
		id := entityID(node.Type, incident.Namespace, node.ID)
		service, resource := node.Metadata["service"], node.Metadata["resource"]
		if service == "" && canonicalKind(node.Type) == "service" {
			service = node.ID
		}
		if resource == "" {
			resource = node.ID
		}
		addEntity(id, node.Type, service, resource, "", time.Time{}, node.Metadata)
	}
	for _, edge := range graph.Edges {
		fromKind, toKind := graphNodeKind(graph, edge.Source), graphNodeKind(graph, edge.Target)
		addRelation(entityID(fromKind, incident.Namespace, edge.Source), entityID(toKind, incident.Namespace, edge.Target), edge.Relation, "")
	}
	for _, item := range evidence {
		kind := evidenceEntityKind(item)
		name := firstNonEmpty(item.Resource, item.Service, incident.Resource, incident.Service)
		id := entityID(kind, incident.Namespace, name)
		observedAt := evidenceTime(item)
		addEntity(id, kind, firstNonEmpty(item.Service, incident.Service), name, item.ID, observedAt, nil)
		if model.RootEntityID != id {
			addRelation(model.RootEntityID, id, "observed_on", item.ID)
		}
		for _, signal := range item.Signals {
			strength := signal.Strength
			if strength <= 0 {
				strength = item.AnomalyScore
			}
			if strength <= 0 {
				strength = item.Confidence
			}
			reliability := signal.Reliability
			if reliability <= 0 {
				reliability = item.QualityScore
			}
			model.AbnormalSignals = append(model.AbnormalSignals, domain.OperationalSignal{ID: firstNonEmpty(signal.ID, item.ID+":"+signal.Signal), EntityID: id, Category: signal.Category, Signal: signal.Signal, Direction: signal.Direction, Value: signal.Value, Strength: clamp(strength), Reliability: clamp(reliability), TemporalAlignment: clamp(signal.TemporalAlignment), EvidenceID: item.ID, ObservedAt: firstTime(signal.ObservedAt, observedAt)})
			if isMetricSource(item.Source) || strings.Contains(strings.ToLower(signal.Category), "metric") {
				model.MetricSignatures = append(model.MetricSignatures, domain.MetricSignature{Name: signal.Signal, EntityID: id, Direction: signal.Direction, Value: signal.Value, Strength: clamp(strength), EvidenceID: item.ID, ObservedAt: firstTime(signal.ObservedAt, observedAt)})
			}
		}
		model.Timeline = append(model.Timeline, domain.OperationalEvent{ID: item.ID, EntityID: id, Kind: firstNonEmpty(item.Type, item.Kind, "observation"), Summary: item.Summary, EvidenceID: item.ID, OccurredAt: observedAt})
		current := entities[id]
		if state := observedState(item); state != "" {
			current.State = state
			entities[id] = current
		}
	}
	for _, entity := range entities {
		model.Entities = append(model.Entities, entity)
	}
	for _, relation := range relations {
		model.Relations = append(model.Relations, relation)
	}
	sort.Slice(model.Entities, func(i, j int) bool { return model.Entities[i].ID < model.Entities[j].ID })
	sort.Slice(model.Relations, func(i, j int) bool {
		return model.Relations[i].From+model.Relations[i].To+model.Relations[i].Kind < model.Relations[j].From+model.Relations[j].To+model.Relations[j].Kind
	})
	sort.Slice(model.AbnormalSignals, func(i, j int) bool { return model.AbnormalSignals[i].ID < model.AbnormalSignals[j].ID })
	sort.Slice(model.Timeline, func(i, j int) bool {
		if model.Timeline[i].OccurredAt.Equal(model.Timeline[j].OccurredAt) {
			return model.Timeline[i].ID < model.Timeline[j].ID
		}
		return model.Timeline[i].OccurredAt.Before(model.Timeline[j].OccurredAt)
	})
	sort.Slice(model.MetricSignatures, func(i, j int) bool {
		return model.MetricSignatures[i].Name+model.MetricSignatures[i].EvidenceID < model.MetricSignatures[j].Name+model.MetricSignatures[j].EvidenceID
	})
	model.EvidenceSnapshotHash = brainruntime.EvidenceSnapshotHash(evidence)
	return model
}

func entityID(kind, namespace, name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	return canonicalKind(kind) + "/" + namespace + "/" + name
}

func canonicalKind(kind string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	switch kind {
	case "deploy", "deployment":
		return "deployment"
	case "pod":
		return "pod"
	case "container":
		return "container"
	case "node":
		return "node"
	case "database", "cache", "queue":
		return kind
	default:
		return "service"
	}
}

func graphNodeKind(graph topology.IncidentGraph, id string) string {
	for _, node := range graph.Nodes {
		if node.ID == id {
			return node.Type
		}
	}
	return "service"
}

func evidenceEntityKind(item domain.Evidence) string {
	text := strings.ToLower(item.Type + " " + item.Kind + " " + item.Resource)
	for _, kind := range []string{"container", "deployment", "pod", "node", "database", "cache", "queue"} {
		if strings.Contains(text, kind) {
			return kind
		}
	}
	return "service"
}

func evidenceTime(item domain.Evidence) time.Time {
	return firstTime(item.Timestamp, item.ObservedAt, item.CollectedAt, item.WindowEnd)
}

func firstTime(values ...time.Time) time.Time {
	for _, value := range values {
		if !value.IsZero() {
			return value
		}
	}
	return time.Time{}
}

func observedState(item domain.Evidence) string {
	for _, payload := range []map[string]any{item.Facts, item.Content, item.Data} {
		for _, key := range []string{"state", "phase", "status", "reason"} {
			if value := strings.TrimSpace(fmt.Sprint(payload[key])); value != "" && value != "<nil>" {
				return value
			}
		}
	}
	return ""
}

func appendUnique(values []string, value string) []string {
	for _, item := range values {
		if item == value {
			return values
		}
	}
	return append(values, value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func clamp(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func isMetricSource(source string) bool {
	source = strings.ToLower(source)
	return source == "metric" || source == "prometheus"
}
