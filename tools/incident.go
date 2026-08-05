package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

type IncidentQueryRepository interface {
	Get(context.Context, string) (*domain.Incident, error)
	List(context.Context, int, int) ([]domain.Incident, error)
}

type IncidentCandidateQuery struct {
	Namespace string `json:"namespace" jsonschema:"required"`
	Limit     int    `json:"limit,omitempty"`
}

type IncidentIDQuery struct {
	IncidentID string `json:"incident_id" jsonschema:"required"`
}

type RelatedIncidentQuery struct {
	Namespace string `json:"namespace" jsonschema:"required"`
	Service   string `json:"service" jsonschema:"required"`
	Limit     int    `json:"limit,omitempty"`
}

func NewIncidentQueryCapabilities(repo IncidentQueryRepository) ([]Capability, error) {
	if repo == nil {
		return nil, fmt.Errorf("incident repository is required")
	}
	registration := Registration{Category: CategoryIncident, AllowedNodes: []string{NodeAlertCorrelation}, Timeout: 30 * time.Second, MaxArgumentBytes: 64 << 10, MaxOutputBytes: 2 << 20}
	find, err := NewCapability("find_incident_candidates", "Find bounded active Incident candidates by namespace.", func(ctx context.Context, in IncidentCandidateQuery) ([]domain.Incident, error) {
		limit := in.Limit
		if limit <= 0 || limit > 100 {
			limit = 100
		}
		items, err := repo.List(ctx, limit, 0)
		if err != nil {
			return nil, err
		}
		out := make([]domain.Incident, 0, len(items))
		for _, item := range items {
			if item.Namespace != in.Namespace {
				continue
			}
			switch item.Status {
			case domain.StatusResolved, domain.StatusRejected, domain.StatusRecoveryFailed, domain.StatusCancelled:
				continue
			}
			item.Evidence, item.Hypotheses, item.ExecutionContext = nil, nil, nil
			out = append(out, item)
		}
		return out, nil
	}, registration)
	if err != nil {
		return nil, err
	}
	loadContext, err := NewCapability("load_incident_context", "Load one bounded Incident context without execution secrets.", func(ctx context.Context, in IncidentIDQuery) (domain.Incident, error) {
		item, err := repo.Get(ctx, in.IncidentID)
		if err != nil {
			return domain.Incident{}, err
		}
		item.Evidence, item.ExecutionContext = nil, nil
		return *item, nil
	}, registration)
	if err != nil {
		return nil, err
	}
	loadEvidence, err := NewCapability("load_incident_evidence", "Load normalized Evidence for one Incident.", func(ctx context.Context, in IncidentIDQuery) ([]domain.Evidence, error) {
		item, err := repo.Get(ctx, in.IncidentID)
		if err != nil {
			return nil, err
		}
		return item.Evidence, nil
	}, registration)
	if err != nil {
		return nil, err
	}
	related, err := NewCapability("load_related_incidents", "Load bounded related Incidents by namespace and service.", func(ctx context.Context, in RelatedIncidentQuery) ([]domain.Incident, error) {
		limit := in.Limit
		if limit <= 0 || limit > 20 {
			limit = 20
		}
		items, err := repo.List(ctx, 100, 0)
		if err != nil {
			return nil, err
		}
		out := make([]domain.Incident, 0, limit)
		for _, item := range items {
			if item.Namespace == in.Namespace && item.Service == in.Service {
				item.Evidence, item.ExecutionContext = nil, nil
				out = append(out, item)
				if len(out) == limit {
					break
				}
			}
		}
		return out, nil
	}, registration)
	if err != nil {
		return nil, err
	}
	return []Capability{find, loadContext, loadEvidence, related}, nil
}
