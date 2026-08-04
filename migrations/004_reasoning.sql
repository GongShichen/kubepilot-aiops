CREATE TABLE IF NOT EXISTS incident_knowledge (
    incident_id TEXT PRIMARY KEY REFERENCES incidents(id) ON DELETE CASCADE,
    namespace TEXT NOT NULL,
    service TEXT NOT NULL,
    resource TEXT NOT NULL DEFAULT '',
    category TEXT NOT NULL DEFAULT '',
    root_cause TEXT NOT NULL DEFAULT '',
    observations JSONB NOT NULL DEFAULT '{}'::jsonb,
    features JSONB NOT NULL DEFAULT '{}'::jsonb,
    topology JSONB NOT NULL DEFAULT '[]'::jsonb,
    search_vector TSVECTOR NOT NULL,
    embedding_version TEXT NOT NULL DEFAULT '',
    resolved_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS incident_knowledge_search_idx ON incident_knowledge USING GIN(search_vector);
CREATE INDEX IF NOT EXISTS incident_knowledge_namespace_idx ON incident_knowledge(namespace, resolved_at DESC);

CREATE TABLE IF NOT EXISTS causal_patterns (
    id TEXT PRIMARY KEY,
    category TEXT NOT NULL,
    cause TEXT NOT NULL,
    nodes JSONB NOT NULL,
    edges JSONB NOT NULL,
    source TEXT NOT NULL,
    confidence DOUBLE PRECISION NOT NULL CHECK(confidence >= 0 AND confidence <= 1),
    status TEXT NOT NULL CHECK(status IN ('active','disabled','candidate')),
    version INTEGER NOT NULL DEFAULT 1,
    support_count INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS causal_patterns_status_category_idx ON causal_patterns(status, category);

CREATE TABLE IF NOT EXISTS causal_pattern_events (
    id TEXT PRIMARY KEY,
    pattern_id TEXT NOT NULL REFERENCES causal_patterns(id) ON DELETE CASCADE,
    incident_id TEXT REFERENCES incidents(id) ON DELETE SET NULL,
    event_type TEXT NOT NULL,
    reason TEXT NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS causal_pattern_events_pattern_idx ON causal_pattern_events(pattern_id, created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS causal_pattern_incident_support_idx ON causal_pattern_events(pattern_id, incident_id, event_type)
WHERE incident_id IS NOT NULL AND event_type = 'incident_support';

UPDATE agent_workflows
SET status='NEEDS_ATTENTION', last_error='incomplete legacy Eino Graph workflow requires explicit retry', completed_at=NOW()
WHERE graph_version='eino-incident-v2'
  AND status NOT IN ('RESOLVED','REJECTED','RECOVERY_FAILED','CANCELLED','NEEDS_ATTENTION');
