package worldmodel

import (
	"testing"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/internal/topology"
)

func TestBuildProjectsEntitiesSignalsTimelineAndMetricSignatures(t *testing.T) {
	now := time.Now().UTC()
	incident := &domain.Incident{ID: "incident-a", Cluster: "cluster-a", Namespace: "team-a", Service: "payment", Resource: "payment-deployment", CreatedAt: now.Add(-time.Minute)}
	evidence := []domain.Evidence{{ID: "metric-a", Source: "prometheus", Type: "container_metric", Namespace: "team-a", Service: "payment", Resource: "payment-pod", Timestamp: now, Summary: "memory pressure", Signals: []domain.EvidenceSignal{{ID: "signal-a", Category: "metric", Signal: "memory_pressure", Direction: "increasing", Value: .92, Strength: .9, Reliability: .95, TemporalAlignment: 1}}}}
	graph := topology.IncidentGraph{IncidentID: incident.ID, Nodes: []topology.GraphNode{{ID: "payment", Type: "service"}, {ID: "redis", Type: "cache"}}, Edges: []topology.GraphEdge{{Source: "payment", Target: "redis", Relation: "depends_on", Weight: 1}}}

	model := Build(incident, evidence, graph)
	if model.RootEntityID == "" || len(model.Entities) < 3 || len(model.Relations) < 2 {
		t.Fatalf("world model lost entities or dependencies: %+v", model)
	}
	if len(model.AbnormalSignals) != 1 || model.AbnormalSignals[0].EvidenceID != "metric-a" || len(model.MetricSignatures) != 1 {
		t.Fatalf("world model lost typed signal provenance: %+v", model)
	}
	if len(model.Timeline) != 1 || model.Timeline[0].OccurredAt.IsZero() || model.EvidenceSnapshotHash == "" {
		t.Fatalf("world model is not replayable: %+v", model)
	}
}

func TestBuildUsesResourceAsRootWhenServiceIsAbsent(t *testing.T) {
	incident := &domain.Incident{ID: "incident-resource", Namespace: "team-a", Resource: "worker-deployment", CreatedAt: time.Now().UTC()}
	model := Build(incident, nil, topology.IncidentGraph{})
	if model.RootEntityID != "service/team-a/worker-deployment" || len(model.Entities) != 1 || model.Entities[0].Resource != "worker-deployment" {
		t.Fatalf("resource-only Incident lost its World Model root: %+v", model)
	}
}

func TestBuildExpandsCanonicalKubernetesFactsIntoOperationalEntities(t *testing.T) {
	now := time.Now().UTC()
	incident := &domain.Incident{ID: "incident-kubernetes", Namespace: "team-a", Service: "payment", Resource: "payment", CreatedAt: now.Add(-time.Minute)}
	evidence := []domain.Evidence{{ID: "k8s-a", Source: "kubernetes", Kind: "workload_state", Namespace: "team-a", Service: "payment", Resource: "payment", ObservedAt: now, Data: map[string]any{
		"deployment":              map[string]any{"name": "payment", "uid": "deployment-uid", "resource_version": "12", "revision": "3", "unavailable_replicas": int32(1), "containers": []map[string]any{{"name": "api", "image": "payment:v3"}}},
		"pods":                    []map[string]any{{"name": "payment-abc", "uid": "pod-uid", "resource_version": "17", "phase": "Running", "node": "node-a", "containers": []map[string]any{{"name": "api", "state": "waiting", "reason": "CrashLoopBackOff"}}}},
		"discovered_dependencies": []string{"redis"},
		"events":                  []map[string]any{{"object": "payment-abc", "reason": "BackOff", "message": "back-off restarting failed container", "last_timestamp": now}},
	}}}
	graph := topology.IncidentGraph{Nodes: []topology.GraphNode{{ID: "orders-db", Type: "database"}}}
	model := Build(incident, evidence, graph)
	wantedKinds := map[string]bool{"service": false, "deployment": false, "pod": false, "container": false, "node": false, "database": false}
	for _, entity := range model.Entities {
		if _, ok := wantedKinds[entity.Kind]; ok {
			wantedKinds[entity.Kind] = true
		}
	}
	for kind, found := range wantedKinds {
		if !found {
			t.Fatalf("World Model did not materialize %s: %+v", kind, model.Entities)
		}
	}
	wantedRelations := map[string]bool{"implemented_by": false, "owns": false, "contains": false, "runs_on": false, "depends_on": false}
	for _, relation := range model.Relations {
		if _, ok := wantedRelations[relation.Kind]; ok {
			wantedRelations[relation.Kind] = true
		}
	}
	for relation, found := range wantedRelations {
		if !found {
			t.Fatalf("World Model did not materialize %s relation: %+v", relation, model.Relations)
		}
	}
	if len(model.Timeline) < 2 || model.Timeline[len(model.Timeline)-1].Kind != "BackOff" || model.Timeline[len(model.Timeline)-1].EntityID != "pod/team-a/payment-abc" {
		t.Fatalf("Kubernetes event timeline was not bound to the Pod: %+v", model.Timeline)
	}
}
