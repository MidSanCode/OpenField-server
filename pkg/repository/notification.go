package repository

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lib/pq"
	"github.com/openfield/server/pkg/database"
)

// In-app notifications. Rows are created by backend services whenever
// something happens that the recipient should see later even if they were not
// online at the moment it happened (replies, payments, new device logins).
// A realtime copy is fanned out through the push service, but this table is
// the durable inbox clients sync against.

// Notification is one entry in a user's notification inbox.
type Notification struct {
	ID        int64           `json:"id"`
	Type      string          `json:"type"`
	Title     string          `json:"title"`
	Body      string          `json:"body"`
	Data      json.RawMessage `json:"data,omitempty"`
	ReadAt    *time.Time      `json:"read_at,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

const notificationColumns = "id, type, title, body, COALESCE(data::text, '{}'), read_at, created_at"

func scanNotification(row interface{ Scan(...any) error }) (*Notification, error) {
	n := &Notification{}
	var raw string
	err := row.Scan(&n.ID, &n.Type, &n.Title, &n.Body, &raw, &n.ReadAt, &n.CreatedAt)
	if err != nil {
		return nil, err
	}
	n.Data = json.RawMessage(raw)
	return n, nil
}

// CreateNotification inserts one notification row.
func CreateNotification(userID int64, typ, title, body string, data json.RawMessage) error {
	if len(data) == 0 {
		data = json.RawMessage("{}")
	}
	_, err := database.DB.Exec(
		`INSERT INTO notifications (user_id, type, title, body, data)
		 VALUES ($1, $2, $3, $4, $5::jsonb)`,
		userID, typ, title, body, string(data),
	)
	if err != nil {
		return fmt.Errorf("failed to create notification: %w", err)
	}
	return nil
}

// ListNotifications returns the user's newest notifications page.
func ListNotifications(userID int64, limit, offset int) ([]Notification, int64, error) {
	rows, err := database.DB.Query(
		`SELECT `+notificationColumns+` FROM notifications WHERE user_id = $1
		 ORDER BY created_at DESC LIMIT $2 OFFSET $3`,
		userID, limit, offset,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list notifications: %w", err)
	}
	defer rows.Close()

	items := []Notification{}
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, *n)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	var total int64
	if err := database.DB.QueryRow(
		"SELECT COUNT(*) FROM notifications WHERE user_id = $1", userID,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("failed to count notifications: %w", err)
	}
	return items, total, nil
}

// CountUnreadNotifications returns how many notifications the user has not
// read yet.
func CountUnreadNotifications(userID int64) (int64, error) {
	var n int64
	err := database.DB.QueryRow(
		"SELECT COUNT(*) FROM notifications WHERE user_id = $1 AND read_at IS NULL",
		userID,
	).Scan(&n)
	return n, err
}

// MarkNotificationsRead marks the given notification ids as read for a user.
// An empty id list marks everything read.
func MarkNotificationsRead(userID int64, ids []int64) (int64, error) {
	var res sql.Result
	var err error
	if len(ids) == 0 {
		res, err = database.DB.Exec(
			"UPDATE notifications SET read_at = NOW() WHERE user_id = $1 AND read_at IS NULL",
			userID,
		)
	} else {
		res, err = database.DB.Exec(
			`UPDATE notifications SET read_at = NOW()
			 WHERE user_id = $1 AND read_at IS NULL AND id = ANY($2)`,
			userID, pq.Array(ids),
		)
	}
	if err != nil {
		return 0, fmt.Errorf("failed to mark notifications read: %w", err)
	}
	affected, _ := res.RowsAffected()
	return affected, nil
}
