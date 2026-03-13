package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/neuco-ai/neuco/internal/domain"
)

const projectContextColumns = `
	id, project_id, category, title, content, source_run_id, metadata,
	created_at, updated_at`

// InsertProjectContext creates a new context entry for a project.
func (s *Store) InsertProjectContext(ctx context.Context, pc domain.ProjectContext) (domain.ProjectContext, error) {
	meta := pc.Metadata
	if meta == nil {
		meta = json.RawMessage(`{}`)
	}

	const q = `
		INSERT INTO project_contexts
		       (project_id, category, title, content, source_run_id, metadata)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING ` + projectContextColumns

	row := s.pool.QueryRow(ctx, q,
		pc.ProjectID,
		pc.Category,
		pc.Title,
		pc.Content,
		pc.SourceRunID,
		meta,
	)
	return scanProjectContext(row)
}

// GetProjectContext returns a single context entry scoped to a project.
func (s *Store) GetProjectContext(ctx context.Context, projectID, contextID uuid.UUID) (domain.ProjectContext, error) {
	const q = `
		SELECT ` + projectContextColumns + `
		FROM   project_contexts
		WHERE  id = $1 AND project_id = $2`

	row := s.pool.QueryRow(ctx, q, contextID, projectID)
	pc, err := scanProjectContext(row)
	if err != nil {
		return domain.ProjectContext{}, fmt.Errorf("store.GetProjectContext: %w", err)
	}
	return pc, nil
}

// ListProjectContexts returns paginated context entries for a project, optionally
// filtered by category.
func (s *Store) ListProjectContexts(
	ctx context.Context,
	projectID uuid.UUID,
	category string,
	pp PageParams,
) ([]domain.ProjectContext, int, error) {
	args := []any{projectID}
	where := "WHERE project_id = $1"
	if category != "" {
		args = append(args, category)
		where += fmt.Sprintf(" AND category = $%d", len(args))
	}

	countQ := "SELECT COUNT(*) FROM project_contexts " + where
	var total int
	if err := s.pool.QueryRow(ctx, countQ, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("store.ListProjectContexts count: %w", err)
	}

	args = append(args, pp.Limit, pp.Offset)
	dataQ := fmt.Sprintf(
		"SELECT %s FROM project_contexts %s ORDER BY created_at DESC LIMIT $%d OFFSET $%d",
		projectContextColumns, where, len(args)-1, len(args),
	)

	rows, err := s.pool.Query(ctx, dataQ, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("store.ListProjectContexts: %w", err)
	}
	defer rows.Close()

	contexts, err := collectProjectContexts(rows)
	if err != nil {
		return nil, 0, fmt.Errorf("store.ListProjectContexts: %w", err)
	}
	return contexts, total, nil
}

// UpdateProjectContext updates the title, content, and category of a context entry.
func (s *Store) UpdateProjectContext(ctx context.Context, projectID, contextID uuid.UUID, title, content string, category string) (domain.ProjectContext, error) {
	const q = `
		UPDATE project_contexts
		SET    title = $3, content = $4, category = $5
		WHERE  id = $1 AND project_id = $2
		RETURNING ` + projectContextColumns

	row := s.pool.QueryRow(ctx, q, contextID, projectID, title, content, category)
	pc, err := scanProjectContext(row)
	if err != nil {
		return domain.ProjectContext{}, fmt.Errorf("store.UpdateProjectContext: %w", err)
	}
	return pc, nil
}

// DeleteProjectContext removes a context entry scoped to a project.
func (s *Store) DeleteProjectContext(ctx context.Context, projectID, contextID uuid.UUID) error {
	const q = `DELETE FROM project_contexts WHERE id = $1 AND project_id = $2`
	ct, err := s.pool.Exec(ctx, q, contextID, projectID)
	if err != nil {
		return fmt.Errorf("store.DeleteProjectContext: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("store.DeleteProjectContext: context %s not found in project %s", contextID, projectID)
	}
	return nil
}

// UpdateProjectContextEmbedding stores the vector embedding for a context entry.
func (s *Store) UpdateProjectContextEmbedding(ctx context.Context, contextID uuid.UUID, embedding []float32) error {
	lit := float32SliceToVectorLiteral(embedding)
	const q = `UPDATE project_contexts SET embedding = $2::vector WHERE id = $1`
	_, err := s.pool.Exec(ctx, q, contextID, lit)
	if err != nil {
		return fmt.Errorf("store.UpdateProjectContextEmbedding: %w", err)
	}
	return nil
}

// ContextSearchResult extends ProjectContext with a similarity distance.
type ContextSearchResult struct {
	domain.ProjectContext
	Distance float64 `json:"distance"`
}

// SearchProjectContexts finds the most similar context entries using pgvector
// cosine distance. Returns up to `limit` results ordered by similarity.
func (s *Store) SearchProjectContexts(
	ctx context.Context,
	projectID uuid.UUID,
	embedding []float32,
	limit int,
) ([]ContextSearchResult, error) {
	lit := float32SliceToVectorLiteral(embedding)

	const q = `
		SELECT ` + projectContextColumns + `,
		       (embedding <=> $2::vector) AS distance
		FROM   project_contexts
		WHERE  project_id = $1 AND embedding IS NOT NULL
		ORDER  BY embedding <=> $2::vector
		LIMIT  $3`

	rows, err := s.pool.Query(ctx, q, projectID, lit, limit)
	if err != nil {
		return nil, fmt.Errorf("store.SearchProjectContexts: %w", err)
	}
	defer rows.Close()

	var results []ContextSearchResult
	for rows.Next() {
		var r ContextSearchResult
		var meta []byte
		err := rows.Scan(
			&r.ID,
			&r.ProjectID,
			&r.Category,
			&r.Title,
			&r.Content,
			&r.SourceRunID,
			&meta,
			&r.CreatedAt,
			&r.UpdatedAt,
			&r.Distance,
		)
		if err != nil {
			return nil, fmt.Errorf("store.SearchProjectContexts scan: %w", err)
		}
		if meta != nil {
			r.Metadata = json.RawMessage(meta)
		} else {
			r.Metadata = json.RawMessage(`{}`)
		}
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store.SearchProjectContexts rows: %w", err)
	}
	return results, nil
}

// ListProjectContextsInternal returns all context entries for a project without
// tenant scoping (for workers).
func (s *Store) ListProjectContextsInternal(ctx context.Context, projectID uuid.UUID, limit int) ([]domain.ProjectContext, error) {
	const q = `
		SELECT ` + projectContextColumns + `
		FROM   project_contexts
		WHERE  project_id = $1
		ORDER  BY created_at DESC
		LIMIT  $2`

	rows, err := s.pool.Query(ctx, q, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("store.ListProjectContextsInternal: %w", err)
	}
	defer rows.Close()

	contexts, err := collectProjectContexts(rows)
	if err != nil {
		return nil, fmt.Errorf("store.ListProjectContextsInternal: %w", err)
	}
	return contexts, nil
}

func scanProjectContext(row pgx.Row) (domain.ProjectContext, error) {
	var pc domain.ProjectContext
	var meta []byte
	err := row.Scan(
		&pc.ID,
		&pc.ProjectID,
		&pc.Category,
		&pc.Title,
		&pc.Content,
		&pc.SourceRunID,
		&meta,
		&pc.CreatedAt,
		&pc.UpdatedAt,
	)
	if err != nil {
		return domain.ProjectContext{}, err
	}
	if meta != nil {
		pc.Metadata = json.RawMessage(meta)
	} else {
		pc.Metadata = json.RawMessage(`{}`)
	}
	return pc, nil
}

func collectProjectContexts(rows pgx.Rows) ([]domain.ProjectContext, error) {
	var pcs []domain.ProjectContext
	for rows.Next() {
		pc, err := scanProjectContext(rows)
		if err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		pcs = append(pcs, pc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return pcs, nil
}
