package store

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

var ErrNotFound = errors.New("not found")

type IncidentStore interface {
	Create(context.Context, *domain.Incident) error
	Update(context.Context, *domain.Incident) error
	Get(context.Context, string) (*domain.Incident, error)
	List(context.Context, int, int) ([]domain.Incident, error)
	FindByFingerprint(context.Context, string) (*domain.Incident, error)
	AppendAudit(context.Context, domain.AuditEvent) error
	ListAudit(context.Context, string) ([]domain.AuditEvent, error)
	RecordApproval(context.Context, string, string, string, string, string) (bool, error)
}

type MemoryStore struct {
	mu        sync.RWMutex
	incidents map[string]*domain.Incident
	audit     map[string][]domain.AuditEvent
	approvals map[string]bool
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{incidents: map[string]*domain.Incident{}, audit: map[string][]domain.AuditEvent{}, approvals: map[string]bool{}}
}
func (s *MemoryStore) RecordApproval(_ context.Context, key, _, _, _, _ string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.approvals[key] {
		return false, nil
	}
	s.approvals[key] = true
	return true, nil
}

func (s *MemoryStore) Create(_ context.Context, in *domain.Incident) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *in
	s.incidents[in.ID] = &cp
	return nil
}
func (s *MemoryStore) Update(_ context.Context, in *domain.Incident) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.incidents[in.ID]; !ok {
		return ErrNotFound
	}
	cp := *in
	s.incidents[in.ID] = &cp
	return nil
}
func (s *MemoryStore) Get(_ context.Context, id string) (*domain.Incident, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	in, ok := s.incidents[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *in
	return &cp, nil
}
func (s *MemoryStore) List(_ context.Context, limit, offset int) ([]domain.Incident, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	all := make([]domain.Incident, 0, len(s.incidents))
	for _, v := range s.incidents {
		all = append(all, *v)
	}
	if offset >= len(all) {
		return []domain.Incident{}, nil
	}
	end := offset + limit
	if end > len(all) {
		end = len(all)
	}
	return all[offset:end], nil
}
func (s *MemoryStore) FindByFingerprint(_ context.Context, fingerprint string) (*domain.Incident, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, in := range s.incidents {
		for _, a := range in.Alerts {
			if a.Fingerprint == fingerprint {
				cp := *in
				return &cp, nil
			}
		}
	}
	return nil, ErrNotFound
}
func (s *MemoryStore) AppendAudit(_ context.Context, e domain.AuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audit[e.IncidentID] = append(s.audit[e.IncidentID], e)
	return nil
}
func (s *MemoryStore) ListAudit(_ context.Context, id string) ([]domain.AuditEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]domain.AuditEvent(nil), s.audit[id]...), nil
}

type CheckpointStore interface {
	Save(context.Context, string, []byte, time.Duration) error
	Load(context.Context, string) ([]byte, error)
	Delete(context.Context, string) error
	Lock(context.Context, string, string, time.Duration) (bool, error)
	Unlock(context.Context, string, string) error
}
