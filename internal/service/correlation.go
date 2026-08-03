package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/kubepilot-aiops/kubepilot/agent"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	captools "github.com/kubepilot-aiops/kubepilot/tools"
	"github.com/oklog/ulid/v2"
)

type EinoCorrelator struct {
	node   *compose.ToolsNode
	agents *agent.AgentRegistry
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
	node, err := compose.NewToolNode(ctx, &compose.ToolsNodeConfig{Tools: registered, ExecuteSequentially: true})
	if err != nil {
		return nil, err
	}
	return &EinoCorrelator{node: node, agents: agents}, nil
}

func (c *EinoCorrelator) Correlate(ctx context.Context, alert domain.Alert, service, namespace, resource string, _ []domain.Incident) (string, error) {
	if c == nil || c.node == nil || c.agents == nil {
		return "", fmt.Errorf("Eino correlator is unavailable")
	}
	arguments, _ := json.Marshal(captools.IncidentCandidateQuery{Namespace: namespace, Limit: 100})
	results, err := c.node.Invoke(ctx, &schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{{
			ID: "correlation-" + ulid.Make().String(), Type: "function",
			Function: schema.FunctionCall{Name: "find_incident_candidates", Arguments: string(arguments)},
		}},
	})
	if err != nil {
		return "", err
	}
	if len(results) != 1 {
		return "", fmt.Errorf("candidate tool returned %d results", len(results))
	}
	var candidates []domain.Incident
	if err = json.Unmarshal([]byte(results[0].Content), &candidates); err != nil {
		return "", err
	}
	return c.agents.Correlate(ctx, alert, service, namespace, resource, candidates)
}
