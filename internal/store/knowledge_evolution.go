package store

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"

	causaldiscovery "github.com/kubepilot-aiops/kubepilot/internal/causal/discovery"
	causalknowledge "github.com/kubepilot-aiops/kubepilot/internal/causal/knowledge"
	"github.com/kubepilot-aiops/kubepilot/internal/topology"
	topologyknowledge "github.com/kubepilot-aiops/kubepilot/internal/topology/knowledge"
)

type PostgresTopologyPatternStore struct{ owner *PostgresStore }

func NewPostgresTopologyPatternStore(owner *PostgresStore) *PostgresTopologyPatternStore {
	return &PostgresTopologyPatternStore{owner: owner}
}
func (s *PostgresTopologyPatternStore) List(ctx context.Context, limit int) ([]topologyknowledge.ServiceTopologyPattern, error) {
	if s == nil || s.owner == nil {
		return nil, errors.New("topology pattern store unavailable")
	}
	query := `SELECT pattern_id,nodes,edges,frequency,confidence,source_incidents,last_observed FROM topology_patterns ORDER BY confidence DESC,last_observed DESC,pattern_id`
	args := []any{}
	if limit > 0 {
		query += ` LIMIT $1`
		args = append(args, limit)
	}
	rows, err := s.owner.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []topologyknowledge.ServiceTopologyPattern{}
	for rows.Next() {
		var p topologyknowledge.ServiceTopologyPattern
		var nodes, edges, incidents []byte
		if err := rows.Scan(&p.PatternID, &nodes, &edges, &p.Frequency, &p.Confidence, &incidents, &p.LastObserved); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(nodes, &p.Nodes); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(edges, &p.Edges); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(incidents, &p.SourceIncidents); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *PostgresTopologyPatternStore) Search(ctx context.Context, query topology.IncidentGraph, limit int) ([]topologyknowledge.ServiceTopologyPattern, error) {
	items, err := s.List(ctx, 0)
	if err != nil {
		return nil, err
	}
	q := topologyknowledge.Normalize(query)
	sort.SliceStable(items, func(i, j int) bool { return topologyPatternScore(q, items[i]) > topologyPatternScore(q, items[j]) })
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}
func (s *PostgresTopologyPatternStore) Merge(ctx context.Context, p topologyknowledge.ServiceTopologyPattern) (topologyknowledge.ServiceTopologyPattern, error) {
	if s == nil || s.owner == nil {
		return p, errors.New("topology pattern store unavailable")
	}
	p.Frequency = maxInt(1, p.Frequency)
	if old, err := s.find(ctx, p.PatternID); err == nil {
		p = topologyknowledge.Merge(old, p)
	}
	nodes, _ := json.Marshal(p.Nodes)
	edges, _ := json.Marshal(p.Edges)
	incidents, _ := json.Marshal(p.SourceIncidents)
	_, err := s.owner.pool.Exec(ctx, `INSERT INTO topology_patterns(pattern_id,nodes,edges,frequency,confidence,source_incidents,last_observed) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(pattern_id) DO UPDATE SET frequency=EXCLUDED.frequency,confidence=EXCLUDED.confidence,source_incidents=(SELECT jsonb_agg(DISTINCT value) FROM jsonb_array_elements(topology_patterns.source_incidents||EXCLUDED.source_incidents) value),last_observed=GREATEST(topology_patterns.last_observed,EXCLUDED.last_observed),updated_at=NOW()`, p.PatternID, nodes, edges, p.Frequency, p.Confidence, incidents, p.LastObserved)
	if err != nil {
		return p, err
	}
	return p, nil
}
func (s *PostgresTopologyPatternStore) find(ctx context.Context, id string) (topologyknowledge.ServiceTopologyPattern, error) {
	items, err := s.List(ctx, 0)
	if err != nil {
		return topologyknowledge.ServiceTopologyPattern{}, err
	}
	for _, p := range items {
		if p.PatternID == id {
			return p, nil
		}
	}
	return topologyknowledge.ServiceTopologyPattern{}, topologyknowledge.ErrNotFound
}

type PostgresCausalKnowledgeStore struct{ owner *PostgresStore }

func NewPostgresCausalKnowledgeStore(owner *PostgresStore) *PostgresCausalKnowledgeStore {
	return &PostgresCausalKnowledgeStore{owner: owner}
}
func (s *PostgresCausalKnowledgeStore) List(ctx context.Context, status string, limit int) ([]causalknowledge.CausalPattern, error) {
	if s == nil || s.owner == nil {
		return nil, errors.New("causal knowledge store unavailable")
	}
	q := `SELECT pattern,status,confidence,source_incidents,created_at,updated_at FROM evolving_causal_patterns`
	args := []any{}
	if status != "" {
		q += ` WHERE status=$1`
		args = append(args, status)
	}
	q += ` ORDER BY confidence DESC,updated_at DESC`
	if limit > 0 {
		q += ` LIMIT $` + itoa(len(args)+1)
		args = append(args, limit)
	}
	rows, err := s.owner.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []causalknowledge.CausalPattern{}
	for rows.Next() {
		var raw, incidents []byte
		var p causalknowledge.CausalPattern
		var status string
		var confidence float64
		if err := rows.Scan(&raw, &status, &confidence, &incidents, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		p.Status = status
		p.Confidence = confidence
		if err := json.Unmarshal(incidents, &p.SourceIncidents); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *PostgresCausalKnowledgeStore) Merge(ctx context.Context, p causalknowledge.CausalPattern) (causalknowledge.CausalPattern, error) {
	if s == nil || s.owner == nil {
		return p, errors.New("causal knowledge store unavailable")
	}
	p = causalknowledge.Canonicalize(p)
	raw, _ := json.Marshal(p)
	incidents, _ := json.Marshal(p.SourceIncidents)
	_, err := s.owner.pool.Exec(ctx, `INSERT INTO evolving_causal_patterns(pattern_id,pattern,status,confidence,source_incidents,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(pattern_id) DO UPDATE SET pattern=EXCLUDED.pattern,status=EXCLUDED.status,confidence=EXCLUDED.confidence,source_incidents=(SELECT jsonb_agg(DISTINCT value) FROM jsonb_array_elements(evolving_causal_patterns.source_incidents||EXCLUDED.source_incidents) value),updated_at=NOW()`, p.PatternID, raw, p.Status, p.Confidence, incidents, p.CreatedAt, p.UpdatedAt)
	return p, err
}

func topologyPatternScore(q topologyknowledge.ServiceTopologyPattern, p topologyknowledge.ServiceTopologyPattern) float64 {
	left := map[string]bool{}
	right := map[string]bool{}
	for _, e := range q.Edges {
		left[e.Source+">"+e.Target+":"+e.Type] = true
	}
	for _, e := range p.Edges {
		right[e.Source+">"+e.Target+":"+e.Type] = true
	}
	u, i := 0, 0
	for k := range left {
		u++
		if right[k] {
			i++
		}
	}
	for k := range right {
		if !left[k] {
			u++
		}
	}
	if u == 0 {
		return 0
	}
	return float64(i) / float64(u)
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func itoa(v int) string { return strconv.Itoa(v) }

var _ topologyknowledge.PatternStore = (*PostgresTopologyPatternStore)(nil)
var _ causalknowledge.PatternStore = (*PostgresCausalKnowledgeStore)(nil)

// PostgresCausalCandidateStore is the persistence boundary for discovered
// candidates. Agents only receive its Reader interface; Upsert is used by the
// resolved-Incident learning service after deterministic validation.
type PostgresCausalCandidateStore struct{ owner *PostgresStore }

func NewPostgresCausalCandidateStore(owner *PostgresStore) *PostgresCausalCandidateStore {
	return &PostgresCausalCandidateStore{owner: owner}
}

func (s *PostgresCausalCandidateStore) List(ctx context.Context, status string, limit int) ([]causaldiscovery.CausalPatternCandidate, error) {
	if s == nil || s.owner == nil {
		return nil, errors.New("causal candidate store unavailable")
	}
	query := `SELECT pattern_id,causal_path,supporting_incidents,support_count,coverage,evidence_confidence,causal_consistency,contradiction_penalty,confidence,contradictions,status,explanation,created_at,updated_at FROM causal_pattern_candidates`
	args := []any{}
	if status != "" {
		query += ` WHERE status=$1`
		args = append(args, status)
	}
	query += ` ORDER BY confidence DESC,updated_at DESC,pattern_id`
	if limit > 0 {
		query += ` LIMIT $` + itoa(len(args)+1)
		args = append(args, limit)
	}
	rows, err := s.owner.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []causaldiscovery.CausalPatternCandidate{}
	for rows.Next() {
		var item causaldiscovery.CausalPatternCandidate
		var path, incidents, contradictions []byte
		if err := rows.Scan(&item.PatternID, &path, &incidents, &item.Frequency, &item.Coverage, &item.EvidenceConfidence, &item.CausalConsistency, &item.ContradictionPenalty, &item.Confidence, &contradictions, &item.Status, &item.Explanation, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(path, &item.CausalPath); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(incidents, &item.SupportingIncidents); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(contradictions, &item.Contradictions); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *PostgresCausalCandidateStore) Search(ctx context.Context, terms []string, limit int) ([]causaldiscovery.CausalPatternCandidate, error) {
	items, err := s.List(ctx, causaldiscovery.StatusAccepted, 0)
	if err != nil {
		return nil, err
	}
	if len(terms) == 0 {
		if limit > 0 && len(items) > limit {
			items = items[:limit]
		}
		return items, nil
	}
	filtered := make([]causaldiscovery.CausalPatternCandidate, 0, len(items))
	for _, item := range items {
		text := strings.ToLower(strings.Join(item.CausalPath, " "))
		for _, term := range terms {
			if term != "" && strings.Contains(text, strings.ToLower(strings.TrimSpace(term))) {
				filtered = append(filtered, item)
				break
			}
		}
	}
	if limit > 0 && len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered, nil
}

func (s *PostgresCausalCandidateStore) Upsert(ctx context.Context, item causaldiscovery.CausalPatternCandidate) error {
	if s == nil || s.owner == nil {
		return errors.New("causal candidate store unavailable")
	}
	item = causaldiscovery.NormalizeCandidate(item)
	path, _ := json.Marshal(item.CausalPath)
	incidents, _ := json.Marshal(item.SupportingIncidents)
	contradictions, _ := json.Marshal(item.Contradictions)
	_, err := s.owner.pool.Exec(ctx, `INSERT INTO causal_pattern_candidates(id,pattern_id,causal_path,supporting_incidents,support_count,coverage,evidence_confidence,causal_consistency,contradiction_penalty,confidence,contradictions,status,explanation,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15) ON CONFLICT(pattern_id) DO UPDATE SET causal_path=EXCLUDED.causal_path,supporting_incidents=EXCLUDED.supporting_incidents,support_count=EXCLUDED.support_count,coverage=EXCLUDED.coverage,evidence_confidence=EXCLUDED.evidence_confidence,causal_consistency=EXCLUDED.causal_consistency,contradiction_penalty=EXCLUDED.contradiction_penalty,confidence=EXCLUDED.confidence,contradictions=EXCLUDED.contradictions,status=EXCLUDED.status,explanation=EXCLUDED.explanation,updated_at=EXCLUDED.updated_at`, item.PatternID, item.PatternID, path, incidents, item.Frequency, item.Coverage, item.EvidenceConfidence, item.CausalConsistency, item.ContradictionPenalty, item.Confidence, contradictions, item.Status, item.Explanation, item.CreatedAt, item.UpdatedAt)
	return err
}

var _ causaldiscovery.Store = (*PostgresCausalCandidateStore)(nil)
