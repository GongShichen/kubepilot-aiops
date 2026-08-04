package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	captools "github.com/kubepilot-aiops/kubepilot/tools"
)

const ActionExecutorNode = "deterministic_action_executor"

type MutationBackend interface {
	Execute(context.Context, *domain.Incident, domain.RecoveryProposal) error
}

type ActionValidator func(*domain.Incident, domain.RecoveryProposal) error

type actionInput struct {
	Action     domain.RecoveryAction `json:"action" jsonschema:"required"`
	ProposalID string                `json:"proposal_id" jsonschema:"required"`
}

type approvedActionContextKey struct{}
type approvedActionContext struct {
	incident *domain.Incident
	proposal domain.RecoveryProposal
}

type actionOutput struct {
	Executed bool   `json:"executed"`
	Action   string `json:"action"`
	Target   string `json:"target"`
}

// ActionExecutor selects exactly one server-registered Eino capability from
// the approved proposal. No ChatModel and no model-produced ToolCall exists on
// this path.
type ActionExecutor struct {
	capabilities map[domain.RecoveryAction]tool.InvokableTool
}

func NewActionExecutor(ctx context.Context, backend MutationBackend, validate ActionValidator) (*ActionExecutor, error) {
	if backend == nil || validate == nil {
		return nil, fmt.Errorf("mutation backend and action validator are required")
	}
	registry := captools.NewRegistry()
	for action, name := range map[domain.RecoveryAction]string{
		domain.ActionRestartPod:         "restart_workload",
		domain.ActionScaleDeployment:    "scale_deployment",
		domain.ActionRollbackDeployment: "rollback_deployment",
	} {
		action, name := action, name
		candidate, err := toolutils.InferTool(name, "Execute exactly one server-approved Kubernetes mutation.", func(callCtx context.Context, in actionInput) (actionOutput, error) {
			approved, ok := callCtx.Value(approvedActionContextKey{}).(approvedActionContext)
			if !ok || approved.incident == nil || in.Action != action || approved.proposal.Action != action || in.ProposalID != approved.proposal.ID {
				return actionOutput{}, fmt.Errorf("approved proposal does not match selected action capability")
			}
			if err := validate(approved.incident, approved.proposal); err != nil {
				return actionOutput{}, err
			}
			if err := backend.Execute(callCtx, approved.incident, approved.proposal); err != nil {
				return actionOutput{}, err
			}
			return actionOutput{Executed: true, Action: string(action), Target: approved.proposal.Target}, nil
		})
		if err != nil {
			return nil, err
		}
		if err = registry.Register(ctx, candidate, captools.Registration{Category: captools.CategoryAction, AllowedNodes: []string{ActionExecutorNode}, Timeout: 30 * time.Second, MaxArgumentBytes: 128 << 10, MaxOutputBytes: 8 << 10, ApprovalMiddleware: true}); err != nil {
			return nil, err
		}
	}
	registered, err := registry.ToolsForNode(ActionExecutorNode)
	if err != nil {
		return nil, err
	}
	result := &ActionExecutor{capabilities: map[domain.RecoveryAction]tool.InvokableTool{}}
	for _, candidate := range registered {
		info, infoErr := candidate.Info(ctx)
		if infoErr != nil {
			return nil, infoErr
		}
		invokable, ok := candidate.(tool.InvokableTool)
		if !ok {
			return nil, fmt.Errorf("action capability %s is not invokable", info.Name)
		}
		switch info.Name {
		case "restart_workload":
			result.capabilities[domain.ActionRestartPod] = invokable
		case "scale_deployment":
			result.capabilities[domain.ActionScaleDeployment] = invokable
		case "rollback_deployment":
			result.capabilities[domain.ActionRollbackDeployment] = invokable
		}
	}
	return result, nil
}

func (e *ActionExecutor) Execute(ctx context.Context, incident *domain.Incident, proposal domain.RecoveryProposal) error {
	if e == nil || incident == nil {
		return fmt.Errorf("deterministic action executor is unavailable")
	}
	capability := e.capabilities[proposal.Action]
	if capability == nil {
		return fmt.Errorf("approved recovery action is not registered")
	}
	payload, err := json.Marshal(actionInput{Action: proposal.Action, ProposalID: proposal.ID})
	if err != nil {
		return err
	}
	ctx = context.WithValue(ctx, approvedActionContextKey{}, approvedActionContext{incident: incident, proposal: proposal})
	_, err = capability.InvokableRun(ctx, string(payload))
	return err
}
