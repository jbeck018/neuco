-- =============================================================================
-- Migration: 000007_usage_tracking.up.sql
-- Description: Add org_usage table for tracking monthly usage per billing period
-- =============================================================================

CREATE TABLE org_usage (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id          UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    period_start    DATE        NOT NULL,
    signals_count   INT         NOT NULL DEFAULT 0,
    prs_count       INT         NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE (org_id, period_start)
);

CREATE INDEX org_usage_org_period_idx ON org_usage (org_id, period_start);

CREATE TRIGGER org_usage_updated_at
    BEFORE UPDATE ON org_usage
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();
