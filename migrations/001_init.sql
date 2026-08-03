CREATE TABLE IF NOT EXISTS incidents (
    id TEXT PRIMARY KEY,
    status TEXT NOT NULL,
    severity TEXT NOT NULL,
    service TEXT NOT NULL,
    namespace TEXT NOT NULL,
    resource TEXT NOT NULL DEFAULT '',
    summary TEXT NOT NULL DEFAULT '',
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS alerts (
    fingerprint TEXT NOT NULL,
    incident_id TEXT NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
    status TEXT NOT NULL,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (fingerprint, incident_id)
);

CREATE TABLE IF NOT EXISTS audit_events (
    id TEXT PRIMARY KEY,
    incident_id TEXT NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
    type TEXT NOT NULL,
    message TEXT NOT NULL,
    data JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS benchmark_runs (
    id TEXT PRIMARY KEY,
    profile TEXT NOT NULL,
    status TEXT NOT NULL,
    manifest JSONB NOT NULL,
    summary JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS benchmark_case_results (
    run_id TEXT NOT NULL REFERENCES benchmark_runs(id) ON DELETE CASCADE,
    case_id TEXT NOT NULL,
    status TEXT NOT NULL,
    result JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY(run_id, case_id)
);

CREATE TABLE IF NOT EXISTS correlation_results (
    run_id TEXT NOT NULL REFERENCES benchmark_runs(id) ON DELETE CASCADE,
    group_id TEXT NOT NULL,
    result JSONB NOT NULL,
    PRIMARY KEY(run_id, group_id)
);

CREATE TABLE IF NOT EXISTS retrieval_results (
    run_id TEXT NOT NULL REFERENCES benchmark_runs(id) ON DELETE CASCADE,
    query_id TEXT NOT NULL,
    result JSONB NOT NULL,
    PRIMARY KEY(run_id, query_id)
);

CREATE INDEX IF NOT EXISTS incidents_status_idx ON incidents(status);
CREATE INDEX IF NOT EXISTS incidents_created_idx ON incidents(created_at DESC);
CREATE INDEX IF NOT EXISTS alerts_fingerprint_idx ON alerts(fingerprint);
