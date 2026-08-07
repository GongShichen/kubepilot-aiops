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
func (s *PostgresStore) Close()                         { s.pool.Close() }
func (s *PostgresStore) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func (s *PostgresStore) RecordMemoryAccess(ctx context.Context, event domain.MemoryAccessEvent) error {
	resultIDs, err := json.Marshal(event.ResultIDs)
	if err != nil {
		return err
	}
	results, err := json.Marshal(event.Results)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO memory_access_events(incident_id,agent,memory_kind,cluster_scope,namespace_scope,query_hash,result_ids,results,policy_version,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, event.IncidentID, event.Agent, event.Kind, event.Scope.Cluster, event.Scope.Namespace, event.QueryHash, resultIDs, results, event.PolicyVersion, event.CreatedAt)
	return err
}

func (s *PostgresStore) RecordModelUsage(ctx context.Context, event domain.ModelUsageEvent) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO model_usage_events(incident_id,agent,parent_agent,phase,input_tokens,output_tokens,reasoning_tokens,duration_ms,estimated_cost,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, event.IncidentID, event.Agent, event.ParentAgent, event.Phase, event.InputTokens, event.OutputTokens, event.ReasoningTokens, event.DurationMS, event.EstimatedCost, event.CreatedAt)
	return err
}

func (s *PostgresStore) SaveBenchmarkRun(ctx context.Context, run domain.BenchmarkRun) error {
	payload, err := json.Marshal(run)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO benchmark_runs(id,profile,status,manifest,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(id) DO UPDATE SET profile=EXCLUDED.profile,status=EXCLUDED.status,manifest=EXCLUDED.manifest,updated_at=EXCLUDED.updated_at`, run.ID, run.Profile, run.Status, payload, run.CreatedAt, run.UpdatedAt)
	return err
}

func (s *PostgresStore) SaveBenchmarkCaseResult(ctx context.Context, result domain.BenchmarkCaseResult) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO benchmark_case_results(run_id,strategy_id,case_id,seed,repetition,status,result) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(run_id,strategy_id,case_id,seed,repetition) DO UPDATE SET status=EXCLUDED.status,result=EXCLUDED.result`, result.RunID, result.StrategyID, result.CaseID, result.Seed, result.Repetition, result.Status, result.Result)
	return err
}

func (s *PostgresStore) ListBenchmarkRuns(ctx context.Context) ([]domain.BenchmarkRun, error) {
	rows, err := s.pool.Query(ctx, `SELECT manifest FROM benchmark_runs ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []domain.BenchmarkRun
	for rows.Next() {
		var payload []byte
		var run domain.BenchmarkRun
		if err = rows.Scan(&payload); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(payload, &run); err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (s *PostgresStore) InterruptActiveBenchmarkRuns(ctx context.Context, interruptedAt time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE benchmark_runs SET status='interrupted',manifest=jsonb_set(jsonb_set(manifest,'{status}',to_jsonb('interrupted'::text),true),'{updated_at}',to_jsonb($1::timestamptz),true),updated_at=$1 WHERE status IN ('queued','running')`, interruptedAt)
	return err
}

func (s *PostgresStore) SavePolicyVersion(ctx context.Context, policy domain.PolicyVersion) error {
	payload, err := json.Marshal(policy)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO policy_versions(policy_id,status,policy,created_at,promoted_at) VALUES($1,$2,$3,$4,$5) ON CONFLICT(policy_id) DO UPDATE SET status=EXCLUDED.status,policy=EXCLUDED.policy,promoted_at=EXCLUDED.promoted_at`, policy.ID, policy.Status, payload, policy.CreatedAt, statusTime(!policy.PromotedAt.IsZero(), policy.PromotedAt))
	return err
}

func (s *PostgresStore) SavePolicyEvaluation(ctx context.Context, evaluation domain.PolicyEvaluation) error {
	metrics, err := json.Marshal(evaluation.Metrics)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO policy_evaluations(policy_id,run_id,metrics,accepted,reason,created_at) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(policy_id,run_id) DO UPDATE SET metrics=EXCLUDED.metrics,accepted=EXCLUDED.accepted,reason=EXCLUDED.reason,created_at=EXCLUDED.created_at`, evaluation.PolicyID, evaluation.RunID, metrics, evaluation.Accepted, evaluation.Reason, evaluation.CreatedAt)
	return err
}

// DeleteIncidents removes explicitly identified incidents. It is intended for
// administrative lifecycle cleanup; foreign-key cascades remove normalized
// evidence and incident knowledge. Agent tools never expose this method.
func (s *PostgresStore) DeleteIncidents(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM incidents WHERE id=ANY($1::text[])`, ids)
	return err
}

// ListResolvedIncidents is used only by the server-side Knowledge Evolution
// layer. It returns bounded, resolved payloads and never crosses the Agent Tool
// boundary.
func (s *PostgresStore) ListResolvedIncidents(ctx context.Context, namespaces []string, limit int) ([]*domain.Incident, error) {
	if len(namespaces) == 0 {
		return []*domain.Incident{}, nil
	}
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	rows, err := s.pool.Query(ctx, `SELECT payload FROM incidents WHERE status=$1 AND namespace=ANY($2::text[]) ORDER BY updated_at DESC LIMIT $3`, domain.StatusResolved, namespaces, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []*domain.Incident{}
	for rows.Next() {
		var raw []byte
		var incident domain.Incident
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &incident); err != nil {
			return nil, err
		}
		result = append(result, &incident)
	}
	return result, rows.Err()
}

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

// AppendAlert serializes alert correlation with workflow persistence. Alert
// delivery is asynchronous and can overlap a diagnosis graph finalizing its
// Investigation; a full Incident update from a stale correlation snapshot
// would otherwise erase the completed evidence and arbitration audit.
func (s *PostgresStore) AppendAlert(ctx context.Context, id string, alert domain.Alert, occurredAt time.Time) (*domain.Incident, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var raw []byte
	if err = tx.QueryRow(ctx, `SELECT payload FROM incidents WHERE id=$1 FOR UPDATE`, id).Scan(&raw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var incident domain.Incident
	if err = json.Unmarshal(raw, &incident); err != nil {
		return nil, err
	}
	incident.Alerts = append(incident.Alerts, alert)
	incident.UpdatedAt = occurredAt
	payload, err := json.Marshal(&incident)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE incidents SET status=$2,severity=$3,service=$4,namespace=$5,resource=$6,summary=$7,payload=$8,updated_at=$9 WHERE id=$1`, incident.ID, incident.Status, incident.Severity, incident.Service, incident.Namespace, incident.Resource, incident.Summary, payload, incident.UpdatedAt); err != nil {
		return nil, err
	}
	if err = syncIncidentRecords(ctx, tx, &incident); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &incident, nil
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

func (s *PostgresStore) WorkflowIdentity(ctx context.Context, incidentID string) (string, error) {
	var version string
	err := s.pool.QueryRow(ctx, `SELECT graph_version FROM agent_workflows WHERE incident_id=$1`, incidentID).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return version, err
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
		// Evidence IDs are stable content identities used by the diagnosis
		// runtime, ranking, and audit. The normalized evidence table is keyed
		// globally, however, so the database row must additionally be scoped to
		// its Incident. Identical current observations can legitimately occur in
		// several concurrent Incidents; persisting the bare evidence ID would
		// abort the entire Incident transaction and lose its investigation audit.
		id := evidenceRecordID(in.ID, evidence.ID, index)
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
	if in.DiagnosisLedger != nil {
		for index, decision := range in.DiagnosisLedger.AgentDecisions {
			raw, marshalErr := json.Marshal(decision)
			if marshalErr != nil {
				return marshalErr
			}
			id := fmt.Sprintf("%s-agent-decision-%06d", in.ID, index)
			if _, insertErr := tx.Exec(ctx, `INSERT INTO audit_events(id,incident_id,type,message,data,created_at) VALUES($1,$2,'agent_decision',$3,$4,$5) ON CONFLICT(id) DO UPDATE SET message=EXCLUDED.message,data=EXCLUDED.data,created_at=EXCLUDED.created_at`, id, in.ID, decision.ReasonCategory, raw, decision.OccurredAt); insertErr != nil {
				return insertErr
			}
		}
		for index, feedback := range in.DiagnosisLedger.SafetyFeedback {
			raw, marshalErr := json.Marshal(feedback)
			if marshalErr != nil {
				return marshalErr
			}
			id := fmt.Sprintf("%s-safety-feedback-%06d", in.ID, index)
			if _, insertErr := tx.Exec(ctx, `INSERT INTO audit_events(id,incident_id,type,message,data,created_at) VALUES($1,$2,'safety_feedback',$3,$4,$5) ON CONFLICT(id) DO UPDATE SET message=EXCLUDED.message,data=EXCLUDED.data`, id, in.ID, feedback.Code, raw, in.UpdatedAt); insertErr != nil {
				return insertErr
			}
		}
		for index, transition := range in.DiagnosisLedger.HypothesisTransitions {
			raw, marshalErr := json.Marshal(transition)
			if marshalErr != nil {
				return marshalErr
			}
			id := fmt.Sprintf("%s-hypothesis-transition-%06d", in.ID, index)
			if _, insertErr := tx.Exec(ctx, `INSERT INTO audit_events(id,incident_id,type,message,data,created_at) VALUES($1,$2,'hypothesis_transition',$3,$4,$5) ON CONFLICT(id) DO UPDATE SET message=EXCLUDED.message,data=EXCLUDED.data,created_at=EXCLUDED.created_at`, id, in.ID, string(transition.To), raw, transition.OccurredAt); insertErr != nil {
				return insertErr
			}
		}
		confidenceIndex := 0
		for _, verified := range in.DiagnosisLedger.Verified {
			for _, record := range verified.ConfidenceHistory {
				raw, marshalErr := json.Marshal(record)
				if marshalErr != nil {
					return marshalErr
				}
				id := fmt.Sprintf("%s-hypothesis-confidence-%06d", in.ID, confidenceIndex)
				confidenceIndex++
				if _, insertErr := tx.Exec(ctx, `INSERT INTO audit_events(id,incident_id,type,message,data,created_at) VALUES($1,$2,'hypothesis_confidence',$3,$4,$5) ON CONFLICT(id) DO UPDATE SET message=EXCLUDED.message,data=EXCLUDED.data,created_at=EXCLUDED.created_at`, id, in.ID, record.HypothesisID, raw, record.ComputedAt); insertErr != nil {
					return insertErr
				}
			}
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
	if in.Investigation != nil {
		plan, marshalErr := json.Marshal(in.Investigation.Plan)
		if marshalErr != nil {
			return marshalErr
		}
		findings, marshalErr := json.Marshal(in.Investigation.Findings)
		if marshalErr != nil {
			return marshalErr
		}
		debate, marshalErr := json.Marshal(in.Investigation.Debate)
		if marshalErr != nil {
			return marshalErr
		}
		arbitration, marshalErr := json.Marshal(in.Investigation.Arbitration)
		if marshalErr != nil {
			return marshalErr
		}
		intelligence, marshalErr := json.Marshal(diagnosticIntelligencePayload(in.Investigation))
		if marshalErr != nil {
			return marshalErr
		}
		if _, insertErr := tx.Exec(ctx, `INSERT INTO incident_investigations(incident_id,architecture,plan,findings,debate,arbitration,diagnostic_intelligence,started_at,completed_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,NOW()) ON CONFLICT(incident_id) DO UPDATE SET architecture=EXCLUDED.architecture,plan=EXCLUDED.plan,findings=EXCLUDED.findings,debate=EXCLUDED.debate,arbitration=EXCLUDED.arbitration,diagnostic_intelligence=EXCLUDED.diagnostic_intelligence,started_at=EXCLUDED.started_at,completed_at=EXCLUDED.completed_at,updated_at=NOW()`, in.ID, in.Investigation.Architecture, plan, findings, debate, arbitration, intelligence, in.Investigation.StartedAt, statusTime(!in.Investigation.CompletedAt.IsZero(), in.Investigation.CompletedAt)); insertErr != nil {
			return insertErr
		}
		for _, usage := range in.Investigation.ModelUsage {
			if _, insertErr := tx.Exec(ctx, `INSERT INTO model_usage_events(incident_id,agent,parent_agent,phase,input_tokens,output_tokens,reasoning_tokens,duration_ms,estimated_cost,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(incident_id,agent,created_at) DO NOTHING`, usage.IncidentID, usage.Agent, usage.ParentAgent, usage.Phase, usage.InputTokens, usage.OutputTokens, usage.ReasoningTokens, usage.DurationMS, usage.EstimatedCost, usage.CreatedAt); insertErr != nil {
				return insertErr
			}
		}
	}
	if in.WorkflowAttempt != nil {
		executionSnapshot, marshalErr := json.Marshal(in.WorkflowAttempt.ExecutionSnapshot)
		if marshalErr != nil {
			return marshalErr
		}
		invalidated, marshalErr := json.Marshal(in.WorkflowAttempt.InvalidatedArtifactIDs)
		if marshalErr != nil {
			return marshalErr
		}
		if _, insertErr := tx.Exec(ctx, `INSERT INTO workflow_attempts(id,incident_id,sequence,checkpoint_id,status,execution_snapshot,evidence_snapshot_hash,migrated_from_attempt_id,invalidated_artifact_ids,started_at,interrupted_at,completed_at,updated_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,NOW())
			ON CONFLICT(id) DO UPDATE SET status=EXCLUDED.status,execution_snapshot=EXCLUDED.execution_snapshot,evidence_snapshot_hash=EXCLUDED.evidence_snapshot_hash,invalidated_artifact_ids=EXCLUDED.invalidated_artifact_ids,interrupted_at=EXCLUDED.interrupted_at,completed_at=EXCLUDED.completed_at,updated_at=NOW()`,
			in.WorkflowAttempt.ID, in.ID, in.WorkflowAttempt.Sequence, in.WorkflowAttempt.CheckpointID, in.WorkflowAttempt.Status, executionSnapshot, nullString(in.WorkflowAttempt.EvidenceSnapshotHash), nullString(in.WorkflowAttempt.MigratedFromAttemptID), invalidated, in.WorkflowAttempt.StartedAt, statusTime(!in.WorkflowAttempt.InterruptedAt.IsZero(), in.WorkflowAttempt.InterruptedAt), statusTime(!in.WorkflowAttempt.CompletedAt.IsZero(), in.WorkflowAttempt.CompletedAt)); insertErr != nil {
			return insertErr
		}
	}
	budget, _ := json.Marshal(in.AgentBudget)
	architecture := "constrained-react"
	if in.Investigation != nil && in.Investigation.Architecture != "" {
		architecture = in.Investigation.Architecture
	}
	if _, err := tx.Exec(ctx, `INSERT INTO agent_workflows(incident_id,graph_version,strategy_id,architecture,checkpoint_id,interrupt_id,model_protocol,model_name,model_config_hash,skill_snapshot_hash,ranking_policy_hash,reranker_config_hash,budget_state,status,started_at,interrupted_at,resumed_at,completed_at,last_error)
		VALUES($1,$19,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
		ON CONFLICT(incident_id) DO UPDATE SET graph_version=EXCLUDED.graph_version,strategy_id=EXCLUDED.strategy_id,architecture=EXCLUDED.architecture,checkpoint_id=EXCLUDED.checkpoint_id,interrupt_id=EXCLUDED.interrupt_id,model_protocol=EXCLUDED.model_protocol,model_name=EXCLUDED.model_name,model_config_hash=EXCLUDED.model_config_hash,skill_snapshot_hash=EXCLUDED.skill_snapshot_hash,ranking_policy_hash=EXCLUDED.ranking_policy_hash,reranker_config_hash=EXCLUDED.reranker_config_hash,budget_state=EXCLUDED.budget_state,status=EXCLUDED.status,interrupted_at=COALESCE(agent_workflows.interrupted_at,EXCLUDED.interrupted_at),resumed_at=COALESCE(agent_workflows.resumed_at,EXCLUDED.resumed_at),completed_at=EXCLUDED.completed_at,last_error=EXCLUDED.last_error`,
		in.ID, in.DiagnosisMethod, architecture, "incident:"+in.ID, nullString(in.WorkflowInterruptID), nullString(in.ModelProtocol), nullString(in.ModelName), nullString(in.ModelConfigHash), nullString(in.SkillSnapshotHash), nullString(in.RankingPolicyHash), nullString(in.RerankerConfigHash), budget, in.Status, in.CreatedAt, statusTime(in.Status == domain.StatusAwaitingApproval, in.UpdatedAt), statusTime(in.Status == domain.StatusRecovering || in.Status == domain.StatusVerifying || in.Status == domain.StatusResolved || in.Status == domain.StatusRecoveryFailed, in.UpdatedAt), statusTime(terminalWorkflow(in.Status), in.UpdatedAt), nullString(in.DiagnosisError), domain.WorkflowRuntimeName); err != nil {
		return err
	}
	return nil
}

// diagnosticIntelligencePayload is the durable, API-facing audit projection
// for the diagnosis runtime.  The event tables remain the query-efficient
// source for analytics, but an Investigation export must be self-contained so
// a trace can be inspected or replayed without joining hidden side tables.
func diagnosticIntelligencePayload(investigation *domain.Investigation) map[string]any {
	if investigation == nil {
		return map[string]any{}
	}
	return map[string]any{
		"candidates":                   investigation.Candidates,
		"verified_hypotheses":          investigation.Verified,
		"signals":                      investigation.Signals,
		"state_assertions":             investigation.Assertions,
		"cognitive_reasoning":          investigation.CognitiveReasoning,
		"falsification":                investigation.Falsification,
		"pairwise_falsification":       investigation.Pairwise,
		"candidate_expansion_requests": investigation.ExpansionRequests,
		"recovery_permission":          investigation.RecoveryPermission,
		"memory_reads":                 investigation.MemoryReads,
		"model_usage":                  investigation.ModelUsage,
		"brain_turns":                  investigation.BrainTurns,
		"incident_understanding":       investigation.IncidentUnderstanding,
		"skill_activations":            investigation.SkillActivations,
		"tool_executions":              investigation.ToolExecutions,
		"agent_hypotheses":             investigation.AgentHypotheses,
		"hypothesis_admissions":        investigation.HypothesisAdmissions,
		"hypothesis_groundings":        investigation.HypothesisGroundings,
		"grounding_deltas":             investigation.GroundingDeltas,
		"belief_deltas":                investigation.BeliefDeltas,
		"reflections":                  investigation.Reflections,
		"agent_diagnosis":              investigation.AgentDiagnosis,
		"agent_recovery_plan":          investigation.AgentRecoveryPlan,
		"termination":                  investigation.Termination,
		"brain_budget":                 investigation.BrainBudget,
		"execution_snapshot":           investigation.ExecutionSnapshot,
		"workflow_attempt":             investigation.WorkflowAttempt,
	}
}

func hypothesisRecordID(incidentID, localID string) string {
	return incidentID + "-" + localID
}

func evidenceRecordID(incidentID, localID string, index int) string {
	if localID == "" {
		localID = fmt.Sprintf("evidence-%d", index)
	}
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
var _ WorkflowIdentityStore = (*PostgresStore)(nil)
var _ = errors.Is
