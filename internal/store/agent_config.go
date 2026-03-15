package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const agentConfigColumns = `
	id, org_id, project_id, provider, encrypted_api_key,
	model_override, extra_config, is_default, created_at, updated_at`

// AgentConfigRow maps to the agent_configs table.
type AgentConfigRow struct {
	ID              uuid.UUID       `db:"id"`
	OrgID           uuid.UUID       `db:"org_id"`
	ProjectID       *uuid.UUID      `db:"project_id"`
	Provider        string          `db:"provider"`
	EncryptedAPIKey []byte          `db:"encrypted_api_key"`
	ModelOverride   *string         `db:"model_override"`
	ExtraConfig     json.RawMessage `db:"extra_config"`
	IsDefault       bool            `db:"is_default"`
	CreatedAt       time.Time       `db:"created_at"`
	UpdatedAt       time.Time       `db:"updated_at"`
}

// GetAgentConfig returns a config for an org scoped to a specific project (or NULL project).
func (s *Store) GetAgentConfig(ctx context.Context, orgID uuid.UUID, projectID *uuid.UUID) (*AgentConfigRow, error) {
	const q = `
		SELECT ` + agentConfigColumns + `
		FROM   agent_configs
		WHERE  org_id = $1
		  AND  project_id IS NOT DISTINCT FROM $2`

	row := s.pool.QueryRow(ctx, q, orgID, projectID)
	cfg, err := scanAgentConfig(row)
	if err != nil {
		return nil, fmt.Errorf("store.GetAgentConfig: %w", err)
	}
	return &cfg, nil
}

// SetAgentConfig inserts or updates an agent config keyed by org/project/provider.
func (s *Store) SetAgentConfig(ctx context.Context, cfg AgentConfigRow) error {
	extra := cfg.ExtraConfig
	if extra == nil {
		extra = json.RawMessage(`{}`)
	}

	const q = `
		INSERT INTO agent_configs (
			id, org_id, project_id, provider, encrypted_api_key,
			model_override, extra_config, is_default
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (
			org_id,
			COALESCE(project_id, '00000000-0000-0000-0000-000000000000'::uuid),
			provider
		)
		DO UPDATE SET
			encrypted_api_key = EXCLUDED.encrypted_api_key,
			model_override    = EXCLUDED.model_override,
			extra_config      = EXCLUDED.extra_config,
			is_default        = EXCLUDED.is_default,
			updated_at        = NOW()`

	if cfg.ID == uuid.Nil {
		cfg.ID = uuid.New()
	}

	_, err := s.pool.Exec(ctx, q,
		cfg.ID,
		cfg.OrgID,
		cfg.ProjectID,
		cfg.Provider,
		cfg.EncryptedAPIKey,
		cfg.ModelOverride,
		extra,
		cfg.IsDefault,
	)
	if err != nil {
		return fmt.Errorf("store.SetAgentConfig: %w", err)
	}
	return nil
}

// DeleteAgentConfig removes a specific provider config for org/project scope.
func (s *Store) DeleteAgentConfig(ctx context.Context, orgID uuid.UUID, projectID *uuid.UUID, provider string) error {
	const q = `
		DELETE FROM agent_configs
		WHERE  org_id = $1
		  AND  project_id IS NOT DISTINCT FROM $2
		  AND  provider = $3`

	_, err := s.pool.Exec(ctx, q, orgID, projectID, provider)
	if err != nil {
		return fmt.Errorf("store.DeleteAgentConfig: %w", err)
	}
	return nil
}

// GetEffectiveConfig resolves the effective config for a project:
// first project-level override, then org-level default (project_id IS NULL).
func (s *Store) GetEffectiveConfig(ctx context.Context, orgID, projectID uuid.UUID) (*AgentConfigRow, error) {
	const projectQ = `
		SELECT ` + agentConfigColumns + `
		FROM   agent_configs
		WHERE  org_id = $1
		  AND  project_id = $2
		ORDER  BY updated_at DESC
		LIMIT  1`

	row := s.pool.QueryRow(ctx, projectQ, orgID, projectID)
	cfg, err := scanAgentConfig(row)
	if err == nil {
		return &cfg, nil
	}
	if err != pgx.ErrNoRows {
		return nil, fmt.Errorf("store.GetEffectiveConfig project-level: %w", err)
	}

	const orgDefaultQ = `
		SELECT ` + agentConfigColumns + `
		FROM   agent_configs
		WHERE  org_id = $1
		  AND  project_id IS NULL
		ORDER  BY is_default DESC, updated_at DESC
		LIMIT  1`

	row = s.pool.QueryRow(ctx, orgDefaultQ, orgID)
	cfg, err = scanAgentConfig(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("store.GetEffectiveConfig org-level: %w", err)
	}
	return &cfg, nil
}

// ListOrgAgentConfigs returns all agent configs for an org.
func (s *Store) ListOrgAgentConfigs(ctx context.Context, orgID uuid.UUID) ([]AgentConfigRow, error) {
	const q = `
		SELECT ` + agentConfigColumns + `
		FROM   agent_configs
		WHERE  org_id = $1
		ORDER  BY provider ASC, project_id NULLS FIRST, created_at DESC`

	rows, err := s.pool.Query(ctx, q, orgID)
	if err != nil {
		return nil, fmt.Errorf("store.ListOrgAgentConfigs: %w", err)
	}
	defer rows.Close()

	var out []AgentConfigRow
	for rows.Next() {
		cfg, err := scanAgentConfig(rows)
		if err != nil {
			return nil, fmt.Errorf("store.ListOrgAgentConfigs: scan: %w", err)
		}
		out = append(out, cfg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store.ListOrgAgentConfigs: rows: %w", err)
	}
	return out, nil
}

func scanAgentConfig(row pgx.Row) (AgentConfigRow, error) {
	var cfg AgentConfigRow
	var extra []byte
	err := row.Scan(
		&cfg.ID,
		&cfg.OrgID,
		&cfg.ProjectID,
		&cfg.Provider,
		&cfg.EncryptedAPIKey,
		&cfg.ModelOverride,
		&extra,
		&cfg.IsDefault,
		&cfg.CreatedAt,
		&cfg.UpdatedAt,
	)
	if err != nil {
		return AgentConfigRow{}, err
	}
	if extra != nil {
		cfg.ExtraConfig = json.RawMessage(extra)
	} else {
		cfg.ExtraConfig = json.RawMessage(`{}`)
	}
	return cfg, nil
}
