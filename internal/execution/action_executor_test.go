package execution

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

type mutationBackend struct {
	calls atomic.Int32
	err   error
}

func (b *mutationBackend) Execute(_ context.Context, _ *domain.Incident, _ domain.RecoveryProposal) error {
	b.calls.Add(1)
	return b.err
}

func TestActionExecutorPreservesUnknownResultSentinel(t *testing.T) {
	sentinel := errors.New("result unknown")
	backend := &mutationBackend{err: sentinel}
	executor, err := NewActionExecutor(context.Background(), backend, func(*domain.Incident, domain.RecoveryProposal) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	proposal := domain.RecoveryProposal{ID: "proposal-1", Action: domain.ActionRestartPod}
	err = executor.Execute(context.Background(), &domain.Incident{}, proposal)
	if !errors.Is(err, sentinel) {
		t.Fatalf("capability registry lost action error identity: %v", err)
	}
}

func TestActionExecutorUsesOnlyApprovedProposalCapability(t *testing.T) {
	backend := &mutationBackend{}
	executor, err := NewActionExecutor(context.Background(), backend, func(incident *domain.Incident, proposal domain.RecoveryProposal) error {
		if incident.ExecutionContext == nil || incident.ExecutionContext.ProposalID != proposal.ID {
			return fmt.Errorf("approval mismatch")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal := domain.RecoveryProposal{ID: "proposal-1", Action: domain.ActionRestartPod, Target: "gateway-service"}
	incident := &domain.Incident{ExecutionContext: &domain.ExecutionContext{ProposalID: "other"}}
	if err = executor.Execute(context.Background(), incident, proposal); err == nil || backend.calls.Load() != 0 {
		t.Fatalf("unapproved action executed: err=%v calls=%d", err, backend.calls.Load())
	}
	incident.ExecutionContext.ProposalID = proposal.ID
	if err = executor.Execute(context.Background(), incident, proposal); err != nil || backend.calls.Load() != 1 {
		t.Fatalf("approved action was not executed exactly once: err=%v calls=%d", err, backend.calls.Load())
	}
}

func TestActionExecutorRejectsUnregisteredAction(t *testing.T) {
	backend := &mutationBackend{}
	executor, err := NewActionExecutor(context.Background(), backend, func(*domain.Incident, domain.RecoveryProposal) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	err = executor.Execute(context.Background(), &domain.Incident{}, domain.RecoveryProposal{Action: domain.RecoveryAction("delete_deployment")})
	if err == nil || backend.calls.Load() != 0 {
		t.Fatalf("unregistered mutation executed: err=%v calls=%d", err, backend.calls.Load())
	}
}
