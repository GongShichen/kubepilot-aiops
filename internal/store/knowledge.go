package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	topologyretrieval "github.com/kubepilot-aiops/kubepilot/retrieval/topology"
	"github.com/oklog/ulid/v2"
)

func (s *PostgresStore) UpsertIncidentKnowledge(ctx context.Context, in *domain.Incident, features domain.IncidentFeatures, embeddingVersion string) error {
	if in == nil || in.Status != domain.StatusResolved {
		return fmt.Errorf("only resolved incidents can enter incident knowledge")
	}
	featuresRaw, err := json.Marshal(features)
	if err != nil {
		return err
	}
	observations, err := json.Marshal(features.Observed)
	if err != nil {
		return err
	}
	topologyServices := append([]string(nil), features.TopologyServices...)
	if len(topologyServices) == 0 {
		for _, node := range features.TopologyGraph.Nodes {
			if node.ID != "" {
				topologyServices = append(topologyServices, node.ID)
			}
		}
	}
	topology, err := json.Marshal(topologyServices)
	if err != nil {
		return err
	}
	topologyGraph, err := json.Marshal(features.TopologyGraph)
	if err != nil {
		return err
	}
	searchText := strings.Join(append(append([]string{in.Summary, in.RootCause, in.RootCauseCategory, in.RootCauseVariant, in.Service, in.Resource}, features.Terms...), features.EvidenceTypes...), " ")
	_, err = s.pool.Exec(ctx, `INSERT INTO incident_knowledge(incident_id,namespace,service,resource,category,root_cause,observations,features,topology,topology_graph,search_vector,embedding_version,resolved_at,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,to_tsvector('simple',$11),$12,$13,NOW())
		ON CONFLICT(incident_id) DO UPDATE SET namespace=EXCLUDED.namespace,service=EXCLUDED.service,resource=EXCLUDED.resource,category=EXCLUDED.category,root_cause=EXCLUDED.root_cause,observations=EXCLUDED.observations,features=EXCLUDED.features,topology=EXCLUDED.topology,topology_graph=EXCLUDED.topology_graph,search_vector=EXCLUDED.search_vector,embedding_version=EXCLUDED.embedding_version,resolved_at=EXCLUDED.resolved_at,updated_at=NOW()`,
		in.ID, in.Namespace, in.Service, in.Resource, in.RootCauseCategory, in.RootCause, observations, featuresRaw, topology, topologyGraph, searchText, embeddingVersion, in.UpdatedAt)
	return err
}

func (s *PostgresStore) SearchLexicalIncidents(ctx context.Context, features domain.IncidentFeatures, limit int) ([]domain.RetrievalCandidate, error) {
	if limit <= 0 {
		return []domain.RetrievalCandidate{}, nil
	}
	query := strings.Join(features.Terms, " ")
	if strings.TrimSpace(query) == "" {
		query = strings.Join([]string{features.Service, features.Resource}, " ")
	}
	rows, err := s.pool.Query(ctx, `SELECT incident_id,namespace,service,resource,category,root_cause,features,topology_graph,
		ts_rank_cd(search_vector,websearch_to_tsquery('simple',$2)) + CASE WHEN service=$3 THEN 0.15 ELSE 0 END AS score
		FROM incident_knowledge WHERE namespace=$1 AND search_vector @@ websearch_to_tsquery('simple',$2)
		ORDER BY score DESC, incident_id ASC LIMIT $4`, features.Namespace, query, features.Service, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanKnowledgeCandidates(rows, "lexical")
}

func (s *PostgresStore) SearchTopologyIncidents(ctx context.Context, features domain.IncidentFeatures, limit int) ([]domain.RetrievalCandidate, error) {
	if limit <= 0 {
		return []domain.RetrievalCandidate{}, nil
	}
	query := topologyretrieval.BuildGraphQuery(features, limit)
	rows, err := s.pool.Query(ctx, `SELECT incident_id,namespace,service,resource,category,root_cause,features,topology_graph,
		COALESCE((SELECT COUNT(*)::double precision/GREATEST(1,$2) FROM jsonb_array_elements(COALESCE(topology_graph->'nodes','[]'::jsonb)) node
			WHERE COALESCE(node->>'id',node->>'name')=ANY($1::text[])),0) +
		CASE WHEN service=ANY($1::text[]) THEN 0.05 ELSE 0 END AS score
		FROM incident_knowledge
		WHERE jsonb_array_length(COALESCE(topology_graph->'nodes','[]'::jsonb)) > 0
		   OR topology ?| $1::text[]
		ORDER BY score DESC, incident_id ASC LIMIT $3`, query.NodeIDs, len(query.NodeIDs), query.CandidateLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	candidates, err := scanKnowledgeCandidates(rows, "topology")
	if err != nil {
		return nil, err
	}
	// SQL narrows the candidate set using the indexed topology service set;
	// graph similarity is the authoritative ranking signal once candidates are
	// loaded. This allows payment->mysql to match order->mysql without requiring
	// the root service names to be identical.
	for index := range candidates {
		candidate := &candidates[index]
		graphScore := topologyretrieval.GraphCandidateScore(features.TopologyGraph, candidate.Features.TopologyGraph)
		if len(features.TopologyGraph.Edges) > 0 && len(candidate.Features.TopologyGraph.Edges) > 0 {
			candidate.SourceScores["topology"] = graphScore
		} else {
			candidate.SourceScores["topology"] = serviceTopologyOverlap(query.NodeIDs, candidate.Features.TopologyServices)
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		left := candidates[i].SourceScores["topology"]
		right := candidates[j].SourceScores["topology"]
		if left == right {
			return candidates[i].IncidentID < candidates[j].IncidentID
		}
		return left > right
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates, nil
}

type knowledgeRows interface {
	Next() bool
	Scan(...any) error
	Err() error
}

func scanKnowledgeCandidates(rows knowledgeRows, source string) ([]domain.RetrievalCandidate, error) {
	var out []domain.RetrievalCandidate
	for rows.Next() {
		var item domain.RetrievalCandidate
		var raw []byte
		var topologyRaw []byte
		var score float64
		if err := rows.Scan(&item.IncidentID, &item.Namespace, &item.Service, &item.Resource, &item.Category, &item.RootCause, &raw, &topologyRaw, &score); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &item.Features); err != nil {
			return nil, err
		}
		if len(topologyRaw) > 0 {
			// topology_graph is explicit for new rows; the features payload keeps
			// backward compatibility with rows written before migration 006.
			var persisted domain.IncidentDependencyGraph
			if json.Unmarshal(topologyRaw, &persisted) == nil && (len(persisted.Nodes) > 0 || len(persisted.Edges) > 0) {
				item.Features.TopologyGraph = persisted
			}
		}
		item.SourceScores = map[string]float64{source: score}
		out = append(out, item)
	}
	return out, rows.Err()
}

func serviceTopologyOverlap(current, historical []string) float64 {
	if len(current) == 0 || len(historical) == 0 {
		return 0
	}
	left := map[string]bool{}
	for _, value := range current {
		left[value] = true
	}
	matched := 0
	seen := map[string]bool{}
	for _, value := range historical {
		if left[value] && !seen[value] {
			matched++
			seen[value] = true
		}
	}
	return float64(matched) / float64(len(left))
}

func (s *PostgresStore) SeedCausalPatterns(ctx context.Context, patterns []domain.CausalPattern) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	for _, pattern := range patterns {
		nodes, _ := json.Marshal(pattern.Nodes)
		edges, _ := json.Marshal(pattern.Edges)
		_, err = tx.Exec(ctx, `INSERT INTO causal_patterns(id,category,cause,nodes,edges,source,confidence,status,version,support_count)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(id) DO UPDATE SET category=EXCLUDED.category,cause=EXCLUDED.cause,nodes=EXCLUDED.nodes,edges=EXCLUDED.edges,source=EXCLUDED.source,confidence=EXCLUDED.confidence,version=GREATEST(causal_patterns.version,EXCLUDED.version),updated_at=NOW()`, pattern.ID, pattern.Category, pattern.Cause, nodes, edges, pattern.Source, pattern.Confidence, pattern.Status, pattern.Version, pattern.SupportCount)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) ListCausalPatterns(ctx context.Context, status string) ([]domain.CausalPattern, error) {
	query := `SELECT id,category,cause,nodes,edges,source,confidence,status,version,support_count,created_at,updated_at FROM causal_patterns`
	args := []any{}
	if status != "" {
		query += ` WHERE status=$1`
		args = append(args, status)
	}
	query += ` ORDER BY category,id`
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.CausalPattern
	for rows.Next() {
		pattern, scanErr := scanPattern(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, *pattern)
	}
	return out, rows.Err()
}

func (s *PostgresStore) GetCausalPattern(ctx context.Context, id string) (*domain.CausalPattern, error) {
	row := s.pool.QueryRow(ctx, `SELECT id,category,cause,nodes,edges,source,confidence,status,version,support_count,created_at,updated_at FROM causal_patterns WHERE id=$1`, id)
	pattern, err := scanPattern(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return pattern, err
}

type patternRow interface{ Scan(...any) error }

func scanPattern(row patternRow) (*domain.CausalPattern, error) {
	var p domain.CausalPattern
	var nodes, edges []byte
	if err := row.Scan(&p.ID, &p.Category, &p.Cause, &nodes, &edges, &p.Source, &p.Confidence, &p.Status, &p.Version, &p.SupportCount, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(nodes, &p.Nodes); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(edges, &p.Edges); err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *PostgresStore) SetCausalPatternStatus(ctx context.Context, id, status, operator string) (*domain.CausalPattern, error) {
	if status != "active" && status != "disabled" {
		return nil, fmt.Errorf("causal pattern status must be active or disabled")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	tag, err := tx.Exec(ctx, `UPDATE causal_patterns SET status=$2,version=version+1,updated_at=NOW() WHERE id=$1`, id, status)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	payload, _ := json.Marshal(map[string]any{"operator": operator, "status": status})
	_, err = tx.Exec(ctx, `INSERT INTO causal_pattern_events(id,pattern_id,event_type,reason,payload) VALUES($1,$2,'status_changed',$3,$4)`, ulid.Make().String(), id, "status updated through authenticated API", payload)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetCausalPattern(ctx, id)
}

func (s *PostgresStore) RecordCausalPatternEvent(ctx context.Context, patternID, incidentID, eventType, reason string, payload map[string]any) error {
	raw, _ := json.Marshal(payload)
	if eventType != "incident_support" {
		_, err := s.pool.Exec(ctx, `INSERT INTO causal_pattern_events(id,pattern_id,incident_id,event_type,reason,payload) VALUES($1,$2,NULLIF($3,''),$4,$5,$6)`, ulid.Make().String(), patternID, incidentID, eventType, reason, raw)
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err = tx.Exec(ctx, `INSERT INTO causal_pattern_events(id,pattern_id,incident_id,event_type,reason,payload) VALUES($1,$2,NULLIF($3,''),$4,$5,$6) ON CONFLICT DO NOTHING`, ulid.Make().String(), patternID, incidentID, eventType, reason, raw); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE causal_patterns SET support_count=(SELECT COUNT(DISTINCT incident_id) FROM causal_pattern_events WHERE pattern_id=$1 AND event_type='incident_support' AND incident_id IS NOT NULL),updated_at=NOW() WHERE id=$1`, patternID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) CountCausalPatternSupport(ctx context.Context, patternID string) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx, `SELECT COUNT(DISTINCT incident_id) FROM causal_pattern_events WHERE pattern_id=$1 AND event_type='incident_support' AND incident_id IS NOT NULL`, patternID).Scan(&count)
	return count, err
}

var _ KnowledgeStore = (*PostgresStore)(nil)
