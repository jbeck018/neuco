package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/neuco-ai/neuco/internal/domain"
)

// CreateOrg inserts a new organisation row and returns it.
func (s *Store) CreateOrg(ctx context.Context, name, slug string, plan domain.OrgPlan) (domain.Organization, error) {
	const q = `
		INSERT INTO organizations (name, slug, plan)
		VALUES ($1, $2, $3)
		RETURNING id, name, slug, plan, created_at, updated_at`

	row := s.pool.QueryRow(ctx, q, name, slug, plan)
	org, err := scanOrg(row)
	if err != nil {
		return domain.Organization{}, fmt.Errorf("store.CreateOrg: %w", err)
	}
	return org, nil
}

// GetOrgByID returns an organisation by its UUID.
func (s *Store) GetOrgByID(ctx context.Context, id uuid.UUID) (domain.Organization, error) {
	const q = `
		SELECT id, name, slug, plan, created_at, updated_at
		FROM   organizations
		WHERE  id = $1`

	row := s.pool.QueryRow(ctx, q, id)
	org, err := scanOrg(row)
	if err != nil {
		return domain.Organization{}, fmt.Errorf("store.GetOrgByID: %w", err)
	}
	return org, nil
}

// GetOrgBySlug returns an organisation by its URL-safe slug.
func (s *Store) GetOrgBySlug(ctx context.Context, slug string) (domain.Organization, error) {
	const q = `
		SELECT id, name, slug, plan, created_at, updated_at
		FROM   organizations
		WHERE  slug = $1`

	row := s.pool.QueryRow(ctx, q, slug)
	org, err := scanOrg(row)
	if err != nil {
		return domain.Organization{}, fmt.Errorf("store.GetOrgBySlug: %w", err)
	}
	return org, nil
}

// ListUserOrgs returns every organisation of which userID is a member.
func (s *Store) ListUserOrgs(ctx context.Context, userID uuid.UUID) ([]domain.Organization, error) {
	const q = `
		SELECT o.id, o.name, o.slug, o.plan, o.created_at, o.updated_at
		FROM   organizations o
		JOIN   org_members m ON m.org_id = o.id
		WHERE  m.user_id = $1
		ORDER  BY o.name`

	rows, err := s.pool.Query(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("store.ListUserOrgs: %w", err)
	}
	defer rows.Close()

	var orgs []domain.Organization
	for rows.Next() {
		org, err := scanOrg(rows)
		if err != nil {
			return nil, fmt.Errorf("store.ListUserOrgs: scan: %w", err)
		}
		orgs = append(orgs, org)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store.ListUserOrgs: rows: %w", err)
	}
	return orgs, nil
}

// UpdateOrg modifies the name and/or plan of an existing organisation.
// Fields are updated only when the supplied pointer is non-nil.
func (s *Store) UpdateOrg(ctx context.Context, id uuid.UUID, name *string, plan *domain.OrgPlan) (domain.Organization, error) {
	const q = `
		UPDATE organizations
		SET    name       = COALESCE($2, name),
		       plan       = COALESCE($3, plan),
		       updated_at = NOW()
		WHERE  id = $1
		RETURNING id, name, slug, plan, created_at, updated_at`

	row := s.pool.QueryRow(ctx, q, id, name, plan)
	org, err := scanOrg(row)
	if err != nil {
		return domain.Organization{}, fmt.Errorf("store.UpdateOrg: %w", err)
	}
	return org, nil
}

// DeleteOrg permanently removes an organisation and cascades deletes to all
// member, project, and signal rows via the database ON DELETE CASCADE clauses.
func (s *Store) DeleteOrg(ctx context.Context, id uuid.UUID) error {
	const q = `DELETE FROM organizations WHERE id = $1`
	ct, err := s.pool.Exec(ctx, q, id)
	if err != nil {
		return fmt.Errorf("store.DeleteOrg: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("store.DeleteOrg: org %s not found", id)
	}
	return nil
}

// GetOrgCreatedAt returns the creation timestamp for an org.
func (s *Store) GetOrgCreatedAt(ctx context.Context, id uuid.UUID) (time.Time, error) {
	const q = `SELECT created_at FROM organizations WHERE id = $1`
	var t time.Time
	if err := s.pool.QueryRow(ctx, q, id).Scan(&t); err != nil {
		return time.Time{}, fmt.Errorf("store.GetOrgCreatedAt: %w", err)
	}
	return t, nil
}

// scanOrg reads a single Organization from any pgx row-like value.
func scanOrg(row pgx.Row) (domain.Organization, error) {
	var o domain.Organization
	err := row.Scan(
		&o.ID,
		&o.Name,
		&o.Slug,
		&o.Plan,
		&o.CreatedAt,
		&o.UpdatedAt,
	)
	if err != nil {
		return domain.Organization{}, err
	}
	return o, nil
}

// ListAllOrgs returns every organisation in the system.
// This is an operator-only method and should not be exposed to regular API users.
func (s *Store) ListAllOrgs(ctx context.Context) ([]domain.Organization, error) {
	const q = `
		SELECT id, name, slug, plan, created_at, updated_at
		FROM   organizations
		ORDER  BY created_at DESC`

	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("store.ListAllOrgs: %w", err)
	}
	defer rows.Close()

	var orgs []domain.Organization
	for rows.Next() {
		org, err := scanOrg(rows)
		if err != nil {
			return nil, fmt.Errorf("store.ListAllOrgs: scan: %w", err)
		}
		orgs = append(orgs, org)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store.ListAllOrgs: rows: %w", err)
	}
	return orgs, nil
}
