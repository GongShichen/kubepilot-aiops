package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	workflowgraph "github.com/kubepilot-aiops/kubepilot/graph"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/retrieval"
	"github.com/oklog/ulid/v2"
)

type PostgresStore struct{ pool *pgxpool.Pool }

func NewPostgres(ctx context.Context, dsn string) (*PostgresStore, error) {
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err = p.Ping(ctx); err != nil {
		p.Close()
		return nil, err
	}
	return &PostgresStore{pool: p}, nil
}
func (s *PostgresStore) Close() { s.pool.Close() }

func (s *PostgresStore) Create(ctx context.Context, in *domain.Incident) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	payload, err := json.Marshal(in)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO incidents(id,status,severity,service,namespace,resource,summary,payload,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, in.ID, in.Status, in.Severity, in.Service, in.Namespace, in.Resource, in.Summary, payload, in.CreatedAt, in.UpdatedAt)
	if err != nil {
		return err
	}
	if err = syncIncidentRecords(ctx, tx, in); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *PostgresStore) Update(ctx context.Context, in *domain.Incident) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	payload, err := json.Marshal(in)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE incidents SET status=$2,severity=$3,service=$4,namespace=$5,resource=$6,summary=$7,payload=$8,updated_at=$9 WHERE id=$1`, in.ID, in.Status, in.Severity, in.Service, in.Namespace, in.Resource, in.Summary, payload, in.UpdatedAt)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if err = syncIncidentRecords(ctx, tx, in); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) UpdateWorkflowStatus(ctx context.Context, id string, status domain.IncidentStatus, occurredAt time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	tag, err := tx.Exec(ctx, `UPDATE incidents
		SET status=$2,
			payload=jsonb_set(jsonb_set(payload,'{status}',to_jsonb($2::text),true),'{updated_at}',to_jsonb($3::timestamptz),true),
			updated_at=$3
		WHERE id=$1`, id, status, occurredAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	_, err = tx.Exec(ctx, `UPDATE agent_workflows
		SET status=$2,
			interrupted_at=CASE WHEN $2=$4 THEN COALESCE(interrupted_at,$3) ELSE interrupted_at END,
			resumed_at=CASE WHEN $2 = ANY($5::text[]) THEN COALESCE(resumed_at,$3) ELSE resumed_at END,
			completed_at=CASE WHEN $2 = ANY($6::text[]) THEN COALESCE(completed_at,$3) ELSE completed_at END
		WHERE incident_id=$1`, id, status, occurredAt, domain.StatusAwaitingApproval,
		[]string{string(domain.StatusRecovering), string(domain.StatusVerifying), string(domain.StatusResolved), string(domain.StatusRecoveryFailed)},
		[]string{string(domain.StatusResolved), string(domain.StatusRejected), string(domain.StatusRecoveryFailed), string(domain.StatusCancelled), string(domain.StatusNeedsAttention)})
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func syncIncidentRecords(ctx context.Context, tx pgx.Tx, in *domain.Incident) error {
	for _, alert := range in.Alerts {
		raw, err := json.Marshal(alert)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO alerts(fingerprint,incident_id,status,payload,created_at) VALUES($1,$2,$3,$4,$5) ON CONFLICT(fingerprint,incident_id) DO UPDATE SET status=EXCLUDED.status,payload=EXCLUDED.payload`, alert.Fingerprint, in.ID, alert.Status, raw, time.Now().UTC())
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO incident_alerts(incident_id,fingerprint) VALUES($1,$2) ON CONFLICT DO NOTHING`, in.ID, alert.Fingerprint); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM evidence WHERE incident_id=$1`, in.ID); err != nil {
		return err
	}
	for index, evidence := range in.Evidence {
		id := evidence.ID
		if id == "" {
			id = fmt.Sprintf("%s-evidence-%d", in.ID, index)
		}
		raw, err := json.Marshal(evidence)
		if err != nil {
			return err
		}
		observed := evidence.Timestamp
		if observed.IsZero() {
			observed = evidence.ObservedAt
		}
		if observed.IsZero() {
			observed = in.UpdatedAt
		}
		kind := evidence.Type
		if kind == "" {
			kind = evidence.Kind
		}
		if _, err = tx.Exec(ctx, `INSERT INTO evidence(id,incident_id,source,kind,payload,observed_at) VALUES($1,$2,$3,$4,$5,$6)`, id, in.ID, evidence.Source, kind, raw, observed); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM hypotheses WHERE incident_id=$1`, in.ID); err != nil {
		return err
	}
	for index, hypothesis := range in.Hypotheses {
		localID := hypothesis.ID
		if localID == "" {
			localID = fmt.Sprintf("hypothesis-%d", index)
		}
		// ADK output IDs (for example h1/h2) are scoped to one Incident;
		// the normalized table primary key must remain globally unique.
		id := hypothesisRecordID(in.ID, localID)
		raw, err := json.Marshal(hypothesis)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO hypotheses(id,incident_id,probability,payload) VALUES($1,$2,$3,$4)`, id, in.ID, hypothesis.Probability, raw); err != nil {
			return err
		}
	}
	if in.Proposal != nil {
		raw, err := json.Marshal(in.Proposal)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO recovery_proposals(id,incident_id,status,payload,expires_at) VALUES($1,$2,$3,$4,$5) ON CONFLICT(id) DO UPDATE SET status=EXCLUDED.status,payload=EXCLUDED.payload,expires_at=EXCLUDED.expires_at`, in.Proposal.ID, in.ID, in.Status, raw, in.Proposal.ExpiresAt); err != nil {
			return err
		}
	}
	if in.Verification != nil {
		raw, err := json.Marshal(in.Verification)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO verifications(id,incident_id,success,payload) VALUES($1,$2,$3,$4) ON CONFLICT(id) DO UPDATE SET success=EXCLUDED.success,payload=EXCLUDED.payload,created_at=NOW()`, in.ID+"-verification", in.ID, in.Verification.Success, raw); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO agent_workflows(incident_id,graph_version,checkpoint_id,interrupt_id,model_protocol,model_name,model_config_hash,status,started_at,interrupted_at,resumed_at,completed_at,last_error)
		VALUES($1,'eino-incident-v2',$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT(incident_id) DO UPDATE SET graph_version=EXCLUDED.graph_version,checkpoint_id=EXCLUDED.checkpoint_id,interrupt_id=EXCLUDED.interrupt_id,model_protocol=EXCLUDED.model_protocol,model_name=EXCLUDED.model_name,model_config_hash=EXCLUDED.model_config_hash,status=EXCLUDED.status,interrupted_at=COALESCE(agent_workflows.interrupted_at,EXCLUDED.interrupted_at),resumed_at=COALESCE(agent_workflows.resumed_at,EXCLUDED.resumed_at),completed_at=EXCLUDED.completed_at,last_error=EXCLUDED.last_error`,
		in.ID, "incident:"+in.ID, nullString(in.WorkflowInterruptID), nullString(in.ModelProtocol), nullString(in.ModelName), nullString(in.ModelConfigHash), in.Status, in.CreatedAt, statusTime(in.Status == domain.StatusAwaitingApproval, in.UpdatedAt), statusTime(in.Status == domain.StatusRecovering || in.Status == domain.StatusVerifying || in.Status == domain.StatusResolved || in.Status == domain.StatusRecoveryFailed, in.UpdatedAt), statusTime(terminalWorkflow(in.Status), in.UpdatedAt), nullString(in.DiagnosisError)); err != nil {
		return err
	}
	return nil
}

func hypothesisRecordID(incidentID, localID string) string {
	return incidentID + "-" + localID
}

func statusTime(condition bool, value time.Time) any {
	if condition {
		return value
	}
	return nil
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func terminalWorkflow(status domain.IncidentStatus) bool {
	switch status {
	case domain.StatusResolved, domain.StatusRejected, domain.StatusRecoveryFailed, domain.StatusCancelled, domain.StatusNeedsAttention:
		return true
	default:
		return false
	}
}
func (s *PostgresStore) Get(ctx context.Context, id string) (*domain.Incident, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT payload FROM incidents WHERE id=$1`, id).Scan(&raw)
	if err != nil {
		return nil, ErrNotFound
	}
	var in domain.Incident
	if err = json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	return &in, nil
}
func (s *PostgresStore) List(ctx context.Context, limit, offset int) ([]domain.Incident, error) {
	rows, err := s.pool.Query(ctx, `SELECT payload FROM incidents ORDER BY created_at DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Incident
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var in domain.Incident
		if err = json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}
func (s *PostgresStore) FindByFingerprint(ctx context.Context, fp string) (*domain.Incident, error) {
	var id string
	err := s.pool.QueryRow(ctx, `SELECT incident_id FROM alerts WHERE fingerprint=$1 ORDER BY created_at DESC LIMIT 1`, fp).Scan(&id)
	if err != nil {
		return nil, ErrNotFound
	}
	return s.Get(ctx, id)
}
func (s *PostgresStore) AppendAudit(ctx context.Context, e domain.AuditEvent) error {
	raw, err := json.Marshal(e.Data)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO audit_events(id,incident_id,type,message,data,created_at) VALUES($1,$2,$3,$4,$5,$6)`, e.ID, e.IncidentID, e.Type, e.Message, raw, e.CreatedAt)
	return err
}
func (s *PostgresStore) ListAudit(ctx context.Context, id string) ([]domain.AuditEvent, error) {
	rows, err := s.pool.Query(ctx, `SELECT id,type,message,data,created_at FROM audit_events WHERE incident_id=$1 ORDER BY created_at`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.AuditEvent
	for rows.Next() {
		var e domain.AuditEvent
		var raw []byte
		e.IncidentID = id
		if err = rows.Scan(&e.ID, &e.Type, &e.Message, &raw, &e.CreatedAt); err != nil {
			return nil, err
		}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &e.Data)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *PostgresStore) RecordApproval(ctx context.Context, key, incidentID, proposalID, decision, comment string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `INSERT INTO approvals(id,incident_id,proposal_id,decision,idempotency_key,comment) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(idempotency_key) DO NOTHING`, ulid.Make().String(), incidentID, proposalID, decision, key, comment)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *PostgresStore) RecordWorkflowEvent(ctx context.Context, event workflowgraph.WorkflowEvent) error {
	if event.RunID == "" {
		return nil
	}
	switch event.Type {
	case "tool_started":
		_, err := s.pool.Exec(ctx, `INSERT INTO tool_calls(id,incident_id,tool,status,started_at) VALUES($1,$2,$3,'running',$4) ON CONFLICT(id) DO NOTHING`, event.RunID, event.IncidentID, event.Name, event.OccurredAt)
		return err
	case "tool_completed":
		status := "succeeded"
		errorClass := any(nil)
		if event.Error != "" {
			status = "failed"
			errorClass = "component_error"
		}
		_, err := s.pool.Exec(ctx, `UPDATE tool_calls SET status=$2,error_class=$3,finished_at=$4,response=jsonb_build_object('error',$5::text) WHERE id=$1`, event.RunID, status, errorClass, event.OccurredAt, event.Error)
		return err
	default:
		return nil
	}
}

func (s *PostgresStore) UpsertLogTemplates(ctx context.Context, records []retrieval.LogTemplateRecord) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	for _, record := range records {
		if _, err = tx.Exec(ctx, `INSERT INTO log_templates(id,namespace,service,template,cluster_id,occurrence_count,indexed_at) VALUES($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT(id) DO UPDATE SET cluster_id=EXCLUDED.cluster_id,occurrence_count=GREATEST(log_templates.occurrence_count,EXCLUDED.occurrence_count),indexed_at=GREATEST(log_templates.indexed_at,EXCLUDED.indexed_at)`,
			record.ID, record.Namespace, record.Service, record.Template, record.ClusterID, record.OccurrenceCount, record.IndexedAt); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

var _ IncidentStore = (*PostgresStore)(nil)
var _ WorkflowStatusStore = (*PostgresStore)(nil)
var _ = errors.Is
