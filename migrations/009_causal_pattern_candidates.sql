CREATE TABLE IF NOT EXISTS causal_pattern_candidates (
    id TEXT PRIMARY KEY,
    pattern_id TEXT NOT NULL UNIQUE,
    causal_path JSONB NOT NULL,
    supporting_incidents JSONB NOT NULL DEFAULT '[]'::jsonb,
    support_count INTEGER NOT NULL DEFAULT 0 CHECK (support_count >= 0),
    coverage DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK (coverage >= 0 AND coverage <= 1),
    evidence_confidence DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK (evidence_confidence >= 0 AND evidence_confidence <= 1),
    causal_consistency DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK (causal_consistency >= 0 AND causal_consistency <= 1),
    contradiction_penalty DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK (contradiction_penalty >= 0 AND contradiction_penalty <= 1),
    confidence DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK (confidence >= 0 AND confidence <= 1),
    contradictions JSONB NOT NULL DEFAULT '[]'::jsonb,
    status TEXT NOT NULL CHECK (status IN ('DISCOVERED','VALIDATING','ACCEPTED','REJECTED')),
    explanation TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS causal_pattern_candidates_status_idx ON causal_pattern_candidates(status, confidence DESC, updated_at DESC);
ALTER TABLE causal_pattern_candidates ADD COLUMN IF NOT EXISTS causal_consistency DOUBLE PRECISION NOT NULL DEFAULT 0;
ALTER TABLE causal_pattern_candidates ADD COLUMN IF NOT EXISTS contradiction_penalty DOUBLE PRECISION NOT NULL DEFAULT 0;
