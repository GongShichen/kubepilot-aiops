package service

import (
	"context"
	"testing"
	"time"

	workflowgraph "github.com/kubepilot-aiops/kubepilot/graph"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/internal/store"
)

type blockingCorrelationFallback struct{}

func (blockingCorrelationFallback) Correlate(ctx context.Context, _ domain.Alert, _, _, _ string, _ []domain.Incident) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func TestWorkflowContextHasNoWallClockDeadline(t *testing.T) {
	ctx, cancel := newWorkflowContext()
	defer cancel()
	if deadline, ok := ctx.Deadline(); ok {
		t.Fatalf("workflow deadline must be unset, got %s", deadline)
	}
}

func TestApproveRejectsLegacyWorkflowWithoutEinoCheckpoint(t *testing.T) {
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
	manager := &IncidentManager{Store: st, Hub: NewHub()}
	if _, err := manager.Approve(ctx, incident.ID, incident.Proposal.ID, "approve", "test", "key-1"); err == nil {
		t.Fatal("expected approval without an Eino checkpoint to be rejected")
	}
	current, err := st.Get(ctx, incident.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != domain.StatusNeedsAttention {
		t.Fatalf("status=%s", current.Status)
	}
}

func TestIngestWithoutRuntimeLeavesIncidentReceived(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	manager := &IncidentManager{Store: st, Hub: NewHub()}
	incident, err := manager.IngestAlert(ctx, domain.Alert{Fingerprint: "fp-1", Status: "firing"}, "gateway-service", "production", "warning", "gateway-service", "service alert")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	current, err := st.Get(ctx, incident.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != domain.StatusReceived {
		t.Fatalf("incident unexpectedly entered an unavailable workflow: %s", current.Status)
	}
}

func TestCorrelationDoesNotMergeNamespaces(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	manager := &IncidentManager{Store: st, Hub: NewHub()}
	startsAt := time.Now().UTC()
	first, err := manager.IngestAlert(ctx, domain.Alert{Fingerprint: "fp-1", Status: "firing", StartsAt: startsAt}, "gateway-service", "namespace-a", "warning", "gateway-service", "first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.IngestAlert(ctx, domain.Alert{Fingerprint: "fp-2", Status: "firing", StartsAt: startsAt}, "gateway-service", "namespace-b", "warning", "gateway-service", "second")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatal("incidents from different namespaces were merged")
	}
}

func TestWorkflowStatusEventIsImmediatelyVisible(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	now := time.Now().UTC()
	incident := &domain.Incident{ID: "incident-status", Status: domain.StatusReceived, CreatedAt: now, UpdatedAt: now}
	if err := st.Create(ctx, incident); err != nil {
		t.Fatal(err)
	}
	manager := &IncidentManager{Store: st, Hub: NewHub()}
	transitionedAt := now.Add(time.Second)
	manager.ObserveWorkflowEvent(ctx, workflowgraph.WorkflowEvent{
		IncidentID: incident.ID,
		RunID:      "run-status",
		Type:       "status_transition",
		Name:       string(domain.StatusCollecting),
		Component:  "EinoGraph",
		OccurredAt: transitionedAt,
	})
	current, err := st.Get(ctx, incident.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != domain.StatusCollecting {
		t.Fatalf("status=%s", current.Status)
	}
	if !current.UpdatedAt.Equal(transitionedAt) {
		t.Fatalf("updated_at=%s", current.UpdatedAt)
	}
}

func TestCorrelationFallbackHasIndependentDeadline(t *testing.T) {
	manager := &IncidentManager{
		Store:                      store.NewMemoryStore(),
		Hub:                        NewHub(),
		CorrelationFallback:        blockingCorrelationFallback{},
		CorrelationFallbackTimeout: 20 * time.Millisecond,
	}
	started := time.Now()
	if incident := manager.correlate(context.Background(), domain.Alert{}, "gateway", "production", "gateway"); incident != nil {
		t.Fatal("unexpected correlation")
	}
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
		t.Fatalf("fallback exceeded deadline: %s", elapsed)
	}
}
