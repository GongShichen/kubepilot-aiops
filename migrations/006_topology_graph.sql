ALTER TABLE incident_knowledge
    ADD COLUMN IF NOT EXISTS topology_graph JSONB NOT NULL DEFAULT '{"nodes":[],"edges":[],"root_service":"","suspected_failure_nodes":[],"error_propagation_paths":[]}'::jsonb;

CREATE INDEX IF NOT EXISTS incident_knowledge_topology_graph_gin_idx
    ON incident_knowledge USING GIN(topology_graph);
