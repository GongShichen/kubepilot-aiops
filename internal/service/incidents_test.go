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

func TestRetryCreatesNewWorkflowAttemptAndInvalidatesFrozenArtifacts(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	now := time.Now().UTC()
	snapshot := domain.ExecutionSnapshot{SkillSnapshotHash: "old-skills", ModelConfigHash: "old-model", ToolSchemaHash: "old-tools", PolicyHash: "old-policy"}
	incident := &domain.Incident{
		ID: "incident-migrate", DiagnosisMethod: domain.DiagnosisMethodKubePilot, Status: domain.StatusNeedsAttention, Namespace: "production", CreatedAt: now, UpdatedAt: now,
		ExecutionSnapshot: &snapshot,
		WorkflowAttempt:   &domain.WorkflowAttempt{ID: "attempt:old", IncidentID: "incident-migrate", Sequence: 2, Status: domain.WorkflowAttemptCompleted, ExecutionSnapshot: snapshot, StartedAt: now},
		Investigation:     &domain.Investigation{Architecture: "eino-native-self-reflective-brain", AgentDiagnosis: &domain.AgentDiagnosis{ID: "diagnosis:old"}, AgentRecoveryPlan: &domain.AgentRecoveryPlan{ID: "plan:old"}},
		Evidence:          []domain.Evidence{{ID: "e1"}}, Proposal: &domain.RecoveryProposal{ID: "proposal:old"}, DryRun: &domain.DryRunResult{MutationSpecHash: "mutation:old"},
	}
	if err := st.Create(ctx, incident); err != nil {
		t.Fatal(err)
	}
	manager := &IncidentManager{Store: st, Hub: NewHub()}
	migrated, err := manager.Retry(ctx, incident.ID)
	if err != nil {
		t.Fatal(err)
	}
	if migrated.WorkflowAttempt == nil || migrated.WorkflowAttempt.Sequence != 3 || migrated.WorkflowAttempt.MigratedFromAttemptID != "attempt:old" || migrated.WorkflowAttempt.Status != domain.WorkflowAttemptActive {
		t.Fatalf("new Workflow Attempt not created: %+v", migrated.WorkflowAttempt)
	}
	if migrated.ExecutionSnapshot != nil || migrated.Investigation != nil || len(migrated.Evidence) != 0 || migrated.Proposal != nil || migrated.DryRun != nil {
		t.Fatalf("frozen artifacts survived explicit migration: %+v", migrated)
	}
}

func TestBaselineRetryDoesNotAdoptBrainWorkflowAttemptSemantics(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	now := time.Now().UTC()
	investigation := &domain.Investigation{Architecture: "eino-evidence-diagnosis-runtime", StartedAt: now}
	incident := &domain.Incident{ID: "baseline-retry", DiagnosisMethod: domain.DiagnosisMethodEvidence, Status: domain.StatusNeedsAttention, Investigation: investigation, CreatedAt: now, UpdatedAt: now}
	if err := st.Create(ctx, incident); err != nil {
		t.Fatal(err)
	}
	manager := &IncidentManager{Store: st, Hub: NewHub()}
	retried, err := manager.Retry(ctx, incident.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retried.WorkflowAttempt != nil || retried.ExecutionSnapshot != nil {
		t.Fatalf("baseline retry adopted Brain attempt state: attempt=%+v snapshot=%+v", retried.WorkflowAttempt, retried.ExecutionSnapshot)
	}
	if retried.Investigation == nil || retried.Investigation.Architecture != investigation.Architecture {
		t.Fatalf("baseline investigation behavior changed: %+v", retried.Investigation)
	}
}

func TestWorkflowAttemptMigrationRejectsBaseline(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	now := time.Now().UTC()
	incident := &domain.Incident{ID: "baseline-migration", DiagnosisMethod: domain.DiagnosisMethodEvidence, Status: domain.StatusNeedsAttention, CreatedAt: now, UpdatedAt: now}
	if err := st.Create(ctx, incident); err != nil {
		t.Fatal(err)
	}
	manager := &IncidentManager{Store: st, Hub: NewHub()}
	if _, err := manager.MigrateWorkflowAttempt(ctx, incident.ID); err == nil {
		t.Fatal("baseline accepted KubePilot Brain Workflow Attempt migration")
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

func TestResolvedAlertDoesNotCancelActiveInvestigation(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	manager := &IncidentManager{Store: st, Hub: NewHub()}
	firing := domain.Alert{Fingerprint: "fp-active", Status: "firing", StartsAt: time.Now().UTC().Add(-time.Minute)}
	incident, err := manager.IngestAlert(ctx, firing, "gateway-service", "production", "critical", "gateway-service", "service alert")
	if err != nil {
		t.Fatal(err)
	}
	if err = st.UpdateWorkflowStatus(ctx, incident.ID, domain.StatusDiagnosing, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	resolved := firing
	resolved.Status = "resolved"
	resolved.EndsAt = time.Now().UTC()
	current, err := manager.IngestAlert(ctx, resolved, "gateway-service", "production", "critical", "gateway-service", "service alert")
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != domain.StatusDiagnosing {
		t.Fatalf("resolved alert cancelled active investigation: %s", current.Status)
	}
	if len(current.Alerts) != 2 || current.Alerts[len(current.Alerts)-1].Status != "resolved" {
		t.Fatalf("resolved alert was not preserved as an observation: %#v", current.Alerts)
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

func TestAppendAlertRetainsConcurrentDiagnosisState(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	now := time.Now().UTC()
	persisted := &domain.Incident{
		ID: "incident-append-alert", Status: domain.StatusDiagnosing, Namespace: "production", Service: "gateway-service",
		CreatedAt: now, UpdatedAt: now,
		Investigation: &domain.Investigation{
			Architecture: "eino-evidence-diagnosis-runtime",
		},
	}
	if err := st.Create(ctx, persisted); err != nil {
		t.Fatal(err)
	}
	// Correlation can retain a snapshot read before the graph persisted its
	// investigation. AppendAlert must merge only the alert into the current
	// stored record rather than write this stale snapshot back wholesale.
	stale := *persisted
	stale.Investigation = nil
	manager := &IncidentManager{Store: st, Hub: NewHub()}
	if _, err := manager.appendAlert(ctx, &stale, domain.Alert{Fingerprint: "fp-alert", Status: "firing"}); err != nil {
		t.Fatal(err)
	}
	current, err := st.Get(ctx, persisted.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Investigation == nil || current.Investigation.Architecture != "eino-evidence-diagnosis-runtime" {
		t.Fatalf("diagnosis state was overwritten: %#v", current.Investigation)
	}
	if len(current.Alerts) != 1 || current.Alerts[0].Fingerprint != "fp-alert" {
		t.Fatalf("alerts=%#v", current.Alerts)
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
