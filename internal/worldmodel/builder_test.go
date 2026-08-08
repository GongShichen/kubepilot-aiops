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
