-- =============================================================================
-- Migration: 000015_production_indexes.down.sql
-- Description: Remove production indexes and statement_timeout.
-- =============================================================================

ALTER DATABASE CURRENT RESET statement_timeout;

DROP INDEX IF EXISTS idx_generations_project;
DROP INDEX IF EXISTS idx_copilot_notes_active;
DROP INDEX IF EXISTS idx_integrations_provider_active;
DROP INDEX IF EXISTS idx_specs_candidate_version;
DROP INDEX IF EXISTS idx_pipeline_runs_project_status;
DROP INDEX IF EXISTS idx_signals_unembedded;
DROP INDEX IF EXISTS idx_signals_project_occurred_at;
DROP INDEX IF EXISTS idx_candidates_project_score;
DROP INDEX IF EXISTS idx_projects_created_by;
DROP INDEX IF EXISTS idx_projects_org_name;
DROP INDEX IF EXISTS idx_org_members_user_id;
DROP INDEX IF EXISTS idx_users_email;
