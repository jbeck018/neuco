-- =============================================================================
-- Migration: 000008_user_onboarding.up.sql
-- Description: Track onboarding progress per user
-- =============================================================================

CREATE TABLE user_onboarding (
    user_id           UUID        PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    completed_steps   TEXT[]      NOT NULL DEFAULT '{}',
    completed_at      TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TRIGGER user_onboarding_updated_at
    BEFORE UPDATE ON user_onboarding
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();
