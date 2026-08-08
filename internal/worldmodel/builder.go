package worldmodel

import (
	"fmt"
	"reflect"
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
	rootName := firstNonEmpty(incident.Service, incident.Resource)
	model.RootEntityID = entityID("service", incident.Namespace, rootName)
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
				if key == "state" {
					current.State = value
				}
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
	addEntity(model.RootEntityID, "service", firstNonEmpty(incident.Service, rootName), firstNonEmpty(incident.Resource, rootName), "", incident.CreatedAt, map[string]string{"role": "incident_root"})
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
		expandOperationalFacts(incident, item, canonicalEvidenceFacts(item), model.RootEntityID, addEntity, addRelation, &model)
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

func canonicalEvidenceFacts(item domain.Evidence) map[string]any {
	for _, payload := range []map[string]any{item.Facts, item.Content, item.Data} {
		if len(payload) > 0 {
			return payload
		}
	}
	return nil
}

func expandOperationalFacts(
	incident *domain.Incident,
	evidence domain.Evidence,
	facts map[string]any,
	rootID string,
	addEntity func(string, string, string, string, string, time.Time, map[string]string),
	addRelation func(string, string, string, string),
	model *domain.OperationalWorldModel,
) {
	if len(facts) == 0 {
		return
	}
	observedAt := evidenceTime(evidence)
	serviceName := firstNonEmpty(evidence.Service, incident.Service, evidence.Resource, incident.Resource)
	serviceID := entityID("service", incident.Namespace, serviceName)
	objectEntities := map[string]string{serviceName: serviceID}
	if serviceID != "" {
		addEntity(serviceID, "service", serviceName, serviceName, evidence.ID, observedAt, nil)
	}
	deploymentID := ""
	if deployment, ok := stringMap(facts["deployment"]); ok {
		name := textValue(deployment["name"])
		if name != "" {
			deploymentID = entityID("deployment", incident.Namespace, name)
			objectEntities[name] = deploymentID
			state := "healthy"
			if numericValue(deployment["unavailable_replicas"]) > 0 {
				state = "degraded"
			}
			addEntity(deploymentID, "deployment", serviceName, name, evidence.ID, observedAt, map[string]string{"state": state, "revision": textValue(deployment["revision"]), "uid": textValue(deployment["uid"]), "resource_version": textValue(deployment["resource_version"])})
			addRelation(serviceID, deploymentID, "implemented_by", evidence.ID)
			for _, container := range mapSlice(deployment["containers"]) {
				containerName := textValue(container["name"])
				containerID := entityID("container", incident.Namespace, name+"/template/"+containerName)
				addEntity(containerID, "container", serviceName, containerName, evidence.ID, observedAt, map[string]string{"image": textValue(container["image"]), "role": "pod_template"})
				addRelation(deploymentID, containerID, "defines", evidence.ID)
			}
		}
	}
	for _, pod := range mapSlice(facts["pods"]) {
		name := textValue(pod["name"])
		if name == "" {
			continue
		}
		podID := entityID("pod", incident.Namespace, name)
		objectEntities[name] = podID
		addEntity(podID, "pod", serviceName, name, evidence.ID, observedAt, map[string]string{"state": textValue(pod["phase"]), "uid": textValue(pod["uid"]), "resource_version": textValue(pod["resource_version"]), "pod_ip": textValue(pod["pod_ip"])})
		if deploymentID != "" {
			addRelation(deploymentID, podID, "owns", evidence.ID)
		} else {
			addRelation(serviceID, podID, "selects", evidence.ID)
		}
		nodeName := textValue(pod["node"])
		if nodeName != "" {
			nodeID := entityID("node", incident.Namespace, nodeName)
			addEntity(nodeID, "node", "", nodeName, evidence.ID, observedAt, nil)
			addRelation(podID, nodeID, "runs_on", evidence.ID)
		}
		for _, container := range mapSlice(pod["containers"]) {
			containerName := textValue(container["name"])
			if containerName == "" {
				continue
			}
			containerID := entityID("container", incident.Namespace, name+"/"+containerName)
			addEntity(containerID, "container", serviceName, containerName, evidence.ID, observedAt, map[string]string{"state": firstNonEmpty(textValue(container["state"]), textValue(container["reason"])), "image": textValue(container["image"])})
			addRelation(podID, containerID, "contains", evidence.ID)
		}
	}
	for _, dependency := range stringSlice(facts["discovered_dependencies"]) {
		dependencyID := entityID("service", incident.Namespace, dependency)
		addEntity(dependencyID, "service", dependency, dependency, evidence.ID, observedAt, map[string]string{"role": "one_hop_dependency"})
		addRelation(serviceID, dependencyID, "depends_on", evidence.ID)
	}
	for index, event := range mapSlice(facts["events"]) {
		object := textValue(event["object"])
		entity := objectEntities[object]
		if entity == "" {
			entity = rootID
		}
		occurredAt := timeValue(event["last_timestamp"])
		if occurredAt.IsZero() {
			occurredAt = observedAt
		}
		model.Timeline = append(model.Timeline, domain.OperationalEvent{ID: fmt.Sprintf("%s:event:%d", evidence.ID, index), EntityID: entity, Kind: firstNonEmpty(textValue(event["reason"]), textValue(event["type"]), "kubernetes_event"), Summary: firstNonEmpty(textValue(event["message"]), textValue(event["reason"])), EvidenceID: evidence.ID, OccurredAt: occurredAt})
	}
}

func stringMap(value any) (map[string]any, bool) {
	if value == nil {
		return nil, false
	}
	if direct, ok := value.(map[string]any); ok {
		return direct, true
	}
	ref := reflect.ValueOf(value)
	if ref.Kind() != reflect.Map || ref.Type().Key().Kind() != reflect.String {
		return nil, false
	}
	out := make(map[string]any, ref.Len())
	iter := ref.MapRange()
	for iter.Next() {
		out[iter.Key().String()] = iter.Value().Interface()
	}
	return out, true
}

func mapSlice(value any) []map[string]any {
	ref := reflect.ValueOf(value)
	if !ref.IsValid() || (ref.Kind() != reflect.Slice && ref.Kind() != reflect.Array) {
		return nil
	}
	out := make([]map[string]any, 0, ref.Len())
	for index := 0; index < ref.Len(); index++ {
		if item, ok := stringMap(ref.Index(index).Interface()); ok {
			out = append(out, item)
		}
	}
	return out
}

func stringSlice(value any) []string {
	ref := reflect.ValueOf(value)
	if !ref.IsValid() || (ref.Kind() != reflect.Slice && ref.Kind() != reflect.Array) {
		return nil
	}
	out := []string{}
	for index := 0; index < ref.Len(); index++ {
		if item := textValue(ref.Index(index).Interface()); item != "" {
			out = appendUnique(out, item)
		}
	}
	return out
}

func textValue(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func numericValue(value any) float64 {
	switch typed := value.(type) {
	case int:
		return float64(typed)
	case int32:
		return float64(typed)
	case int64:
		return float64(typed)
	case float32:
		return float64(typed)
	case float64:
		return typed
	default:
		return 0
	}
}

func timeValue(value any) time.Time {
	switch typed := value.(type) {
	case time.Time:
		return typed
	case string:
		parsed, _ := time.Parse(time.RFC3339Nano, typed)
		return parsed
	default:
		ref := reflect.ValueOf(value)
		if ref.IsValid() && ref.Kind() == reflect.Struct {
			field := ref.FieldByName("Time")
			if field.IsValid() && field.CanInterface() {
				if observed, ok := field.Interface().(time.Time); ok {
					return observed
				}
			}
		}
		return time.Time{}
	}
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
