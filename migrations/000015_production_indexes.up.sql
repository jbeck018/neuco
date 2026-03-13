-- =============================================================================
-- Migration: 000015_production_indexes.up.sql
-- Description: Add missing composite indexes for production query patterns,
--              a statement_timeout safety net, and user email index.
-- =============================================================================

-- ---------------------------------------------------------------------------
-- 1. User authentication lookups
-- ---------------------------------------------------------------------------
CREATE INDEX IF NOT EXISTS idx_users_email ON users (email);

-- ---------------------------------------------------------------------------
-- 2. Org member reverse lookups (ListUserOrgs: JOIN org_members WHERE user_id)
-- ---------------------------------------------------------------------------
CREATE INDEX IF NOT EXISTS idx_org_members_user_id ON org_members (user_id);

-- ---------------------------------------------------------------------------
-- 3. Project listing by org (ListOrgProjects: WHERE org_id ORDER BY name)
-- ---------------------------------------------------------------------------
CREATE INDEX IF NOT EXISTS idx_projects_org_name ON projects (org_id, name);

-- ---------------------------------------------------------------------------
-- 4. Project created_by lookups (usage counting, analytics)
-- ---------------------------------------------------------------------------
CREATE INDEX IF NOT EXISTS idx_projects_created_by ON projects (created_by);

-- ---------------------------------------------------------------------------
-- 5. Feature candidate listing (WHERE project_id ORDER BY score DESC)
-- ---------------------------------------------------------------------------
CREATE INDEX IF NOT EXISTS idx_candidates_project_score
    ON feature_candidates (project_id, score DESC, suggested_at DESC);

-- ---------------------------------------------------------------------------
-- 6. Signal date-range filtering (analytics dashboards, ListProjectSignals)
-- ---------------------------------------------------------------------------
CREATE INDEX IF NOT EXISTS idx_signals_project_occurred_at
    ON signals (project_id, occurred_at DESC);

-- ---------------------------------------------------------------------------
-- 7. Unembedded signal lookups (embedding pipeline)
-- ---------------------------------------------------------------------------
CREATE INDEX IF NOT EXISTS idx_signals_unembedded
    ON signals (project_id, ingested_at)
    WHERE embedding IS NULL;

-- ---------------------------------------------------------------------------
-- 8. Pipeline runs by project+status (active pipeline queries)
-- ---------------------------------------------------------------------------
CREATE INDEX IF NOT EXISTS idx_pipeline_runs_project_status
    ON pipeline_runs (project_id, status);

-- ---------------------------------------------------------------------------
-- 9. Spec version lookups (GetSpecByCandidate, UpdateSpec locking)
-- ---------------------------------------------------------------------------
CREATE INDEX IF NOT EXISTS idx_specs_candidate_version
    ON specs (candidate_id, project_id, version DESC);

-- ---------------------------------------------------------------------------
-- 10. Integration sync worker (ListActiveIntegrationsInternal)
-- ---------------------------------------------------------------------------
CREATE INDEX IF NOT EXISTS idx_integrations_provider_active
    ON integrations (provider, is_active, created_at)
    WHERE is_active = true;

-- ---------------------------------------------------------------------------
-- 11. Copilot notes for org insights (GetOrgTopCopilotInsights)
-- ---------------------------------------------------------------------------
CREATE INDEX IF NOT EXISTS idx_copilot_notes_active
    ON copilot_notes (project_id, created_at DESC)
    WHERE dismissed = false;

-- ---------------------------------------------------------------------------
-- 12. Generation lookups by project (analytics, project stats)
-- ---------------------------------------------------------------------------
CREATE INDEX IF NOT EXISTS idx_generations_project
    ON generations (project_id, created_at DESC);

-- ---------------------------------------------------------------------------
-- 13. Statement timeout safety net (30s default for all connections)
-- Prevents runaway queries from holding connections indefinitely.
-- ---------------------------------------------------------------------------
ALTER DATABASE CURRENT SET statement_timeout = '30s';
