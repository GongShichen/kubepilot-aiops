CREATE TABLE IF NOT EXISTS topology_patterns (
    pattern_id TEXT PRIMARY KEY,
    nodes JSONB NOT NULL,
    edges JSONB NOT NULL,
    frequency INTEGER NOT NULL DEFAULT 1 CHECK (frequency >= 1),
    confidence DOUBLE PRECISION NOT NULL CHECK (confidence >= 0 AND confidence <= 1),
    source_incidents JSONB NOT NULL DEFAULT '[]'::jsonb,
    last_observed TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS topology_patterns_confidence_idx ON topology_patterns(confidence DESC, last_observed DESC);

CREATE TABLE IF NOT EXISTS evolving_causal_patterns (
    pattern_id TEXT PRIMARY KEY,
    pattern JSONB NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending','active','disabled')),
    confidence DOUBLE PRECISION NOT NULL CHECK (confidence >= 0 AND confidence <= 1),
    source_incidents JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS evolving_causal_patterns_status_idx ON evolving_causal_patterns(status, confidence DESC);
