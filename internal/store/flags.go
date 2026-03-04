package store

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/neuco-ai/neuco/internal/domain"
)

const flagColumns = `key, enabled, description, metadata, updated_at, updated_by`

// GetFlag returns a single feature flag by its key.
func (s *Store) GetFlag(ctx context.Context, key string) (domain.FeatureFlag, error) {
	const q = `
		SELECT ` + flagColumns + `
		FROM   feature_flags
		WHERE  key = $1`

	row := s.pool.QueryRow(ctx, q, key)
	f, err := scanFlag(row)
	if err != nil {
		return domain.FeatureFlag{}, fmt.Errorf("store.GetFlag: %w", err)
	}
	return f, nil
}

// ListFlags returns all feature flags ordered by key.
func (s *Store) ListFlags(ctx context.Context) ([]domain.FeatureFlag, error) {
	const q = `
		SELECT ` + flagColumns + `
		FROM   feature_flags
		ORDER  BY key`

	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("store.ListFlags: %w", err)
	}
	defer rows.Close()

	var flags []domain.FeatureFlag
	for rows.Next() {
		f, err := scanFlag(rows)
		if err != nil {
			return nil, fmt.Errorf("store.ListFlags: scan: %w", err)
		}
		flags = append(flags, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store.ListFlags: rows: %w", err)
	}
	return flags, nil
}

// SetFlag updates the enabled status of a feature flag and records who made the change.
func (s *Store) SetFlag(ctx context.Context, key string, enabled bool, updatedBy *uuid.UUID) error {
	const q = `
		UPDATE feature_flags
		SET    enabled    = $2,
		       updated_by = $3,
		       updated_at = NOW()
		WHERE  key = $1`

	ct, err := s.pool.Exec(ctx, q, key, enabled, updatedBy)
	if err != nil {
		return fmt.Errorf("store.SetFlag: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("store.SetFlag: flag %q not found", key)
	}
	return nil
}

// IsFlagEnabled returns true if the flag is enabled. On any error (missing key,
// database failure, etc.) it returns false as a safe default.
func (s *Store) IsFlagEnabled(ctx context.Context, key string) bool {
	const q = `SELECT enabled FROM feature_flags WHERE key = $1`
	var enabled bool
	if err := s.pool.QueryRow(ctx, q, key).Scan(&enabled); err != nil {
		slog.Debug("IsFlagEnabled: defaulting to false",
			"key", key,
			"error", err,
		)
		return false
	}
	return enabled
}

// scanFlag reads a single FeatureFlag from any pgx row-like value.
func scanFlag(row pgx.Row) (domain.FeatureFlag, error) {
	var f domain.FeatureFlag
	var meta []byte
	err := row.Scan(
		&f.Key,
		&f.Enabled,
		&f.Description,
		&meta,
		&f.UpdatedAt,
		&f.UpdatedBy,
	)
	if err != nil {
		return domain.FeatureFlag{}, err
	}
	if meta != nil {
		f.Metadata = json.RawMessage(meta)
	} else {
		f.Metadata = json.RawMessage(`{}`)
	}
	return f, nil
}
