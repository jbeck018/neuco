package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/neuco-ai/neuco/internal/domain"
)

const notificationColumns = `id, org_id, user_id, type, title, body, link, read_at, created_at`

// scanNotification reads a single Notification from a pgx row.
func scanNotification(row pgx.Row) (domain.Notification, error) {
	var n domain.Notification
	err := row.Scan(
		&n.ID,
		&n.OrgID,
		&n.UserID,
		&n.Type,
		&n.Title,
		&n.Body,
		&n.Link,
		&n.ReadAt,
		&n.CreatedAt,
	)
	if err != nil {
		return domain.Notification{}, err
	}
	return n, nil
}

func collectNotifications(rows pgx.Rows) ([]domain.Notification, error) {
	var out []domain.Notification
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return out, nil
}

// ─── Tenant-scoped (API handlers) ────────────────────────────────────────────

// ListUserNotifications returns notifications for a user in an org, ordered by
// created_at DESC. Includes org-wide notifications (user_id IS NULL).
func (s *Store) ListUserNotifications(
	ctx context.Context,
	userID, orgID uuid.UUID,
	unreadOnly bool,
	pp PageParams,
) ([]domain.Notification, int, error) {
	where := "WHERE org_id = $1 AND (user_id = $2 OR user_id IS NULL)"
	args := []any{orgID, userID}

	if unreadOnly {
		where += " AND read_at IS NULL"
	}

	// Count.
	var total int
	countQ := "SELECT COUNT(*) FROM notifications " + where
	if err := s.pool.QueryRow(ctx, countQ, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("store.ListUserNotifications count: %w", err)
	}

	// Data.
	args = append(args, pp.Limit, pp.Offset)
	dataQ := fmt.Sprintf(
		"SELECT %s FROM notifications %s ORDER BY created_at DESC LIMIT $%d OFFSET $%d",
		notificationColumns, where, len(args)-1, len(args),
	)

	rows, err := s.pool.Query(ctx, dataQ, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("store.ListUserNotifications: %w", err)
	}
	defer rows.Close()

	notifs, err := collectNotifications(rows)
	if err != nil {
		return nil, 0, fmt.Errorf("store.ListUserNotifications: %w", err)
	}
	return notifs, total, nil
}

// UnreadNotificationCount returns the number of unread notifications for a user
// in an org (including org-wide notifications).
func (s *Store) UnreadNotificationCount(ctx context.Context, userID, orgID uuid.UUID) (int, error) {
	const q = `
		SELECT COUNT(*)
		FROM   notifications
		WHERE  org_id = $1 AND (user_id = $2 OR user_id IS NULL) AND read_at IS NULL`
	var count int
	if err := s.pool.QueryRow(ctx, q, orgID, userID).Scan(&count); err != nil {
		return 0, fmt.Errorf("store.UnreadNotificationCount: %w", err)
	}
	return count, nil
}

// MarkNotificationRead sets read_at for a notification owned by the given user.
func (s *Store) MarkNotificationRead(ctx context.Context, userID, orgID, notifID uuid.UUID) error {
	const q = `
		UPDATE notifications
		SET    read_at = $4
		WHERE  id = $1 AND org_id = $2 AND (user_id = $3 OR user_id IS NULL) AND read_at IS NULL`
	_, err := s.pool.Exec(ctx, q, notifID, orgID, userID, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("store.MarkNotificationRead: %w", err)
	}
	return nil
}

// MarkAllNotificationsRead marks all unread notifications as read for a user in
// an org.
func (s *Store) MarkAllNotificationsRead(ctx context.Context, userID, orgID uuid.UUID) error {
	const q = `
		UPDATE notifications
		SET    read_at = $3
		WHERE  org_id = $1 AND (user_id = $2 OR user_id IS NULL) AND read_at IS NULL`
	_, err := s.pool.Exec(ctx, q, orgID, userID, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("store.MarkAllNotificationsRead: %w", err)
	}
	return nil
}

// ─── Internal (workers / jobs) ────────────────────────────────────────────────

// CreateNotificationInternal inserts a notification. Used by background jobs.
func (s *Store) CreateNotificationInternal(ctx context.Context, n domain.Notification) error {
	const q = `
		INSERT INTO notifications (org_id, user_id, type, title, body, link)
		VALUES ($1, $2, $3, $4, $5, $6)`
	_, err := s.pool.Exec(ctx, q, n.OrgID, n.UserID, n.Type, n.Title, n.Body, n.Link)
	if err != nil {
		return fmt.Errorf("store.CreateNotificationInternal: %w", err)
	}
	return nil
}
