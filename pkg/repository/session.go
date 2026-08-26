package repository

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/openfield/server/pkg/database"
)

// Device sessions. Every refresh token is a session; the extra columns record
// which device it belongs to so users can audit and revoke logins.

// Session is one logged-in device for a user.
type Session struct {
	ID          int64      `json:"id"`
	DeviceLabel string     `json:"device_label"`
	LastIP      string     `json:"last_ip"`
	CreatedAt   time.Time  `json:"created_at"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
	Current     bool       `json:"current,omitempty"`
}

// CreateSession stores a refresh token together with its device metadata and
// reports whether this user already had a session on the same device label
// before (used to raise "new device login" notifications).
func CreateSession(userID int64, token string, expiresInSeconds int, deviceLabel, ip string) (knownDevice bool, err error) {
	var prior int
	if err := database.DB.QueryRow(
		`SELECT COUNT(*) FROM refresh_tokens WHERE user_id = $1 AND device_label = $2 AND expires_at > NOW()`,
		userID, deviceLabel,
	).Scan(&prior); err != nil {
		return false, fmt.Errorf("failed to inspect sessions: %w", err)
	}
	if _, err := database.DB.Exec(
		`INSERT INTO refresh_tokens (user_id, token, expires_at, device_label, last_used_at, last_ip)
		 VALUES ($1, $2, NOW() + ($3 || ' seconds')::interval, $4, NOW(), $5)`,
		userID, token, expiresInSeconds, deviceLabel, ip,
	); err != nil {
		return false, fmt.Errorf("failed to create session: %w", err)
	}
	return prior > 0, nil
}

// TouchSession updates the last-used time and IP when a refresh token rotates.
func TouchSession(token string, ip string) error {
	_, err := database.DB.Exec(
		"UPDATE refresh_tokens SET last_used_at = NOW(), last_ip = $2 WHERE token = $1",
		token, ip,
	)
	return err
}

// ListSessions returns the active sessions for a user, newest first.
func ListSessions(userID int64) ([]Session, error) {
	rows, err := database.DB.Query(
		`SELECT id, device_label, last_ip, created_at, last_used_at
		 FROM refresh_tokens WHERE user_id = $1 AND expires_at > NOW()
		 ORDER BY COALESCE(last_used_at, created_at) DESC`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list sessions: %w", err)
	}
	defer rows.Close()

	items := []Session{}
	for rows.Next() {
		s := Session{}
		if err := rows.Scan(&s.ID, &s.DeviceLabel, &s.LastIP, &s.CreatedAt, &s.LastUsedAt); err != nil {
			return nil, err
		}
		items = append(items, s)
	}
	return items, rows.Err()
}

// DeleteSession revokes one of the user's own sessions by row id.
func DeleteSession(userID, sessionID int64) error {
	res, err := database.DB.Exec(
		"DELETE FROM refresh_tokens WHERE id = $1 AND user_id = $2", sessionID, userID,
	)
	if err != nil {
		return fmt.Errorf("failed to delete session: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteOtherSessions revokes every session except the one carrying keepToken.
func DeleteOtherSessions(userID int64, keepToken string) (int64, error) {
	res, err := database.DB.Exec(
		"DELETE FROM refresh_tokens WHERE user_id = $1 AND token <> $2 AND expires_at > NOW()",
		userID, keepToken,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to delete other sessions: %w", err)
	}
	affected, _ := res.RowsAffected()
	return affected, nil
}

// TouchLastSeen records a heartbeat from an authenticated client.
func TouchLastSeen(userID int64) error {
	_, err := database.DB.Exec(
		"UPDATE users SET last_seen_at = NOW() WHERE id = $1 AND deleted_at IS NULL", userID,
	)
	return err
}

// IsUserOnline reports whether the user has heartbeated recently. The window
// matches the client heartbeat cadence with slack for network jitter.
func IsUserOnline(userID int64) bool {
	var n int
	err := database.DB.QueryRow(
		`SELECT COUNT(*) FROM users WHERE id = $1 AND deleted_at IS NULL
		 AND last_seen_at > NOW() - interval '150 seconds'`,
		userID,
	).Scan(&n)
	return err == nil && n > 0
}

// LastSeenOf returns the user's last heartbeat timestamp, if any.
func LastSeenOf(userID int64) (*time.Time, error) {
	var t sql.NullTime
	err := database.DB.QueryRow(
		"SELECT last_seen_at FROM users WHERE id = $1", userID,
	).Scan(&t)
	if err != nil {
		return nil, err
	}
	if !t.Valid {
		return nil, nil
	}
	return &t.Time, nil
}

// RequestAccountDeletion stamps the soft-delete marker. Idempotent.
func RequestAccountDeletion(userID int64) error {
	_, err := database.DB.Exec(
		"UPDATE users SET deleted_at = NOW(), updated_at = NOW() WHERE id = $1 AND deleted_at IS NULL",
		userID,
	)
	return err
}

// CancelAccountDeletion lifts a pending deletion within the grace window.
func CancelAccountDeletion(userID int64) error {
	_, err := database.DB.Exec(
		"UPDATE users SET deleted_at = NULL, updated_at = NOW() WHERE id = $1 AND deleted_at IS NOT NULL",
		userID,
	)
	return err
}

// ListPurgeableUsers returns ids whose grace window has fully elapsed.
func ListPurgeableUsers(graceDays int) ([]int64, error) {
	rows, err := database.DB.Query(
		`SELECT id FROM users WHERE deleted_at IS NOT NULL AND deleted_at < NOW() - ($1 || ' days')::interval`,
		graceDays,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list purgeable users: %w", err)
	}
	defer rows.Close()

	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// PurgeUserData erases everything belonging to the account. The users row is
// removed last; foreign keys with ON DELETE CASCADE clean up posts, replies,
// messages, favorites, notifications, sessions and attachments metadata along
// with it. Object-store files are best-effort deleted first via onDelete.
func PurgeUserData(userID int64, onDelete func(objectKey string)) error {
	// Best-effort storage cleanup before the attachment rows cascade away.
	if onDelete != nil {
		rows, err := database.DB.Query(
			"SELECT object_key, bucket FROM attachments WHERE user_id = $1", userID,
		)
		if err == nil {
			keys := [][2]string{}
			for rows.Next() {
				var key, bucket string
				if rows.Scan(&key, &bucket) == nil {
					keys = append(keys, [2]string{bucket, key})
				}
			}
			rows.Close()
			for _, kb := range keys {
				onDelete(kb[0] + "|" + kb[1])
			}
		}
	}

	stmts := []string{
		`DELETE FROM post_replies WHERE user_id = $1`,
		`UPDATE messages SET deleted_at = NOW() WHERE sender_id = $1`,
		`DELETE FROM user_follows WHERE follower_id = $1 OR followee_id = $1`,
		`DELETE FROM post_favorites WHERE user_id = $1`,
		`DELETE FROM reply_favorites WHERE user_id = $1`,
		`DELETE FROM post_reactions WHERE user_id = $1`,
		`DELETE FROM notifications WHERE user_id = $1`,
		`DELETE FROM bot_tokens WHERE user_id IN (SELECT id FROM users WHERE bot_owner_id = $1)`,
		`DELETE FROM refresh_tokens WHERE user_id = $1`,
		`DELETE FROM users WHERE id = $1`,
	}
	for _, stmt := range stmts {
		if _, err := database.DB.Exec(stmt, userID); err != nil {
			return fmt.Errorf("failed to purge user data (%s): %w", stmt, err)
		}
	}
	return nil
}
