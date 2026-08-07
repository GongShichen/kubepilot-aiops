CREATE TABLE IF NOT EXISTS incident_investigations (
    incident_id TEXT PRIMARY KEY REFERENCES incidents(id) ON DELETE CASCADE,
    architecture TEXT NOT NULL,
    plan JSONB NOT NULL DEFAULT '{}'::jsonb,
    findings JSONB NOT NULL DEFAULT '[]'::jsonb,
    debate JSONB NOT NULL DEFAULT '[]'::jsonb,
    arbitration JSONB,
    diagnostic_intelligence JSONB NOT NULL DEFAULT '{}'::jsonb,
    started_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
ALTER TABLE incident_investigations ADD COLUMN IF NOT EXISTS diagnostic_intelligence JSONB NOT NULL DEFAULT '{}'::jsonb;

CREATE TABLE IF NOT EXISTS memory_access_events (
    id BIGSERIAL PRIMARY KEY,
    incident_id TEXT NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
    agent TEXT NOT NULL,
    memory_kind TEXT NOT NULL CHECK(memory_kind IN ('working','episodic','semantic','procedural')),
    cluster_scope TEXT NOT NULL DEFAULT '',
    namespace_scope TEXT NOT NULL,
    query_hash TEXT NOT NULL,
    result_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    results JSONB NOT NULL DEFAULT '[]'::jsonb,
    policy_version TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL
);
ALTER TABLE memory_access_events ADD COLUMN IF NOT EXISTS results JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE memory_access_events ADD COLUMN IF NOT EXISTS policy_version TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS memory_access_incident_idx ON memory_access_events(incident_id, created_at);

CREATE TABLE IF NOT EXISTS model_usage_events (
    id BIGSERIAL PRIMARY KEY,
    incident_id TEXT NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
    agent TEXT NOT NULL,
    parent_agent TEXT NOT NULL DEFAULT '',
    phase TEXT NOT NULL DEFAULT 'diagnosis',
    input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens INTEGER NOT NULL DEFAULT 0,
    duration_ms DOUBLE PRECISION NOT NULL DEFAULT 0,
    estimated_cost DOUBLE PRECISION NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL
);
ALTER TABLE model_usage_events ADD COLUMN IF NOT EXISTS parent_agent TEXT NOT NULL DEFAULT '';
ALTER TABLE model_usage_events ADD COLUMN IF NOT EXISTS phase TEXT NOT NULL DEFAULT 'diagnosis';
CREATE INDEX IF NOT EXISTS model_usage_incident_idx ON model_usage_events(incident_id, created_at);
CREATE UNIQUE INDEX IF NOT EXISTS model_usage_identity_idx ON model_usage_events(incident_id, agent, created_at);

ALTER TABLE incident_knowledge ADD COLUMN IF NOT EXISTS cluster_scope TEXT NOT NULL DEFAULT '';
DROP INDEX IF EXISTS incident_knowledge_namespace_idx;
CREATE INDEX IF NOT EXISTS incident_knowledge_scope_idx ON incident_knowledge(cluster_scope, namespace, resolved_at DESC);

ALTER TABLE causal_patterns DROP CONSTRAINT IF EXISTS causal_patterns_status_check;
ALTER TABLE causal_patterns ADD CONSTRAINT causal_patterns_status_check CHECK(status IN ('candidate','validating','active','rejected','disabled'));
ALTER TABLE causal_patterns ADD COLUMN IF NOT EXISTS supporting_evidence JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE causal_patterns ADD COLUMN IF NOT EXISTS contradicting_evidence JSONB NOT NULL DEFAULT '[]'::jsonb;
-- Preserve conjunction-based causal admission requirements as structured graph
-- node IDs. They must survive the database round trip intact.
ALTER TABLE causal_patterns ADD COLUMN IF NOT EXISTS required_admission_node_ids JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE causal_patterns ADD COLUMN IF NOT EXISTS source_incidents JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE causal_patterns ADD COLUMN IF NOT EXISTS cluster_scope TEXT NOT NULL DEFAULT '';
ALTER TABLE causal_patterns ADD COLUMN IF NOT EXISTS namespace_scope TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS causal_patterns_scope_idx ON causal_patterns(cluster_scope, namespace_scope, status);

CREATE TABLE IF NOT EXISTS causal_pattern_revisions (
    pattern_id TEXT NOT NULL REFERENCES causal_patterns(id) ON DELETE CASCADE,
    revision INTEGER NOT NULL,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY(pattern_id, revision)
);

DO $$
BEGIN
    IF to_regclass('public.evolving_causal_patterns') IS NOT NULL THEN
        EXECUTE $migration$
            INSERT INTO causal_patterns(id,category,cause,nodes,edges,required_admission_node_ids,supporting_evidence,contradicting_evidence,source_incidents,cluster_scope,namespace_scope,source,confidence,status,version,support_count,created_at,updated_at)
            SELECT pattern_id,
                   COALESCE(pattern->>'category',''),
                   COALESCE(pattern->>'cause',''),
                   COALESCE(pattern->'causal_graph'->'nodes','[]'::jsonb),
                   COALESCE(pattern->'causal_graph'->'edges','[]'::jsonb),
                   COALESCE(pattern->'required_admission_node_ids','[]'::jsonb),
                   COALESCE(pattern->'supporting_evidence','[]'::jsonb),
                   COALESCE(pattern->'contradicting_evidence','[]'::jsonb),
                   source_incidents,
                   COALESCE(pattern->>'cluster',''),
                   COALESCE(pattern->>'namespace',''),
                   'learned',
                   confidence,
                   CASE status WHEN 'pending' THEN 'validating' ELSE status END,
                   1,
                   jsonb_array_length(source_incidents),
                   created_at,
                   updated_at
            FROM evolving_causal_patterns
            ON CONFLICT(id) DO NOTHING
        $migration$;
    END IF;
END
$$;

DROP TABLE IF EXISTS evolving_causal_patterns;

INSERT INTO causal_pattern_revisions(pattern_id,revision,payload)
SELECT id,
       version,
       jsonb_build_object(
           'id', id,
           'category', category,
           'cause', cause,
           'nodes', nodes,
           'edges', edges,
           'required_admission_node_ids', required_admission_node_ids,
           'supporting_evidence', supporting_evidence,
           'contradicting_evidence', contradicting_evidence,
           'source_incidents', source_incidents,
           'cluster', cluster_scope,
           'namespace', namespace_scope,
           'source', source,
           'confidence', confidence,
           'status', status,
           'version', version,
           'support_count', support_count,
           'created_at', created_at,
           'updated_at', updated_at
       )
FROM causal_patterns
ON CONFLICT(pattern_id,revision) DO NOTHING;

ALTER TABLE agent_workflows ADD COLUMN IF NOT EXISTS strategy_id TEXT NOT NULL DEFAULT 'kubepilot';
ALTER TABLE agent_workflows ADD COLUMN IF NOT EXISTS architecture TEXT NOT NULL DEFAULT 'constrained-react';
UPDATE agent_workflows
SET status='NEEDS_ATTENTION',
    last_error='workflow architecture changed; explicit retry is required',
    completed_at=NOW()
WHERE graph_version <> 'eino-cognitive-diagnosis-runtime'
  AND status NOT IN ('RESOLVED','REJECTED','RECOVERY_FAILED','CANCELLED','NEEDS_ATTENTION');

ALTER TABLE benchmark_case_results ADD COLUMN IF NOT EXISTS strategy_id TEXT NOT NULL DEFAULT 'kubepilot';
ALTER TABLE benchmark_case_results ADD COLUMN IF NOT EXISTS seed BIGINT NOT NULL DEFAULT 0;
ALTER TABLE benchmark_case_results ADD COLUMN IF NOT EXISTS repetition INTEGER NOT NULL DEFAULT 1;
ALTER TABLE benchmark_case_results DROP CONSTRAINT IF EXISTS benchmark_case_results_pkey;
ALTER TABLE benchmark_case_results ADD PRIMARY KEY(run_id, strategy_id, case_id, seed, repetition);

CREATE TABLE IF NOT EXISTS policy_versions (
    policy_id TEXT PRIMARY KEY,
    status TEXT NOT NULL CHECK(status IN ('candidate','shadow','active','retired','rejected')),
    policy JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    promoted_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS policy_evaluations (
    policy_id TEXT NOT NULL REFERENCES policy_versions(policy_id) ON DELETE CASCADE,
    run_id TEXT NOT NULL REFERENCES benchmark_runs(id) ON DELETE CASCADE,
    metrics JSONB NOT NULL,
    accepted BOOLEAN NOT NULL,
    reason TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY(policy_id, run_id)
);
