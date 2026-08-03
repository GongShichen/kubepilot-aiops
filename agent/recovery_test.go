package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func TestRecoveryRejectsAnotherNamespace(t *testing.T) {
	if _, err := canonicalProposalTarget("other/gateway-service", "kubepilot-benchmark", "gateway-service"); err == nil {
		t.Fatal("expected cross-namespace target to be rejected")
	}
}

type recoveryTestClient struct{ restarts int }

func (c *recoveryTestClient) RestartDeployment(context.Context, string, string) error {
	c.restarts++
	return nil
}
func (*recoveryTestClient) ScaleDeployment(context.Context, string, string, int32) error {
	return nil
}
func (*recoveryTestClient) RollbackDeployment(context.Context, string, string) error { return nil }
func (*recoveryTestClient) TargetState(context.Context, string, string) (string, string, bool, int32, error) {
	return "uid-1", "rv-1", true, 0, nil
}

type recoveryTestGuard struct{ claimed bool }

func (g *recoveryTestGuard) ClaimAction(context.Context, string, time.Duration) (bool, error) {
	if g.claimed {
		return false, nil
	}
	g.claimed = true
	return true, nil
}
func (*recoveryTestGuard) CompleteAction(context.Context, string, string, time.Duration) error {
	return nil
}

func TestKubernetesActionCannotBeReplayed(t *testing.T) {
	client := &recoveryTestClient{}
	executor := KubernetesExecutor{Client: client, Guard: &recoveryTestGuard{}}
	incident := &domain.Incident{
		ID: "incident-1",
		ExecutionContext: &domain.ExecutionContext{
			IdempotencyKey: "approval-key-1",
		},
	}
	proposal := domain.RecoveryProposal{
		Action:          domain.ActionRestartPod,
		Namespace:       "kubepilot-demo",
		Target:          "gateway-service",
		TargetUID:       "uid-1",
		ResourceVersion: "rv-1",
		Parameters:      map[string]any{},
	}
	if err := executor.Execute(context.Background(), incident, proposal); err != nil {
		t.Fatal(err)
	}
	if err := executor.Execute(context.Background(), incident, proposal); !errors.Is(err, ErrActionResultUnknown) {
		t.Fatalf("expected unknown-result replay guard, got %v", err)
	}
	if client.restarts != 1 {
		t.Fatalf("recovery mutation executed %d times", client.restarts)
	}
}
