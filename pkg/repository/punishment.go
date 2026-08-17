package repository

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/openfield/server/pkg/database"
	"github.com/openfield/server/pkg/model"
)

// PunishmentRepository handles moderation actions and their history.
type PunishmentRepository struct{}

// NewPunishmentRepository creates a new PunishmentRepository.
func NewPunishmentRepository() *PunishmentRepository {
	return &PunishmentRepository{}
}

// Record inserts one punishment row and applies its side effects atomically.
// Supported types:
//   - warning:   no side effect, history only.
//   - demerit:   no side effect, history only.
//   - revoke:    inserts into user_permission_bans for the given key.
//   - temp_ban:  sets status='banned' with banned_until=expires_at.
//   - ban:       sets status='banned' and clears banned_until (permanent).
//   - unban:     sets status='active', clears banned_until and lifts every
//     permission ban.
//   - restore:   removes one user_permission_bans row.
func (r *PunishmentRepository) Record(userID, operatorID int64, ptype model.PunishmentType, permissionKey, reason string, expiresAt *time.Time) (int64, error) {
	tx, err := database.DB.Begin()
	if err != nil {
		return 0, fmt.Errorf("failed to begin punishment transaction: %w", err)
	}
	defer tx.Rollback()

	var id int64
	err = tx.QueryRow(
		`INSERT INTO user_punishments (user_id, operator_id, type, permission_key, reason, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		userID, operatorID, string(ptype), permissionKey, reason, expiresAt,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("failed to record punishment: %w", err)
	}

	switch ptype {
	case model.PunishRevoke:
		if permissionKey == "" {
			return 0, fmt.Errorf("revoke requires a permission key")
		}
		if _, err := tx.Exec(
			`INSERT INTO user_permission_bans (user_id, permission_key, operator_id, reason)
			 VALUES ($1, $2, $3, $4) ON CONFLICT (user_id, permission_key) DO UPDATE SET reason = EXCLUDED.reason, operator_id = EXCLUDED.operator_id`,
			userID, permissionKey, operatorID, reason,
		); err != nil {
			return 0, fmt.Errorf("failed to ban permission: %w", err)
		}
	case model.PunishTempBan:
		if _, err := tx.Exec(
			`UPDATE users SET status = 'banned', banned_until = $2, updated_at = NOW() WHERE id = $1`,
			userID, expiresAt,
		); err != nil {
			return 0, fmt.Errorf("failed to ban user: %w", err)
		}
	case model.PunishBan:
		if _, err := tx.Exec(
			`UPDATE users SET status = 'banned', banned_until = NULL, updated_at = NOW() WHERE id = $1`,
			userID,
		); err != nil {
			return 0, fmt.Errorf("failed to ban user: %w", err)
		}
	case model.PunishUnban:
		if _, err := tx.Exec(
			`UPDATE users SET status = 'active', banned_until = NULL, updated_at = NOW() WHERE id = $1`,
			userID,
		); err != nil {
			return 0, fmt.Errorf("failed to unban user: %w", err)
		}
		if _, err := tx.Exec(
			`DELETE FROM user_permission_bans WHERE user_id = $1`,
			userID,
		); err != nil {
			return 0, fmt.Errorf("failed to clear permission bans: %w", err)
		}
	case model.PunishRestore:
		if permissionKey == "" {
			return 0, fmt.Errorf("restore requires a permission key")
		}
		if _, err := tx.Exec(
			`DELETE FROM user_permission_bans WHERE user_id = $1 AND permission_key = $2`,
			userID, permissionKey,
		); err != nil {
			return 0, fmt.Errorf("failed to restore permission: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("failed to commit punishment: %w", err)
	}
	return id, nil
}

// ListByUser returns a user's punishment history, newest first.
func (r *PunishmentRepository) ListByUser(userID int64, limit int) ([]model.Punishment, error) {
	if limit < 1 {
		limit = 50
	}
	rows, err := database.DB.Query(
		`SELECT id, user_id, operator_id, type, permission_key, reason, expires_at, created_at
		 FROM user_punishments WHERE user_id = $1 ORDER BY id DESC LIMIT $2`,
		userID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query punishments: %w", err)
	}
	defer rows.Close()

	punishments := make([]model.Punishment, 0)
	for rows.Next() {
		var p model.Punishment
		if err := rows.Scan(&p.ID, &p.UserID, &p.OperatorID, &p.Type, &p.PermissionKey, &p.Reason, &p.ExpiresAt, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan punishment: %w", err)
		}
		punishments = append(punishments, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return punishments, nil
}

// ListPermissionBans returns every permission withheld from a user.
func (r *PunishmentRepository) ListPermissionBans(userID int64) ([]string, error) {
	rows, err := database.DB.Query(
		`SELECT permission_key FROM user_permission_bans WHERE user_id = $1`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query permission bans: %w", err)
	}
	defer rows.Close()

	keys := make([]string, 0)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("failed to scan permission ban: %w", err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return keys, nil
}

// IsBanned returns whether a user is currently banned given their status and
// ban expiry.
func (r *PunishmentRepository) IsBanned(user *model.User, now time.Time) bool {
	if user == nil || user.Status != "banned" {
		return false
	}
	if user.BannedUntil == nil {
		return true // permanent ban
	}
	return now.Before(*user.BannedUntil)
}

// GetByID returns a user (nil when missing).
func (r *PunishmentRepository) userByID(userID int64) (*model.User, error) {
	return NewUserRepository().GetByID(userID)
}