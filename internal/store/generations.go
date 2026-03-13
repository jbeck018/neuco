package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/neuco-ai/neuco/internal/domain"
)

const generationColumns = `
	id, project_id, spec_id, pipeline_run_id, status,
	branch_name, pr_number, pr_url, files, error, created_at, completed_at`

// CreateGeneration inserts a new generation record.
func (s *Store) CreateGeneration(ctx context.Context, g *domain.Generation) error {
	filesJSON, err := json.Marshal(g.Files)
	if err != nil {
		return fmt.Errorf("store.CreateGeneration: marshal files: %w", err)
	}

	const q = `
		INSERT INTO generations (id, project_id, spec_id, pipeline_run_id, status, branch_name, files)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`

	_, err = s.pool.Exec(ctx, q,
		g.ID,
		g.ProjectID,
		g.SpecID,
		g.PipelineRunID,
		g.Status,
		g.BranchName,
		filesJSON,
	)
	if err != nil {
		return fmt.Errorf("store.CreateGeneration: %w", err)
	}
	return nil
}

// GetGeneration returns a single generation by ID.
func (s *Store) GetGeneration(ctx context.Context, generationID uuid.UUID) (*domain.Generation, error) {
	const q = `
		SELECT ` + generationColumns + `
		FROM   generations
		WHERE  id = $1`

	row := s.pool.QueryRow(ctx, q, generationID)
	g, err := scanGeneration(row)
	if err != nil {
		return nil, fmt.Errorf("store.GetGeneration: %w", err)
	}
	return &g, nil
}

// ListProjectGenerations returns a paginated list of generation records for a project.
func (s *Store) ListProjectGenerations(ctx context.Context, projectID uuid.UUID, pp PageParams) ([]domain.Generation, int, error) {
	const countQ = `SELECT COUNT(*) FROM generations WHERE project_id = $1`
	var total int
	if err := s.pool.QueryRow(ctx, countQ, projectID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("store.ListProjectGenerations count: %w", err)
	}

	const q = `
		SELECT ` + generationColumns + `
		FROM   generations
		WHERE  project_id = $1
		ORDER  BY created_at DESC
		LIMIT  $2 OFFSET $3`

	rows, err := s.pool.Query(ctx, q, projectID, pp.Limit, pp.Offset)
	if err != nil {
		return nil, 0, fmt.Errorf("store.ListProjectGenerations: %w", err)
	}
	defer rows.Close()

	var gens []domain.Generation
	for rows.Next() {
		g, err := scanGeneration(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("store.ListProjectGenerations: scan: %w", err)
		}
		gens = append(gens, g)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("store.ListProjectGenerations: rows: %w", err)
	}
	return gens, total, nil
}

// UpdateGeneration updates a generation record.
func (s *Store) UpdateGeneration(ctx context.Context, g *domain.Generation) error {
	filesJSON, err := json.Marshal(g.Files)
	if err != nil {
		return fmt.Errorf("store.UpdateGeneration: marshal files: %w", err)
	}

	const q = `
		UPDATE generations
		SET    status       = $2,
		       branch_name  = COALESCE(NULLIF($3, ''), branch_name),
		       pr_number    = COALESCE($4, pr_number),
		       pr_url       = COALESCE(NULLIF($5, ''), pr_url),
		       files        = $6,
		       error        = COALESCE(NULLIF($7, ''), error),
		       completed_at = $8
		WHERE  id = $1`

	_, err = s.pool.Exec(ctx, q,
		g.ID,
		g.Status,
		g.BranchName,
		g.PRNumber,
		g.PRURL,
		filesJSON,
		g.ErrorMsg,
		g.CompletedAt,
	)
	if err != nil {
		return fmt.Errorf("store.UpdateGeneration: %w", err)
	}
	return nil
}

func scanGeneration(row pgx.Row) (domain.Generation, error) {
	var g domain.Generation
	var filesJSON []byte
	err := row.Scan(
		&g.ID,
		&g.ProjectID,
		&g.SpecID,
		&g.PipelineRunID,
		&g.Status,
		&g.BranchName,
		&g.PRNumber,
		&g.PRURL,
		&filesJSON,
		&g.ErrorMsg,
		&g.CreatedAt,
		&g.CompletedAt,
	)
	if err != nil {
		return domain.Generation{}, err
	}
	if len(filesJSON) > 0 {
		if err := json.Unmarshal(filesJSON, &g.Files); err != nil {
			return domain.Generation{}, fmt.Errorf("unmarshal files: %w", err)
		}
	}
	return g, nil
}
