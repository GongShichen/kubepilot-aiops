package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
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
	payload, err := json.Marshal(in)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO incidents(id,status,severity,service,namespace,resource,summary,payload,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, in.ID, in.Status, in.Severity, in.Service, in.Namespace, in.Resource, in.Summary, payload, in.CreatedAt, in.UpdatedAt)
	if err != nil {
		return err
	}
	return s.syncAlerts(ctx, in)
}
func (s *PostgresStore) Update(ctx context.Context, in *domain.Incident) error {
	payload, err := json.Marshal(in)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `UPDATE incidents SET status=$2,severity=$3,service=$4,namespace=$5,resource=$6,summary=$7,payload=$8,updated_at=$9 WHERE id=$1`, in.ID, in.Status, in.Severity, in.Service, in.Namespace, in.Resource, in.Summary, payload, in.UpdatedAt)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return s.syncAlerts(ctx, in)
}

func (s *PostgresStore) syncAlerts(ctx context.Context, in *domain.Incident) error {
	for _, alert := range in.Alerts {
		raw, err := json.Marshal(alert)
		if err != nil {
			return err
		}
		_, err = s.pool.Exec(ctx, `INSERT INTO alerts(fingerprint,incident_id,status,payload,created_at) VALUES($1,$2,$3,$4,$5) ON CONFLICT(fingerprint,incident_id) DO UPDATE SET status=EXCLUDED.status,payload=EXCLUDED.payload`, alert.Fingerprint, in.ID, alert.Status, raw, time.Now().UTC())
		if err != nil {
			return err
		}
	}
	return nil
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

var _ IncidentStore = (*PostgresStore)(nil)
var _ = errors.Is
