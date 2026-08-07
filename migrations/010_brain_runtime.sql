CREATE TABLE IF NOT EXISTS workflow_attempts (
    id TEXT PRIMARY KEY,
    incident_id TEXT NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
    sequence INTEGER NOT NULL,
    checkpoint_id TEXT NOT NULL,
    status TEXT NOT NULL,
    execution_snapshot JSONB NOT NULL,
    evidence_snapshot_hash TEXT,
    migrated_from_attempt_id TEXT REFERENCES workflow_attempts(id) ON DELETE SET NULL,
    invalidated_artifact_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    started_at TIMESTAMPTZ NOT NULL,
    interrupted_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (incident_id, sequence)
);

CREATE INDEX IF NOT EXISTS workflow_attempts_incident_idx
    ON workflow_attempts(incident_id, sequence DESC);
CREATE INDEX IF NOT EXISTS workflow_attempts_status_idx
    ON workflow_attempts(status, updated_at DESC);
