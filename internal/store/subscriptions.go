package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/neuco-ai/neuco/internal/domain"
)

// GetSubscriptionByOrgID returns the subscription for an org.
func (s *Store) GetSubscriptionByOrgID(ctx context.Context, orgID uuid.UUID) (domain.Subscription, error) {
	const q = `
		SELECT id, org_id, stripe_customer_id, stripe_subscription_id,
		       plan_tier, status, current_period_end, created_at, updated_at
		FROM   subscriptions
		WHERE  org_id = $1`

	row := s.pool.QueryRow(ctx, q, orgID)
	return scanSubscription(row)
}

// GetSubscriptionByStripeCustomerID looks up a subscription by Stripe customer ID.
func (s *Store) GetSubscriptionByStripeCustomerID(ctx context.Context, customerID string) (domain.Subscription, error) {
	const q = `
		SELECT id, org_id, stripe_customer_id, stripe_subscription_id,
		       plan_tier, status, current_period_end, created_at, updated_at
		FROM   subscriptions
		WHERE  stripe_customer_id = $1`

	row := s.pool.QueryRow(ctx, q, customerID)
	return scanSubscription(row)
}

// UpsertSubscription creates or updates a subscription for an org.
func (s *Store) UpsertSubscription(ctx context.Context, sub domain.Subscription) (domain.Subscription, error) {
	const q = `
		INSERT INTO subscriptions (org_id, stripe_customer_id, stripe_subscription_id, plan_tier, status, current_period_end)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (org_id) DO UPDATE SET
			stripe_customer_id     = EXCLUDED.stripe_customer_id,
			stripe_subscription_id = EXCLUDED.stripe_subscription_id,
			plan_tier              = EXCLUDED.plan_tier,
			status                 = EXCLUDED.status,
			current_period_end     = EXCLUDED.current_period_end
		RETURNING id, org_id, stripe_customer_id, stripe_subscription_id,
		          plan_tier, status, current_period_end, created_at, updated_at`

	row := s.pool.QueryRow(ctx, q,
		sub.OrgID, sub.StripeCustomerID, sub.StripeSubscriptionID,
		sub.PlanTier, sub.Status, sub.CurrentPeriodEnd,
	)
	result, err := scanSubscription(row)
	if err != nil {
		return domain.Subscription{}, fmt.Errorf("store.UpsertSubscription: %w", err)
	}
	return result, nil
}


func scanSubscription(row pgx.Row) (domain.Subscription, error) {
	var sub domain.Subscription
	err := row.Scan(
		&sub.ID, &sub.OrgID, &sub.StripeCustomerID, &sub.StripeSubscriptionID,
		&sub.PlanTier, &sub.Status, &sub.CurrentPeriodEnd,
		&sub.CreatedAt, &sub.UpdatedAt,
	)
	if err != nil {
		return domain.Subscription{}, err
	}
	return sub, nil
}
