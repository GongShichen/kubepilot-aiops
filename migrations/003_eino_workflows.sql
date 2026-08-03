CREATE TABLE IF NOT EXISTS agent_workflows (
    incident_id TEXT PRIMARY KEY REFERENCES incidents(id) ON DELETE CASCADE,
    graph_version TEXT NOT NULL,
    checkpoint_id TEXT,
    interrupt_id TEXT,
    model_protocol TEXT,
    model_name TEXT,
    model_config_hash TEXT,
    status TEXT NOT NULL,
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    interrupted_at TIMESTAMPTZ,
    resumed_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    last_error TEXT
);

CREATE TABLE IF NOT EXISTS log_templates (
    id TEXT PRIMARY KEY,
    namespace TEXT NOT NULL,
    service TEXT NOT NULL,
    template TEXT NOT NULL,
    cluster_id BIGINT,
    occurrence_count BIGINT NOT NULL DEFAULT 0,
    indexed_at TIMESTAMPTZ NOT NULL,
    metadata JSONB NOT NULL DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS agent_workflows_status_idx ON agent_workflows(status);
CREATE INDEX IF NOT EXISTS log_templates_scope_idx ON log_templates(namespace, service, indexed_at DESC);
