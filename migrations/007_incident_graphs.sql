CREATE TABLE IF NOT EXISTS incident_graphs (
    incident_id TEXT PRIMARY KEY,
    graph JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS incident_graphs_graph_gin_idx
    ON incident_graphs USING GIN(graph);
