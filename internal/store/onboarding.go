package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/neuco-ai/neuco/internal/domain"
)

// GetOnboarding returns the onboarding record for a user.
// If no record exists, it returns a zero-value struct with empty steps.
func (s *Store) GetOnboarding(ctx context.Context, userID uuid.UUID) (domain.UserOnboarding, error) {
	const q = `
		SELECT user_id, completed_steps, completed_at, created_at, updated_at
		FROM   user_onboarding
		WHERE  user_id = $1`

	row := s.pool.QueryRow(ctx, q, userID)
	ob, err := scanOnboarding(row)
	if err == pgx.ErrNoRows {
		return domain.UserOnboarding{
			UserID:         userID,
			CompletedSteps: []domain.OnboardingStep{},
		}, nil
	}
	if err != nil {
		return domain.UserOnboarding{}, fmt.Errorf("store.GetOnboarding: %w", err)
	}
	return ob, nil
}

// CompleteOnboardingStep marks a single step as completed.
func (s *Store) CompleteOnboardingStep(ctx context.Context, userID uuid.UUID, step domain.OnboardingStep) (domain.UserOnboarding, error) {
	const q = `
		INSERT INTO user_onboarding (user_id, completed_steps)
		VALUES ($1, ARRAY[$2::text])
		ON CONFLICT (user_id) DO UPDATE
		SET completed_steps = (
			SELECT array_agg(DISTINCT elem)
			FROM unnest(user_onboarding.completed_steps || ARRAY[$2::text]) AS elem
		)
		RETURNING user_id, completed_steps, completed_at, created_at, updated_at`

	row := s.pool.QueryRow(ctx, q, userID, string(step))
	ob, err := scanOnboarding(row)
	if err != nil {
		return domain.UserOnboarding{}, fmt.Errorf("store.CompleteOnboardingStep: %w", err)
	}
	return ob, nil
}

// CompleteOnboarding marks the full onboarding as done.
func (s *Store) CompleteOnboarding(ctx context.Context, userID uuid.UUID) (domain.UserOnboarding, error) {
	now := time.Now().UTC()
	const q = `
		INSERT INTO user_onboarding (user_id, completed_at)
		VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE
		SET completed_at = $2
		RETURNING user_id, completed_steps, completed_at, created_at, updated_at`

	row := s.pool.QueryRow(ctx, q, userID, now)
	ob, err := scanOnboarding(row)
	if err != nil {
		return domain.UserOnboarding{}, fmt.Errorf("store.CompleteOnboarding: %w", err)
	}
	return ob, nil
}

func scanOnboarding(row pgx.Row) (domain.UserOnboarding, error) {
	var ob domain.UserOnboarding
	var steps []string
	err := row.Scan(&ob.UserID, &steps, &ob.CompletedAt, &ob.CreatedAt, &ob.UpdatedAt)
	if err != nil {
		return domain.UserOnboarding{}, err
	}
	ob.CompletedSteps = make([]domain.OnboardingStep, len(steps))
	for i, s := range steps {
		ob.CompletedSteps[i] = domain.OnboardingStep(s)
	}
	return ob, nil
}
