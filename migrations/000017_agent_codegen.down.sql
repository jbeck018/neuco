ALTER TABLE generations
    DROP COLUMN IF EXISTS sandbox_session_id,
    DROP COLUMN IF EXISTS agent_provider,
    DROP COLUMN IF EXISTS agent_model;

DROP TABLE IF EXISTS sandbox_sessions;
DROP TABLE IF EXISTS agent_configs;
