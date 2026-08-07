package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/compose"
	"github.com/kubepilot-aiops/kubepilot/agent"
	workflowgraph "github.com/kubepilot-aiops/kubepilot/graph"
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
	Checkpoints interface {
		Get(context.Context, string) ([]byte, bool, error)
		Delete(context.Context, string) error
	}
	AllowedNamespaces   []string
	CorrelationFallback interface {
		Correlate(context.Context, domain.Alert, string, string, string, []domain.Incident) (string, error)
	}
	CorrelationFallbackTimeout time.Duration
	Learner                    interface {
		Learn(context.Context, *domain.Incident) error
	}
}

func (m *IncidentManager) ObserveWorkflowEvent(ctx context.Context, event workflowgraph.WorkflowEvent) {
	if event.Type == "status_transition" {
		if status, ok := workflowEventStatus(event.Name); ok {
			if updater, supported := m.Store.(store.WorkflowStatusStore); supported {
				_ = updater.UpdateWorkflowStatus(ctx, event.IncidentID, status, event.OccurredAt)
			}
		}
	}
	if recorder, ok := m.Store.(interface {
		RecordWorkflowEvent(context.Context, workflowgraph.WorkflowEvent) error
	}); ok {
		_ = recorder.RecordWorkflowEvent(ctx, event)
	}
	data := map[string]any{"run_id": event.RunID, "name": event.Name, "component": event.Component}
	if event.InputTokens > 0 || event.OutputTokens > 0 {
		data["input_tokens"], data["output_tokens"] = event.InputTokens, event.OutputTokens
	}
	if event.TimeToFirstChunkMS > 0 {
		data["time_to_first_chunk_ms"] = event.TimeToFirstChunkMS
	}
	if event.TimeToToolCallMS > 0 {
		data["time_to_tool_call_ms"] = event.TimeToToolCallMS
	}
	if event.StreamDurationMS > 0 {
		data["stream_duration_ms"] = event.StreamDurationMS
	}
	if event.Error != "" {
		data["error"] = event.Error
	}
	m.audit(ctx, event.IncidentID, event.Type, event.Name, data)
}

func workflowEventStatus(value string) (domain.IncidentStatus, bool) {
	status := domain.IncidentStatus(value)
	switch status {
	case domain.StatusReceived, domain.StatusCorrelating, domain.StatusCollecting, domain.StatusDiagnosing,
		domain.StatusProposing, domain.StatusAwaitingApproval, domain.StatusRecovering, domain.StatusVerifying,
		domain.StatusResolved, domain.StatusNeedsAttention, domain.StatusRejected, domain.StatusRecoveryFailed,
		domain.StatusCancelled:
		return status, true
	default:
		return "", false
	}
}

type ManualIncident struct {
	Severity        string    `json:"severity"`
	Service         string    `json:"service"`
	Cluster         string    `json:"cluster,omitempty"`
	Namespace       string    `json:"namespace"`
	Resource        string    `json:"resource"`
	Summary         string    `json:"summary"`
	DiagnosisMethod string    `json:"diagnosis_method,omitempty"`
	CausalMode      string    `json:"causal_mode,omitempty"`
	EvidenceStartAt time.Time `json:"evidence_start_at,omitempty"`
}

func (m *IncidentManager) Create(ctx context.Context, input ManualIncident) (*domain.Incident, error) {
	now := time.Now().UTC()
	method, valid := domain.NormalizeDiagnosisMethod(input.DiagnosisMethod)
	if !valid {
		return nil, fmt.Errorf("unsupported diagnosis method %q", input.DiagnosisMethod)
	}
	causalMode, valid := domain.NormalizeCausalMode(input.CausalMode)
	if !valid {
		return nil, fmt.Errorf("unsupported causal mode %q", input.CausalMode)
	}
	evidenceStartAt := input.EvidenceStartAt
	if evidenceStartAt.After(now) || evidenceStartAt.Before(now.Add(-15*time.Minute)) {
		evidenceStartAt = time.Time{}
	}
	in := &domain.Incident{ID: ulid.Make().String(), Status: domain.StatusReceived, Severity: input.Severity, Service: input.Service, Cluster: input.Cluster, Namespace: input.Namespace, Resource: input.Resource, Summary: input.Summary, DiagnosisMethod: method, CausalMode: causalMode, EvidenceStartAt: evidenceStartAt, CreatedAt: now, UpdatedAt: now}
	if in.Severity == "" {
		in.Severity = "warning"
	}
	if err := m.Store.Create(ctx, in); err != nil {
		return nil, err
	}
	m.audit(ctx, in.ID, "incident_created", "incident created", nil)
	if m.Supervisor != nil {
		go m.diagnose(in.ID)
	}
	return in, nil
}
func (m *IncidentManager) IngestAlert(ctx context.Context, a domain.Alert, service, namespace, severity, resource, summary string) (*domain.Incident, error) {
	if a.Status == "resolved" {
		existing, err := m.Store.FindByFingerprint(ctx, a.Fingerprint)
		if err != nil {
			return nil, err
		}
		if existing.Status == domain.StatusAwaitingApproval || existing.Status == domain.StatusReceived || existing.Status == domain.StatusCollecting || existing.Status == domain.StatusDiagnosing {
			_ = domain.Transition(existing, domain.StatusCancelled)
			_ = m.Store.Update(ctx, existing)
		}
		return existing, nil
	}
	if existing, err := m.Store.FindByFingerprint(ctx, a.Fingerprint); err == nil {
		return m.appendAlert(ctx, existing, a)
	}
	if existing := m.correlate(ctx, a, service, namespace, resource); existing != nil {
		merged, err := m.appendAlert(ctx, existing, a)
		if err != nil {
			return nil, err
		}
		m.audit(ctx, merged.ID, "alert_correlated", "alert merged into active incident", map[string]any{"fingerprint": a.Fingerprint})
		m.publish(merged)
		return merged, nil
	}
	now := time.Now().UTC()
	evidenceStartAt := a.StartsAt
	if evidenceStartAt.IsZero() || evidenceStartAt.After(now) || evidenceStartAt.Before(now.Add(-15*time.Minute)) {
		evidenceStartAt = time.Time{}
	}
	in := &domain.Incident{ID: ulid.Make().String(), Status: domain.StatusReceived, Severity: severity, Service: service, Namespace: namespace, Resource: resource, Summary: summary, DiagnosisMethod: domain.DiagnosisMethodKubePilot, CausalMode: domain.CausalModeFull, EvidenceStartAt: evidenceStartAt, CreatedAt: now, UpdatedAt: now, Alerts: []domain.Alert{a}}
	if err := m.Store.Create(ctx, in); err != nil {
		return nil, err
	}
	m.audit(ctx, in.ID, "alert_ingested", "alert created incident", map[string]any{"fingerprint": a.Fingerprint})
	if m.Supervisor != nil {
		go m.diagnose(in.ID)
	}
	return in, nil
}

func (m *IncidentManager) appendAlert(ctx context.Context, existing *domain.Incident, alert domain.Alert) (*domain.Incident, error) {
	if appender, ok := m.Store.(store.AlertAppendStore); ok {
		return appender.AppendAlert(ctx, existing.ID, alert, time.Now().UTC())
	}
	// Compatibility fallback for external IncidentStore implementations. The
	// built-in stores provide AlertAppendStore, which is required to preserve
	// workflow state under concurrent correlation and diagnosis.
	existing.Alerts = append(existing.Alerts, alert)
	existing.UpdatedAt = time.Now().UTC()
	if err := m.Store.Update(ctx, existing); err != nil {
		return nil, err
	}
	return existing, nil
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
	if m.CorrelationFallback != nil {
		fallbackTimeout := m.CorrelationFallbackTimeout
		if fallbackTimeout <= 0 {
			fallbackTimeout = 5 * time.Second
		}
		fallbackCtx, cancel := context.WithTimeout(ctx, fallbackTimeout)
		candidateID, fallbackErr := m.CorrelationFallback.Correlate(fallbackCtx, alert, service, namespace, resource, items)
		cancel()
		if fallbackErr == nil && candidateID != "" {
			for index := range items {
				if items[index].ID == candidateID {
					return &items[index]
				}
			}
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
	now := time.Now().UTC()
	brainAttempt := domain.IsKubePilotBrainMethod(in.DiagnosisMethod)
	var migratedFrom string
	sequence := 1
	if brainAttempt && in.WorkflowAttempt != nil {
		migratedFrom = in.WorkflowAttempt.ID
		sequence = in.WorkflowAttempt.Sequence + 1
		in.WorkflowAttempt.Status = domain.WorkflowAttemptInvalidated
		in.WorkflowAttempt.CompletedAt = now
		in.WorkflowAttempt.InvalidatedArtifactIDs = workflowAttemptArtifacts(in)
		if err = m.Store.Update(ctx, in); err != nil {
			return nil, fmt.Errorf("invalidate previous Workflow Attempt: %w", err)
		}
	}
	if err = domain.Transition(in, domain.StatusReceived); err != nil {
		return nil, err
	}
	in.RootCause, in.RootCauseCategory, in.RootCauseVariant, in.RootCauseService, in.RootCauseResource = "", "", "", "", ""
	in.RootCauseEvidenceIDs, in.Hypotheses, in.Evidence = nil, nil, nil
	in.Confidence = 0
	in.ReasoningType = ""
	in.Proposal, in.DryRun, in.ExecutionContext, in.Verification = nil, nil, nil, nil
	in.WorkflowInterruptID = ""
	in.DiagnosisError = ""
	in.SkillSnapshotHash, in.RankingPolicyHash, in.RerankerConfigHash = "", "", ""
	in.AgentBudget, in.DiagnosisLedger = nil, nil
	if brainAttempt {
		in.Investigation = nil
		in.ExecutionSnapshot = nil
		in.WorkflowAttempt = &domain.WorkflowAttempt{ID: "attempt:" + ulid.Make().String(), IncidentID: in.ID, Sequence: sequence, CheckpointID: "incident:" + in.ID, Status: domain.WorkflowAttemptActive, MigratedFromAttemptID: migratedFrom, StartedAt: now}
	}
	in.UpdatedAt = now
	if m.Checkpoints != nil {
		_ = m.Checkpoints.Delete(ctx, "incident:"+id)
	}
	if err = m.Store.Update(ctx, in); err != nil {
		return nil, err
	}
	if brainAttempt {
		m.audit(ctx, id, "workflow_attempt_migrated", "diagnosis workflow restarted with a new immutable attempt", map[string]any{"attempt_id": in.WorkflowAttempt.ID, "sequence": sequence, "migrated_from_attempt_id": migratedFrom})
	} else {
		m.audit(ctx, id, "incident_retried", "diagnosis workflow restarted", nil)
	}
	if m.Supervisor != nil {
		go m.diagnose(id)
	}
	return in, nil
}

func (m *IncidentManager) MigrateWorkflowAttempt(ctx context.Context, id string) (*domain.Incident, error) {
	in, err := m.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !domain.IsKubePilotBrainMethod(in.DiagnosisMethod) {
		return nil, fmt.Errorf("workflow attempt migration is available only for KubePilot Brain strategies")
	}
	return m.Retry(ctx, id)
}

func workflowAttemptArtifacts(in *domain.Incident) []string {
	if in == nil {
		return nil
	}
	values := []string{}
	if in.Investigation != nil {
		if in.Investigation.AgentDiagnosis != nil {
			values = append(values, in.Investigation.AgentDiagnosis.ID)
		}
		if in.Investigation.AgentRecoveryPlan != nil {
			values = append(values, in.Investigation.AgentRecoveryPlan.ID)
		}
	}
	if in.Proposal != nil {
		values = append(values, in.Proposal.ID)
	}
	if in.DryRun != nil && in.DryRun.MutationSpecHash != "" {
		values = append(values, "dry-run:"+in.DryRun.MutationSpecHash)
	}
	return values
}
func (m *IncidentManager) diagnose(id string) {
	// An incident may legitimately require multiple bounded Agent turns and
	// approval-driven recovery. Model requests and capabilities have their own
	// timeouts, so the orchestration itself must not impose a wall-clock cap.
	workflowCtx, cancel := newWorkflowContext()
	defer cancel()
	if m.ModelSnapshotter != nil {
		workflowCtx = m.ModelSnapshotter.WithSnapshot(workflowCtx)
	}
	in, err := m.Store.Get(workflowCtx, id)
	if err != nil {
		return
	}
	if identity, ok := m.ModelSnapshotter.(interface {
		SnapshotIdentity() (string, string, string)
	}); ok {
		in.ModelConfigHash, in.ModelProtocol, in.ModelName = identity.SnapshotIdentity()
	}
	if domain.IsKubePilotBrainMethod(in.DiagnosisMethod) && in.WorkflowAttempt != nil && in.WorkflowAttempt.ExecutionSnapshot == (domain.ExecutionSnapshot{}) {
		snapshot := m.Supervisor.BrainExecutionSnapshot(in.ModelConfigHash)
		in.ExecutionSnapshot = &snapshot
		in.WorkflowAttempt.ExecutionSnapshot = snapshot
	}
	state, err := m.Supervisor.Run(workflowCtx, in)
	persistCtx, persistCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer persistCancel()
	if err != nil {
		if interrupt, ok := compose.ExtractInterruptInfo(err); ok && len(interrupt.InterruptContexts) > 0 {
			_ = domain.Transition(in, domain.StatusAwaitingApproval)
			in.WorkflowInterruptID = interrupt.InterruptContexts[0].ID
			in.UpdatedAt = time.Now().UTC()
			_ = m.Store.Update(persistCtx, in)
			m.audit(persistCtx, id, "workflow_interrupted", "Eino workflow is awaiting approval", map[string]any{"interrupt_id": in.WorkflowInterruptID})
			m.publish(in)
			return
		}
		_ = domain.Transition(in, domain.StatusNeedsAttention)
		in.DiagnosisError = redactWorkflowError(err)
		in.UpdatedAt = time.Now().UTC()
		_ = m.Store.Update(persistCtx, in)
		m.audit(persistCtx, id, "diagnosis_failed", in.DiagnosisError, nil)
		return
	}
	state.Incident.DiagnosisError = ""
	if persistErr := m.Store.Update(persistCtx, state.Incident); persistErr != nil {
		state.Incident.DiagnosisError = "diagnosis result persistence failed"
		m.audit(persistCtx, id, "diagnosis_persistence_failed", state.Incident.DiagnosisError, map[string]any{"error": workflowgraph.RedactError(persistErr.Error())})
		m.publish(state.Incident)
		return
	}
	if state.Incident.Status == domain.StatusResolved && m.Learner != nil {
		if learnErr := m.Learner.Learn(persistCtx, state.Incident); learnErr != nil {
			m.audit(persistCtx, id, "causal_learning_failed", "resolved incident was not learned", map[string]any{"error": workflowgraph.RedactError(learnErr.Error())})
		}
	}
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
	if in.WorkflowInterruptID == "" {
		_ = domain.Transition(in, domain.StatusNeedsAttention)
		in.UpdatedAt = time.Now().UTC()
		_ = m.Store.Update(ctx, in)
		return nil, fmt.Errorf("Eino approval checkpoint is missing; incident requires retry")
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
		m.audit(ctx, id, "proposal_rejected", comment, nil)
		go m.resumeWorkflow(id, false, idempotencyKey, comment)
		return in, nil
	}
	proposal := *in.Proposal
	m.audit(ctx, id, "proposal_approved", comment, map[string]any{"proposal_id": proposal.ID, "action": proposal.Action})
	m.publish(in)
	go m.resumeWorkflow(id, true, idempotencyKey, comment)
	return in, nil
}

func (m *IncidentManager) resumeWorkflow(id string, approved bool, idempotencyKey, operator string) {
	ctx, cancel := newWorkflowContext()
	defer cancel()
	in, err := m.Store.Get(ctx, id)
	if err != nil {
		return
	}
	currentSkill, currentRanking, currentReranker := m.Supervisor.RuntimeHashesForMethod(in.DiagnosisMethod)
	if in.SkillSnapshotHash != currentSkill || in.RankingPolicyHash != currentRanking || in.RerankerConfigHash != currentReranker {
		_ = domain.Transition(in, domain.StatusNeedsAttention)
		in.DiagnosisError = "workflow runtime configuration changed; explicit retry is required"
		in.UpdatedAt = time.Now().UTC()
		_ = m.Store.Update(context.Background(), in)
		m.audit(context.Background(), id, "workflow_resume_refused", in.DiagnosisError, nil)
		m.publish(in)
		return
	}
	if snapshotter, ok := m.ModelSnapshotter.(interface {
		WithSnapshotHash(context.Context, string) (context.Context, error)
	}); ok {
		ctx, err = snapshotter.WithSnapshotHash(ctx, in.ModelConfigHash)
		if err != nil {
			_ = domain.Transition(in, domain.StatusNeedsAttention)
			in.DiagnosisError = workflowgraph.RedactError(err.Error())
			in.UpdatedAt = time.Now().UTC()
			_ = m.Store.Update(context.Background(), in)
			m.audit(context.Background(), id, "workflow_resume_failed", in.DiagnosisError, nil)
			m.publish(in)
			return
		}
	}
	resume := &agent.ApprovalResumeData{Approved: approved}
	if approved {
		resume.Context = domain.ExecutionContext{NamespaceAllowlist: append([]string(nil), m.AllowedNamespaces...), IncidentID: in.ID, ProposalID: in.Proposal.ID, ApprovalID: ulid.Make().String(), IdempotencyKey: idempotencyKey, Operator: operator, TargetUID: in.Proposal.TargetUID, ResourceVersion: in.Proposal.ResourceVersion, ApprovedAt: time.Now().UTC(), ExpiresAt: in.Proposal.ExpiresAt}
		if in.DryRun != nil {
			resume.Context.MutationSpecHash = in.DryRun.MutationSpecHash
		}
	}
	state, runErr := m.Supervisor.Resume(ctx, id, in.WorkflowInterruptID, resume)
	persistCtx, persistCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer persistCancel()
	if runErr != nil {
		_ = domain.Transition(in, domain.StatusNeedsAttention)
		in.DiagnosisError = redactWorkflowError(runErr)
		in.UpdatedAt = time.Now().UTC()
		_ = m.Store.Update(persistCtx, in)
		m.audit(persistCtx, id, "workflow_resume_failed", in.DiagnosisError, nil)
		m.publish(in)
		return
	}
	state.Incident.WorkflowInterruptID = ""
	if persistErr := m.Store.Update(persistCtx, state.Incident); persistErr != nil {
		state.Incident.DiagnosisError = "workflow result persistence failed"
		m.audit(persistCtx, id, "workflow_persistence_failed", state.Incident.DiagnosisError, map[string]any{"error": workflowgraph.RedactError(persistErr.Error())})
		m.publish(state.Incident)
		return
	}
	if state.Incident.Status == domain.StatusResolved && m.Learner != nil {
		if learnErr := m.Learner.Learn(persistCtx, state.Incident); learnErr != nil {
			m.audit(persistCtx, id, "causal_learning_failed", "resolved incident was not learned", map[string]any{"error": workflowgraph.RedactError(learnErr.Error())})
		}
	}
	m.audit(persistCtx, id, "workflow_completed", "Eino workflow completed after approval", map[string]any{"status": state.Incident.Status})
	m.publish(state.Incident)
}

func newWorkflowContext() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}

func redactWorkflowError(err error) string {
	if err == nil {
		return ""
	}
	message := workflowgraph.RedactError(err.Error())
	if isTransientWorkflowError(err) {
		return "transient request failure after retries: " + message
	}
	return message
}

func isTransientWorkflowError(err error) bool {
	if err == nil {
		return false
	}
	var exhausted *adk.RetryExhaustedError
	if errors.As(err, &exhausted) {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"context deadline exceeded", "failed to receive stream chunk", "connection reset", "broken pipe", "unexpected eof", "request failed after"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
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
