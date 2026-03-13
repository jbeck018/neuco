package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/neuco-ai/neuco/internal/domain"
)

// Columns matching the actual DB schema:
// id, project_id, provider, webhook_secret, config, last_sync_at, is_active, created_at
const integrationColumns = `
	id, project_id, provider, webhook_secret, config, last_sync_at, is_active, created_at`

func (s *Store) CreateIntegration(ctx context.Context, intg domain.Integration) (domain.Integration, error) {
	configJSON, err := json.Marshal(intg.Config)
	if err != nil {
		configJSON = []byte(`{}`)
	}

	const q = `
		INSERT INTO integrations (project_id, provider, webhook_secret, config)
		VALUES ($1, $2, $3, $4)
		RETURNING ` + integrationColumns

	row := s.pool.QueryRow(ctx, q,
		intg.ProjectID,
		intg.Provider,
		intg.WebhookSecret,
		configJSON,
	)
	out, err := scanIntegration(row)
	if err != nil {
		return domain.Integration{}, fmt.Errorf("store.CreateIntegration: %w", err)
	}
	return out, nil
}

func (s *Store) GetIntegration(ctx context.Context, projectID, integrationID uuid.UUID) (domain.Integration, error) {
	const q = `SELECT ` + integrationColumns + ` FROM integrations WHERE id = $1 AND project_id = $2`
	row := s.pool.QueryRow(ctx, q, integrationID, projectID)
	intg, err := scanIntegration(row)
	if err != nil {
		return domain.Integration{}, fmt.Errorf("store.GetIntegration: %w", err)
	}
	return intg, nil
}

func (s *Store) ListProjectIntegrations(ctx context.Context, projectID uuid.UUID) ([]domain.Integration, error) {
	const q = `SELECT ` + integrationColumns + ` FROM integrations WHERE project_id = $1 ORDER BY created_at`
	rows, err := s.pool.Query(ctx, q, projectID)
	if err != nil {
		return nil, fmt.Errorf("store.ListProjectIntegrations: %w", err)
	}
	defer rows.Close()

	var intgs []domain.Integration
	for rows.Next() {
		intg, err := scanIntegration(rows)
		if err != nil {
			return nil, fmt.Errorf("store.ListProjectIntegrations: scan: %w", err)
		}
		intgs = append(intgs, intg)
	}
	return intgs, rows.Err()
}

func (s *Store) DeleteIntegration(ctx context.Context, projectID, integrationID uuid.UUID) error {
	const q = `DELETE FROM integrations WHERE id = $1 AND project_id = $2`
	ct, err := s.pool.Exec(ctx, q, integrationID, projectID)
	if err != nil {
		return fmt.Errorf("store.DeleteIntegration: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("store.DeleteIntegration: not found")
	}
	return nil
}

func (s *Store) UpdateIntegrationLastSync(ctx context.Context, projectID, integrationID uuid.UUID, syncedAt time.Time) error {
	const q = `UPDATE integrations SET last_sync_at = $3 WHERE id = $1 AND project_id = $2`
	_, err := s.pool.Exec(ctx, q, integrationID, projectID, syncedAt)
	return err
}

func (s *Store) ValidateWebhookSecret(ctx context.Context, projectID, integrationID uuid.UUID, candidateSecret string) (bool, error) {
	const q = `SELECT webhook_secret FROM integrations WHERE id = $1 AND project_id = $2`
	var stored string
	if err := s.pool.QueryRow(ctx, q, integrationID, projectID).Scan(&stored); err != nil {
		return false, fmt.Errorf("store.ValidateWebhookSecret: %w", err)
	}
	return constantTimeEqual(stored, candidateSecret), nil
}

func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := 0; i < len(a); i++ {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

func scanIntegration(row pgx.Row) (domain.Integration, error) {
	var intg domain.Integration
	var configRaw []byte
	err := row.Scan(
		&intg.ID,
		&intg.ProjectID,
		&intg.Provider,
		&intg.WebhookSecret,
		&configRaw,
		&intg.LastSyncAt,
		&intg.IsActive,
		&intg.CreatedAt,
	)
	if err != nil {
		return domain.Integration{}, err
	}
	if configRaw != nil {
		if err := json.Unmarshal(configRaw, &intg.Config); err != nil {
			return domain.Integration{}, fmt.Errorf("unmarshal config: %w", err)
		}
	}
	return intg, nil
}
