package store

import (
	"context"
	"encoding/json"

	"github.com/kubepilot-aiops/kubepilot/internal/topology"
)

// PostgresGraphStore stores observational graphs independently from the
// resolved incident knowledge row. This allows a paused or still-diagnosing
// workflow to resume with the same graph without making it eligible for
// historical retrieval before resolution.
type PostgresGraphStore struct{ owner *PostgresStore }

func NewPostgresGraphStore(owner *PostgresStore) *PostgresGraphStore {
	return &PostgresGraphStore{owner: owner}
}

func (s *PostgresGraphStore) Put(ctx context.Context, graph topology.IncidentGraph) error {
	if s == nil || s.owner == nil || graph.IncidentID == "" {
		return topology.ErrNotFound
	}
	raw, err := json.Marshal(graph.Normalize())
	if err != nil {
		return err
	}
	_, err = s.owner.pool.Exec(ctx, `INSERT INTO incident_graphs(incident_id,graph,updated_at)
		VALUES($1,$2,NOW())
		ON CONFLICT(incident_id) DO UPDATE SET graph=EXCLUDED.graph,updated_at=NOW()`, graph.IncidentID, raw)
	return err
}

func (s *PostgresGraphStore) Get(ctx context.Context, incidentID string) (topology.IncidentGraph, error) {
	if s == nil || s.owner == nil || incidentID == "" {
		return topology.IncidentGraph{}, topology.ErrNotFound
	}
	var raw []byte
	if err := s.owner.pool.QueryRow(ctx, `SELECT graph FROM incident_graphs WHERE incident_id=$1`, incidentID).Scan(&raw); err != nil {
		return topology.IncidentGraph{}, err
	}
	var graph topology.IncidentGraph
	if err := json.Unmarshal(raw, &graph); err != nil {
		return topology.IncidentGraph{}, err
	}
	return graph.Normalize(), nil
}

var _ topology.GraphStore = (*PostgresGraphStore)(nil)
