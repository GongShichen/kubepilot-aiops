package topology

import (
	"context"
	"errors"
	"sync"
)

var ErrNotFound = errors.New("incident graph not found")

type GraphStore interface {
	Put(context.Context, IncidentGraph) error
	Get(context.Context, string) (IncidentGraph, error)
}

type MemoryStore struct {
	mu     sync.RWMutex
	graphs map[string]IncidentGraph
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{graphs: map[string]IncidentGraph{}} }
func (s *MemoryStore) Put(_ context.Context, graph IncidentGraph) error {
	if s == nil || graph.IncidentID == "" {
		return ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.graphs[graph.IncidentID] = graph.Normalize()
	return nil
}
func (s *MemoryStore) Get(_ context.Context, id string) (IncidentGraph, error) {
	if s == nil {
		return IncidentGraph{}, ErrNotFound
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	graph, ok := s.graphs[id]
	if !ok {
		return IncidentGraph{}, ErrNotFound
	}
	return graph, nil
}
