CREATE TABLE llm_calls (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    pipeline_run_id UUID        REFERENCES pipeline_runs(id) ON DELETE SET NULL,
    pipeline_task_id UUID       REFERENCES pipeline_tasks(id) ON DELETE SET NULL,
    provider        TEXT        NOT NULL,  -- 'anthropic', 'openai'
    model           TEXT        NOT NULL,
    call_type       TEXT        NOT NULL,  -- 'spec_gen', 'codegen', 'theme_naming', 'copilot_review', 'embedding'
    tokens_in       INT         NOT NULL DEFAULT 0,
    tokens_out      INT         NOT NULL DEFAULT 0,
    latency_ms      INT         NOT NULL DEFAULT 0,
    cost_usd        NUMERIC(10,6) NOT NULL DEFAULT 0,
    error_msg       TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_llm_calls_project_id ON llm_calls(project_id);
CREATE INDEX idx_llm_calls_pipeline_run_id ON llm_calls(pipeline_run_id);
CREATE INDEX idx_llm_calls_created_at ON llm_calls(created_at);
CREATE INDEX idx_llm_calls_model ON llm_calls(model);
