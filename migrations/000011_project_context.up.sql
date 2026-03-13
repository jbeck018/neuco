-- 000011: Project Context — cross-session context persistence
--
-- Stores accumulated project insights from synthesis runs. Each entry is an
-- atomic insight with a pgvector embedding for similarity search. The synthesis
-- pipeline reads prior context and appends new insights on every run.

CREATE TABLE project_contexts (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    category    TEXT NOT NULL DEFAULT 'insight',  -- insight, theme, decision, risk, opportunity
    title       TEXT NOT NULL,
    content     TEXT NOT NULL,
    source_run_id UUID REFERENCES pipeline_runs(id) ON DELETE SET NULL,
    metadata    JSONB NOT NULL DEFAULT '{}',
    embedding   vector(1536),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX project_contexts_project_idx ON project_contexts (project_id, created_at DESC);
CREATE INDEX project_contexts_category_idx ON project_contexts (project_id, category);
CREATE INDEX project_contexts_embedding_idx
    ON project_contexts USING hnsw (embedding vector_cosine_ops)
    WITH (ef_construction = 200, m = 16);

CREATE TRIGGER project_contexts_updated_at BEFORE UPDATE ON project_contexts
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();
