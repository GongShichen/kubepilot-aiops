package service

import (
	"context"
	"testing"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/internal/store"
)

type asyncTestExecutor struct{}

func (asyncTestExecutor) Execute(context.Context, *domain.Incident, domain.RecoveryProposal) error {
	time.Sleep(50 * time.Millisecond)
	return nil
}

func (asyncTestExecutor) Verify(context.Context, *domain.Incident) (domain.Verification, error) {
	time.Sleep(50 * time.Millisecond)
	return domain.Verification{Success: true, Checks: map[string]bool{"ready": true}, CompletedAt: time.Now().UTC()}, nil
}

func TestApproveReturnsBeforeRecoveryCompletes(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	incident := &domain.Incident{
		ID: "incident-1", Status: domain.StatusAwaitingApproval, Namespace: "kubepilot-benchmark", Resource: "gateway-service",
		Proposal:  &domain.RecoveryProposal{ID: "proposal-1", Action: domain.ActionRestartPod, Target: "gateway-service", ExpiresAt: time.Now().Add(time.Minute)},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := st.Create(ctx, incident); err != nil {
		t.Fatal(err)
	}
	manager := &IncidentManager{Store: st, Executor: asyncTestExecutor{}, Hub: NewHub()}
	started := time.Now()
	approved, err := manager.Approve(ctx, incident.ID, incident.Proposal.ID, "approve", "test", "key-1")
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed >= 50*time.Millisecond {
		t.Fatalf("approval blocked for recovery: %s", elapsed)
	}
	if approved.Status != domain.StatusRecovering {
		t.Fatalf("status=%s", approved.Status)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		current, getErr := st.Get(ctx, incident.ID)
		if getErr == nil && current.Status == domain.StatusResolved {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("recovery did not finish asynchronously")
}

func TestCorrelationBenchmarkDoesNotLaunchDiagnosis(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	manager := &IncidentManager{Store: st, Hub: NewHub()}
	incident, err := manager.IngestAlert(ctx, domain.Alert{Fingerprint: "fp-1", Status: "firing", Labels: map[string]string{"benchmark_mode": "correlation"}}, "gateway-service", "kubepilot-benchmark", "warning", "gateway-service", "correlation benchmark")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	current, err := st.Get(ctx, incident.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != domain.StatusReceived {
		t.Fatalf("correlation-only incident unexpectedly entered workflow: %s", current.Status)
	}
}

func TestCorrelationRunsAreIsolated(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	manager := &IncidentManager{Store: st, Hub: NewHub()}
	startsAt := time.Now().UTC()
	first, err := manager.IngestAlert(ctx, domain.Alert{Fingerprint: "fp-1", Status: "firing", StartsAt: startsAt, Labels: map[string]string{"benchmark_mode": "correlation", "correlation_run": "run-1"}}, "gateway-service", "kubepilot-benchmark", "warning", "gateway-service", "first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.IngestAlert(ctx, domain.Alert{Fingerprint: "fp-2", Status: "firing", StartsAt: startsAt, Labels: map[string]string{"benchmark_mode": "correlation", "correlation_run": "run-2"}}, "gateway-service", "kubepilot-benchmark", "warning", "gateway-service", "second")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatal("different correlation runs were merged")
	}
}
