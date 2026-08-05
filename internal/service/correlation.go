package service

import (
	"context"
	"fmt"

	"github.com/kubepilot-aiops/kubepilot/agent"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	captools "github.com/kubepilot-aiops/kubepilot/tools"
)

type EinoCorrelator struct {
	candidateCapability captools.Capability
	agents              *agent.AgentRegistry
}

func NewEinoCorrelator(ctx context.Context, repo captools.IncidentQueryRepository, agents *agent.AgentRegistry) (*EinoCorrelator, error) {
	items, err := captools.NewIncidentQueryCapabilities(repo)
	if err != nil {
		return nil, err
	}
	var candidateCapability captools.Capability
	for _, candidate := range items {
		info, infoErr := candidate.EinoTool().Info(ctx)
		if infoErr != nil {
			return nil, infoErr
		}
		if info.Name == "find_incident_candidates" {
			if candidateCapability != nil {
				return nil, fmt.Errorf("duplicate incident candidate capability")
			}
			candidateCapability = candidate
		}
	}
	if candidateCapability == nil {
		return nil, fmt.Errorf("incident candidate capability is not registered")
	}
	return &EinoCorrelator{candidateCapability: candidateCapability, agents: agents}, nil
}

func (c *EinoCorrelator) Correlate(ctx context.Context, alert domain.Alert, service, namespace, resource string, _ []domain.Incident) (string, error) {
	if c == nil || c.candidateCapability == nil || c.agents == nil {
		return "", fmt.Errorf("Eino correlator is unavailable")
	}
	return c.agents.CorrelateWithCandidateCapability(ctx, alert, service, namespace, resource, c.candidateCapability)
}
