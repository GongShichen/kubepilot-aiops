package service

import (
	"context"
	"fmt"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/kubepilot-aiops/kubepilot/agent"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	captools "github.com/kubepilot-aiops/kubepilot/tools"
)

type EinoCorrelator struct {
	candidateTool tool.BaseTool
	agents        *agent.AgentRegistry
}

func NewEinoCorrelator(ctx context.Context, repo captools.IncidentQueryRepository, agents *agent.AgentRegistry) (*EinoCorrelator, error) {
	items, err := captools.NewIncidentQueryTools(repo)
	if err != nil {
		return nil, err
	}
	registry := captools.NewRegistry()
	for _, item := range items {
		if err = registry.Register(ctx, item, captools.Registration{Category: captools.CategoryIncident, AllowedNodes: []string{"alert_correlation"}, Timeout: 30 * time.Second, MaxArgumentBytes: 64 << 10, MaxOutputBytes: 2 << 20}); err != nil {
			return nil, err
		}
	}
	registered, err := registry.ToolsForNode("alert_correlation")
	if err != nil {
		return nil, err
	}
	var candidateTool tool.BaseTool
	for _, candidate := range registered {
		info, infoErr := candidate.Info(ctx)
		if infoErr != nil {
			return nil, infoErr
		}
		if info.Name == "find_incident_candidates" {
			if candidateTool != nil {
				return nil, fmt.Errorf("duplicate incident candidate capability")
			}
			candidateTool = candidate
		}
	}
	if candidateTool == nil {
		return nil, fmt.Errorf("incident candidate capability is not registered")
	}
	return &EinoCorrelator{candidateTool: candidateTool, agents: agents}, nil
}

func (c *EinoCorrelator) Correlate(ctx context.Context, alert domain.Alert, service, namespace, resource string, _ []domain.Incident) (string, error) {
	if c == nil || c.candidateTool == nil || c.agents == nil {
		return "", fmt.Errorf("Eino correlator is unavailable")
	}
	return c.agents.CorrelateWithCandidateTool(ctx, alert, service, namespace, resource, c.candidateTool)
}
