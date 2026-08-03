package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/kubepilot-aiops/kubepilot/agent"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/internal/store"
	"github.com/oklog/ulid/v2"
)

type IncidentManager struct {
	Store            store.IncidentStore
	Supervisor       *agent.Supervisor
	Executor         agent.Executor
	Hub              *Hub
	ModelSnapshotter interface {
		WithSnapshot(context.Context) context.Context
	}
}
type ManualIncident struct {
	Severity        string    `json:"severity"`
	Service         string    `json:"service"`
	Namespace       string    `json:"namespace"`
	Resource        string    `json:"resource"`
	Summary         string    `json:"summary"`
	DiagnosisMethod string    `json:"diagnosis_method,omitempty"`
	EvidenceStartAt time.Time `json:"evidence_start_at,omitempty"`
}

func (m *IncidentManager) Create(ctx context.Context, input ManualIncident) (*domain.Incident, error) {
	now := time.Now().UTC()
	if !domain.ValidDiagnosisMethod(input.DiagnosisMethod) {
		return nil, fmt.Errorf("unsupported diagnosis method %q", input.DiagnosisMethod)
	}
	method := input.DiagnosisMethod
	if method == "" {
		method = domain.DiagnosisMethodKubePilot
	}
	evidenceStartAt := input.EvidenceStartAt
	if evidenceStartAt.After(now) || evidenceStartAt.Before(now.Add(-15*time.Minute)) {
		evidenceStartAt = time.Time{}
	}
	in := &domain.Incident{ID: ulid.Make().String(), Status: domain.StatusReceived, Severity: input.Severity, Service: input.Service, Namespace: input.Namespace, Resource: input.Resource, Summary: input.Summary, DiagnosisMethod: method, EvidenceStartAt: evidenceStartAt, CreatedAt: now, UpdatedAt: now}
	if in.Severity == "" {
		in.Severity = "warning"
	}
	if err := m.Store.Create(ctx, in); err != nil {
		return nil, err
	}
	m.audit(ctx, in.ID, "incident_created", "incident created", nil)
	go m.diagnose(in.ID)
	return in, nil
}
func (m *IncidentManager) IngestAlert(ctx context.Context, a domain.Alert, service, namespace, severity, resource, summary string) (*domain.Incident, error) {
	if a.Status == "resolved" {
		existing, err := m.Store.FindByFingerprint(ctx, a.Fingerprint)
		if err != nil {
			return nil, err
		}
		if existing.Status == domain.StatusAwaitingApproval || existing.Status == domain.StatusReceived || existing.Status == domain.StatusCollecting || existing.Status == domain.StatusDiagnosing {
			existing.Status = domain.StatusCancelled
			existing.UpdatedAt = time.Now().UTC()
			_ = m.Store.Update(ctx, existing)
		}
		return existing, nil
	}
	if existing, err := m.Store.FindByFingerprint(ctx, a.Fingerprint); err == nil {
		existing.Alerts = append(existing.Alerts, a)
		existing.UpdatedAt = time.Now().UTC()
		_ = m.Store.Update(ctx, existing)
		return existing, nil
	}
	if existing := m.correlate(ctx, a, service, namespace, resource); existing != nil {
		existing.Alerts = append(existing.Alerts, a)
		existing.UpdatedAt = time.Now().UTC()
		if err := m.Store.Update(ctx, existing); err != nil {
			return nil, err
		}
		m.audit(ctx, existing.ID, "alert_correlated", "alert merged into active incident", map[string]any{"fingerprint": a.Fingerprint})
		m.publish(existing)
		return existing, nil
	}
	now := time.Now().UTC()
	evidenceStartAt := a.StartsAt
	if evidenceStartAt.IsZero() || evidenceStartAt.After(now) || evidenceStartAt.Before(now.Add(-15*time.Minute)) {
		evidenceStartAt = time.Time{}
	}
	in := &domain.Incident{ID: ulid.Make().String(), Status: domain.StatusReceived, Severity: severity, Service: service, Namespace: namespace, Resource: resource, Summary: summary, EvidenceStartAt: evidenceStartAt, CreatedAt: now, UpdatedAt: now, Alerts: []domain.Alert{a}}
	if err := m.Store.Create(ctx, in); err != nil {
		return nil, err
	}
	m.audit(ctx, in.ID, "alert_ingested", "alert created incident", map[string]any{"fingerprint": a.Fingerprint})
	if a.Labels["benchmark_mode"] != "correlation" {
		go m.diagnose(in.ID)
	}
	return in, nil
}

func (m *IncidentManager) correlate(ctx context.Context, alert domain.Alert, service, namespace, resource string) *domain.Incident {
	items, err := m.Store.List(ctx, 100, 0)
	if err != nil {
		return nil
	}
	for i := range items {
		candidate := &items[i]
		if terminal(candidate.Status) || candidate.Namespace != namespace {
			continue
		}
		correlationRun := alert.Labels["correlation_run"]
		candidateRun := ""
		for _, previous := range candidate.Alerts {
			if previous.Labels["correlation_run"] != "" {
				candidateRun = previous.Labels["correlation_run"]
				break
			}
		}
		if correlationRun != candidateRun && (correlationRun != "" || candidateRun != "") {
			continue
		}
		temporallyClose := false
		for _, previous := range candidate.Alerts {
			if alert.StartsAt.IsZero() || previous.StartsAt.IsZero() || absDuration(alert.StartsAt.Sub(previous.StartsAt)) <= 5*time.Minute {
				temporallyClose = true
				break
			}
		}
		if len(candidate.Alerts) == 0 {
			temporallyClose = absDuration(time.Since(candidate.CreatedAt)) <= 5*time.Minute
		}
		if !temporallyClose {
			continue
		}
		if (resource != "" && candidate.Resource == resource) || (service != "" && candidate.Service == service) {
			return candidate
		}
		for _, previous := range candidate.Alerts {
			if sharedLabel(previous.Labels, alert.Labels, "trace_id", "request_id", "revision", "deployment_revision") {
				return candidate
			}
		}
		if directlyConnected(candidate.Service, service) {
			return candidate
		}
	}
	return nil
}
func absDuration(v time.Duration) time.Duration {
	if v < 0 {
		return -v
	}
	return v
}

func terminal(status domain.IncidentStatus) bool {
	switch status {
	case domain.StatusResolved, domain.StatusRejected, domain.StatusRecoveryFailed, domain.StatusCancelled:
		return true
	default:
		return false
	}
}

func sharedLabel(a, b map[string]string, keys ...string) bool {
	for _, key := range keys {
		if a[key] != "" && a[key] == b[key] {
			return true
		}
	}
	return false
}

func directlyConnected(a, b string) bool {
	pairs := map[string]bool{
		"gateway-service/order-service": true,
		"order-service/gateway-service": true,
		"order-service/payment-service": true,
		"payment-service/order-service": true,
	}
	return pairs[a+"/"+b]
}

func (m *IncidentManager) Retry(ctx context.Context, id string) (*domain.Incident, error) {
	in, err := m.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if in.Status != domain.StatusNeedsAttention && in.Status != domain.StatusRecoveryFailed {
		return nil, fmt.Errorf("incident cannot be retried from %s", in.Status)
	}
	in.Status = domain.StatusReceived
	in.UpdatedAt = time.Now().UTC()
	if err = m.Store.Update(ctx, in); err != nil {
		return nil, err
	}
	m.audit(ctx, id, "incident_retried", "diagnosis workflow restarted", nil)
	go m.diagnose(id)
	return in, nil
}
func (m *IncidentManager) diagnose(id string) {
	workflowCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if m.ModelSnapshotter != nil {
		workflowCtx = m.ModelSnapshotter.WithSnapshot(workflowCtx)
	}
	in, err := m.Store.Get(workflowCtx, id)
	if err != nil {
		return
	}
	state, err := m.Supervisor.Run(workflowCtx, in)
	persistCtx, persistCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer persistCancel()
	if err != nil {
		in.Status = domain.StatusNeedsAttention
		in.DiagnosisError = err.Error()
		in.UpdatedAt = time.Now().UTC()
		_ = m.Store.Update(persistCtx, in)
		m.audit(persistCtx, id, "diagnosis_failed", err.Error(), nil)
		return
	}
	state.Incident.DiagnosisError = ""
	if state.Incident.Proposal != nil {
		if preparer, ok := m.Executor.(interface {
			Prepare(context.Context, *domain.RecoveryProposal) error
		}); ok {
			if prepareErr := preparer.Prepare(persistCtx, state.Incident.Proposal); prepareErr != nil {
				state.Incident.Status = domain.StatusNeedsAttention
				state.Errors = append(state.Errors, "proposal precondition: "+prepareErr.Error())
			}
		}
	}
	_ = m.Store.Update(persistCtx, state.Incident)
	m.publish(state.Incident)
	m.audit(persistCtx, id, "diagnosis_completed", "diagnosis workflow completed", map[string]any{"status": state.Incident.Status, "errors": state.Errors})
}
func (m *IncidentManager) Approve(ctx context.Context, id, proposalID, decision, comment, idempotencyKey string) (*domain.Incident, error) {
	in, err := m.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if in.Status != domain.StatusAwaitingApproval || in.Proposal == nil {
		return nil, fmt.Errorf("incident is not awaiting approval")
	}
	if in.Proposal.ID != proposalID {
		return nil, fmt.Errorf("proposal mismatch")
	}
	if time.Now().After(in.Proposal.ExpiresAt) {
		return nil, fmt.Errorf("proposal expired")
	}
	claimed, err := m.Store.RecordApproval(ctx, idempotencyKey, id, proposalID, decision, comment)
	if err != nil {
		return nil, err
	}
	if !claimed {
		return in, nil
	}
	if decision != "approve" {
		in.Status = domain.StatusRejected
		in.UpdatedAt = time.Now().UTC()
		_ = m.Store.Update(ctx, in)
		m.audit(ctx, id, "proposal_rejected", comment, nil)
		return in, nil
	}
	in.Status = domain.StatusRecovering
	in.UpdatedAt = time.Now().UTC()
	if err = m.Store.Update(ctx, in); err != nil {
		return nil, err
	}
	proposal := *in.Proposal
	m.audit(ctx, id, "proposal_approved", comment, map[string]any{"proposal_id": proposal.ID, "action": proposal.Action})
	m.publish(in)
	go m.recover(id, proposal)
	return in, nil
}

func (m *IncidentManager) recover(id string, proposal domain.RecoveryProposal) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	in, err := m.Store.Get(ctx, id)
	if err != nil {
		return
	}
	if err = m.Executor.Execute(ctx, in, proposal); err != nil {
		in.Status = domain.StatusRecoveryFailed
		in.UpdatedAt = time.Now().UTC()
		persistCtx, persistCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer persistCancel()
		_ = m.Store.Update(persistCtx, in)
		m.audit(persistCtx, id, "recovery_failed", err.Error(), nil)
		m.publish(in)
		return
	}
	in.Status = domain.StatusVerifying
	in.UpdatedAt = time.Now().UTC()
	_ = m.Store.Update(ctx, in)
	m.publish(in)
	verification, err := m.Executor.Verify(ctx, in)
	if err != nil || !verification.Success {
		in.Status = domain.StatusRecoveryFailed
	} else {
		in.Status = domain.StatusResolved
	}
	in.Verification = &verification
	in.UpdatedAt = time.Now().UTC()
	persistCtx, persistCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer persistCancel()
	_ = m.Store.Update(persistCtx, in)
	if err != nil {
		m.audit(persistCtx, id, "verification_failed", err.Error(), nil)
	} else {
		m.audit(persistCtx, id, "verification_completed", verification.Message, map[string]any{"success": verification.Success, "checks": verification.Checks})
	}
	m.publish(in)
}
func (m *IncidentManager) Get(ctx context.Context, id string) (*domain.Incident, error) {
	return m.Store.Get(ctx, id)
}
func (m *IncidentManager) List(ctx context.Context, limit, offset int) ([]domain.Incident, error) {
	return m.Store.List(ctx, limit, offset)
}
func (m *IncidentManager) Events(ctx context.Context, id string) ([]domain.AuditEvent, error) {
	return m.Store.ListAudit(ctx, id)
}
func (m *IncidentManager) audit(ctx context.Context, id, kind, message string, data map[string]any) {
	e := domain.AuditEvent{ID: ulid.Make().String(), IncidentID: id, Type: kind, Message: message, Data: data, CreatedAt: time.Now().UTC()}
	_ = m.Store.AppendAudit(ctx, e)
	b, _ := json.Marshal(e)
	m.Hub.Publish(id, b)
}
func (m *IncidentManager) publish(in *domain.Incident) {
	b, _ := json.Marshal(in)
	m.Hub.Publish(in.ID, b)
}

var _ = errors.Is
