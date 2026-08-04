package extractor

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/internal/topology"
	knowledge "github.com/kubepilot-aiops/kubepilot/internal/topology/knowledge"
)

// Extract produces a reusable topology observation from a resolved Incident.
// Evaluation and benchmark incidents are intentionally rejected before they
// can reach a knowledge store.
func Extract(in *domain.Incident, graph topology.IncidentGraph) (knowledge.ServiceTopologyPattern, error) {
	if in == nil {
		return knowledge.ServiceTopologyPattern{}, fmt.Errorf("incident is required")
	}
	if in.Status != domain.StatusResolved {
		return knowledge.ServiceTopologyPattern{}, fmt.Errorf("only resolved incidents can produce topology knowledge")
	}
	if excluded(in) {
		return knowledge.ServiceTopologyPattern{}, fmt.Errorf("evaluation incidents cannot produce topology knowledge")
	}
	pattern := knowledge.Normalize(graph)
	if pattern.PatternID == "" || len(pattern.Edges) == 0 {
		return knowledge.ServiceTopologyPattern{}, fmt.Errorf("incident has no reusable dependency edges")
	}
	pattern.Frequency = 1
	pattern.SourceIncidents = []string{in.ID}
	pattern.LastObserved = observedAt(in)
	return pattern, nil
}

func Merge(ctx context.Context, in *domain.Incident, graph topology.IncidentGraph, store knowledge.PatternStore) (knowledge.ServiceTopologyPattern, error) {
	if store == nil {
		return knowledge.ServiceTopologyPattern{}, fmt.Errorf("topology knowledge store is required")
	}
	pattern, err := Extract(in, graph)
	if err != nil {
		return knowledge.ServiceTopologyPattern{}, err
	}
	return store.Merge(ctx, pattern)
}

func excluded(in *domain.Incident) bool {
	if strings.EqualFold(in.Namespace, "kubepilot-benchmark") {
		return true
	}
	for _, alert := range in.Alerts {
		for _, key := range []string{"evaluation", "benchmark", "kubepilot.io/evaluation"} {
			if strings.EqualFold(alert.Labels[key], "true") || alert.Labels[key] == "1" {
				return true
			}
		}
	}
	return false
}

func observedAt(in *domain.Incident) time.Time {
	if !in.UpdatedAt.IsZero() {
		return in.UpdatedAt
	}
	if !in.CreatedAt.IsZero() {
		return in.CreatedAt
	}
	return time.Now().UTC()
}
