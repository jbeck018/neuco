package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/neuco-ai/neuco/internal/domain"
)

// currentPeriodStart returns the first day of the current month as the default
// billing period start. For orgs with Stripe subscriptions, the actual period
// start can be derived from the subscription's current_period_end minus one month.
func currentPeriodStart() time.Time {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// GetOrCreateUsage returns the usage record for the current billing period,
// creating it if it doesn't exist yet.
func (s *Store) GetOrCreateUsage(ctx context.Context, orgID uuid.UUID) (domain.OrgUsage, error) {
	periodStart := currentPeriodStart()
	const q = `
		INSERT INTO org_usage (org_id, period_start)
		VALUES ($1, $2)
		ON CONFLICT (org_id, period_start) DO UPDATE SET org_id = EXCLUDED.org_id
		RETURNING id, org_id, period_start, signals_count, prs_count, created_at, updated_at`

	row := s.pool.QueryRow(ctx, q, orgID, periodStart)
	return scanUsage(row)
}

// IncrementSignalUsage atomically increments the signal counter for the current period.
func (s *Store) IncrementSignalUsage(ctx context.Context, orgID uuid.UUID, count int) error {
	periodStart := currentPeriodStart()
	const q = `
		INSERT INTO org_usage (org_id, period_start, signals_count)
		VALUES ($1, $2, $3)
		ON CONFLICT (org_id, period_start) DO UPDATE
		SET signals_count = org_usage.signals_count + $3`

	_, err := s.pool.Exec(ctx, q, orgID, periodStart, count)
	if err != nil {
		return fmt.Errorf("store.IncrementSignalUsage: %w", err)
	}
	return nil
}

// IncrementPRUsage atomically increments the PR counter for the current period.
func (s *Store) IncrementPRUsage(ctx context.Context, orgID uuid.UUID) error {
	periodStart := currentPeriodStart()
	const q = `
		INSERT INTO org_usage (org_id, period_start, prs_count)
		VALUES ($1, $2, 1)
		ON CONFLICT (org_id, period_start) DO UPDATE
		SET prs_count = org_usage.prs_count + 1`

	_, err := s.pool.Exec(ctx, q, orgID, periodStart)
	if err != nil {
		return fmt.Errorf("store.IncrementPRUsage: %w", err)
	}
	return nil
}

// CountOrgProjects returns the number of projects belonging to an org.
func (s *Store) CountOrgProjects(ctx context.Context, orgID uuid.UUID) (int, error) {
	const q = `SELECT COUNT(*) FROM projects WHERE org_id = $1`
	var count int
	if err := s.pool.QueryRow(ctx, q, orgID).Scan(&count); err != nil {
		return 0, fmt.Errorf("store.CountOrgProjects: %w", err)
	}
	return count, nil
}

func scanUsage(row pgx.Row) (domain.OrgUsage, error) {
	var u domain.OrgUsage
	err := row.Scan(
		&u.ID, &u.OrgID, &u.PeriodStart, &u.SignalsCount, &u.PRsCount,
		&u.CreatedAt, &u.UpdatedAt,
	)
	if err != nil {
		return domain.OrgUsage{}, err
	}
	return u, nil
}
