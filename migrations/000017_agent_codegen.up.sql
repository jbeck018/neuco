CREATE TABLE agent_configs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    project_id UUID REFERENCES projects(id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    encrypted_api_key BYTEA NOT NULL,
    model_override TEXT,
    extra_config JSONB NOT NULL DEFAULT '{}'::jsonb,
    is_default BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX idx_agent_configs_unique ON agent_configs (
    org_id,
    COALESCE(project_id, '00000000-0000-0000-0000-000000000000'::uuid),
    provider
);

CREATE TABLE sandbox_sessions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    generation_id UUID NOT NULL REFERENCES generations(id) ON DELETE CASCADE,
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    org_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    agent_provider TEXT NOT NULL,
    agent_model TEXT,
    sandbox_provider TEXT NOT NULL DEFAULT 'e2b',
    sandbox_external_id TEXT,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','provisioning','running','validating','completed','failed','cancelled','timed_out')),
    agent_log TEXT,
    files_changed JSONB NOT NULL DEFAULT '[]'::jsonb,
    test_results JSONB NOT NULL DEFAULT '{}'::jsonb,
    validation_results JSONB NOT NULL DEFAULT '{}'::jsonb,
    tokens_used INTEGER,
    cost_usd NUMERIC(10,6),
    retry_count INTEGER NOT NULL DEFAULT 0,
    max_retries INTEGER NOT NULL DEFAULT 3,
    error_message TEXT,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_sandbox_sessions_generation ON sandbox_sessions(generation_id);
CREATE INDEX idx_sandbox_sessions_project_status ON sandbox_sessions(project_id, status);
CREATE INDEX idx_agent_configs_org ON agent_configs(org_id);

ALTER TABLE generations
    ADD COLUMN sandbox_session_id UUID REFERENCES sandbox_sessions(id),
    ADD COLUMN agent_provider TEXT,
    ADD COLUMN agent_model TEXT;

