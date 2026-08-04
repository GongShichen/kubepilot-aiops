ALTER TABLE agent_workflows ADD COLUMN IF NOT EXISTS skill_snapshot_hash TEXT;
ALTER TABLE agent_workflows ADD COLUMN IF NOT EXISTS ranking_policy_hash TEXT;
ALTER TABLE agent_workflows ADD COLUMN IF NOT EXISTS reranker_config_hash TEXT;
ALTER TABLE agent_workflows ADD COLUMN IF NOT EXISTS budget_state JSONB NOT NULL DEFAULT '{}'::jsonb;

UPDATE agent_workflows
SET status='NEEDS_ATTENTION',
    last_error='incomplete legacy workflow requires explicit retry under constrained ReAct',
    completed_at=NOW()
WHERE graph_version IN ('eino-incident-v2','eino-incident-v3')
  AND status NOT IN ('RESOLVED','REJECTED','RECOVERY_FAILED','CANCELLED','NEEDS_ATTENTION');
