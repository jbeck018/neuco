-- =============================================================================
-- Migration: 000006_subscriptions.up.sql
-- Description: Add subscriptions table for Stripe billing integration
-- =============================================================================

CREATE TABLE subscriptions (
    id                      UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id                  UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    stripe_customer_id      TEXT        NOT NULL,
    stripe_subscription_id  TEXT        UNIQUE,
    plan_tier               TEXT        NOT NULL DEFAULT 'starter'
                                        CHECK (plan_tier IN ('starter', 'builder')),
    status                  TEXT        NOT NULL DEFAULT 'incomplete'
                                        CHECK (status IN ('active', 'past_due', 'canceled', 'incomplete', 'trialing')),
    current_period_end      TIMESTAMPTZ,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX subscriptions_org_idx ON subscriptions (org_id);
CREATE INDEX subscriptions_stripe_customer_idx ON subscriptions (stripe_customer_id);

CREATE TRIGGER subscriptions_updated_at
    BEFORE UPDATE ON subscriptions
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();
