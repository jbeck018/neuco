package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/neuco-ai/neuco/internal/domain"
)

// AddMember inserts a new org_members row with the given role. The member is
// initially in an "invited" state (joined_at is NULL) until they accept.
func (s *Store) AddMember(ctx context.Context, orgID, userID uuid.UUID, role domain.OrgRole) (domain.OrgMember, error) {
	const q = `
		INSERT INTO org_members (org_id, user_id, role)
		VALUES ($1, $2, $3)
		ON CONFLICT (org_id, user_id) DO UPDATE SET role = EXCLUDED.role
		RETURNING org_id, user_id, role, invited_at, joined_at`

	row := s.pool.QueryRow(ctx, q, orgID, userID, role)
	m, err := scanMember(row)
	if err != nil {
		return domain.OrgMember{}, fmt.Errorf("store.AddMember: %w", err)
	}
	return m, nil
}

// RemoveMember deletes the org_members row for the given user in the org.
func (s *Store) RemoveMember(ctx context.Context, orgID, userID uuid.UUID) error {
	const q = `DELETE FROM org_members WHERE org_id = $1 AND user_id = $2`
	ct, err := s.pool.Exec(ctx, q, orgID, userID)
	if err != nil {
		return fmt.Errorf("store.RemoveMember: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("store.RemoveMember: member not found in org %s", orgID)
	}
	return nil
}

// UpdateMemberRole changes the role assigned to a member of an org.
func (s *Store) UpdateMemberRole(ctx context.Context, orgID, userID uuid.UUID, role domain.OrgRole) (domain.OrgMember, error) {
	const q = `
		UPDATE org_members
		SET    role = $3
		WHERE  org_id = $1 AND user_id = $2
		RETURNING org_id, user_id, role, invited_at, joined_at`

	row := s.pool.QueryRow(ctx, q, orgID, userID, role)
	m, err := scanMember(row)
	if err != nil {
		return domain.OrgMember{}, fmt.Errorf("store.UpdateMemberRole: %w", err)
	}
	return m, nil
}

// MarkMemberJoined stamps joined_at for a member who has accepted an invite.
func (s *Store) MarkMemberJoined(ctx context.Context, orgID, userID uuid.UUID) error {
	const q = `
		UPDATE org_members
		SET    joined_at = NOW()
		WHERE  org_id = $1 AND user_id = $2 AND joined_at IS NULL`
	_, err := s.pool.Exec(ctx, q, orgID, userID)
	if err != nil {
		return fmt.Errorf("store.MarkMemberJoined: %w", err)
	}
	return nil
}

// ListOrgMembers returns all members of an org with their user profiles joined.
func (s *Store) ListOrgMembers(ctx context.Context, orgID uuid.UUID) ([]OrgMemberWithUser, error) {
	const q = `
		SELECT m.org_id,
		       m.user_id,
		       m.role,
		       m.invited_at,
		       m.joined_at,
		       u.github_login,
		       u.email,
		       u.avatar_url,
		       m.digest_opt_out
		FROM   org_members m
		JOIN   users u ON u.id = m.user_id
		WHERE  m.org_id = $1
		ORDER  BY m.invited_at`

	rows, err := s.pool.Query(ctx, q, orgID)
	if err != nil {
		return nil, fmt.Errorf("store.ListOrgMembers: %w", err)
	}
	defer rows.Close()

	var members []OrgMemberWithUser
	for rows.Next() {
		var m OrgMemberWithUser
		if err := rows.Scan(
			&m.OrgID,
			&m.UserID,
			&m.Role,
			&m.InvitedAt,
			&m.JoinedAt,
			&m.GitHubLogin,
			&m.Email,
			&m.AvatarURL,
			&m.DigestOptOut,
		); err != nil {
			return nil, fmt.Errorf("store.ListOrgMembers: scan: %w", err)
		}
		members = append(members, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store.ListOrgMembers: rows: %w", err)
	}
	return members, nil
}

// GetMemberRole returns the role of a specific user in an org, or an error if
// the user is not a member.
func (s *Store) GetMemberRole(ctx context.Context, orgID, userID uuid.UUID) (domain.OrgRole, error) {
	const q = `SELECT role FROM org_members WHERE org_id = $1 AND user_id = $2`
	var role domain.OrgRole
	if err := s.pool.QueryRow(ctx, q, orgID, userID).Scan(&role); err != nil {
		return "", fmt.Errorf("store.GetMemberRole: %w", err)
	}
	return role, nil
}

// OrgMemberWithUser combines the org_members row with selected user profile
// columns so callers do not need a second query.
type OrgMemberWithUser struct {
	OrgID       uuid.UUID      `json:"org_id"`
	UserID      uuid.UUID      `json:"user_id"`
	Role        domain.OrgRole `json:"role"`
	InvitedAt   time.Time      `json:"invited_at"`
	JoinedAt    *time.Time     `json:"joined_at,omitempty"`
	GitHubLogin string         `json:"github_login"`
	Email       string         `json:"email"`
	AvatarURL    string         `json:"avatar_url"`
	DigestOptOut bool           `json:"digest_opt_out"`
}

// SetDigestOptOut updates the digest_opt_out preference for a member.
func (s *Store) SetDigestOptOut(ctx context.Context, orgID, userID uuid.UUID, optOut bool) error {
	const q = `UPDATE org_members SET digest_opt_out = $3 WHERE org_id = $1 AND user_id = $2`
	ct, err := s.pool.Exec(ctx, q, orgID, userID, optOut)
	if err != nil {
		return fmt.Errorf("store.SetDigestOptOut: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("store.SetDigestOptOut: member not found")
	}
	return nil
}

// scanMember reads a single OrgMember from any pgx row-like value.
func scanMember(row pgx.Row) (domain.OrgMember, error) {
	var m domain.OrgMember
	err := row.Scan(
		&m.OrgID,
		&m.UserID,
		&m.Role,
		&m.InvitedAt,
		&m.JoinedAt,
	)
	if err != nil {
		return domain.OrgMember{}, err
	}
	return m, nil
}
