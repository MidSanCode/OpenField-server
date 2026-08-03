package repository

import (
	"database/sql"
	"errors"
	"strings"

	"github.com/openfield/server/pkg/database"
)

// Sentinel errors returned by repositories.
var (
	ErrUsernameTaken = errors.New("username already taken")
	ErrNotFound      = errors.New("not found")
	// ErrNoSuchRow reports that no row was affected (missing or not owned).
	ErrNoSuchRow = sql.ErrNoRows
	// ErrAlreadyHandled reports a consent request that was already accepted or declined.
	ErrAlreadyHandled = errors.New("request already handled")
	// ErrDeletedMessage reports an attempt to edit/delete an already deleted message.
	ErrDeletedMessage = errors.New("message already deleted")
)

// isUniqueViolation detects PostgreSQL unique constraint violations.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "23505")
}

// ValidateRefreshToken checks if a refresh token is valid and returns its user id.
func ValidateRefreshToken(token string) (int64, error) {
	var userID int64
	err := database.DB.QueryRow(
		"SELECT user_id FROM refresh_tokens WHERE token = $1 AND expires_at > NOW()",
		token,
	).Scan(&userID)
	if err != nil {
		return 0, err
	}
	return userID, nil
}

// StoreRefreshToken persists a refresh token for a user.
func StoreRefreshToken(userID int64, token string, expiresInSeconds int) error {
	_, err := database.DB.Exec(
		"INSERT INTO refresh_tokens (user_id, token, expires_at) VALUES ($1, $2, NOW() + ($3 || ' seconds')::interval)",
		userID, token, expiresInSeconds,
	)
	if err != nil {
		return err
	}
	return nil
}

// RotateRefreshToken invalidates an old refresh token and persists a new one,
// atomically. Returns ErrNotFound when the old token was invalid or expired.
func RotateRefreshToken(oldToken, newToken string, userID int64, expiresInSeconds int) error {
	tx, err := database.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		"DELETE FROM refresh_tokens WHERE token = $1 AND user_id = $2 AND expires_at > NOW()",
		oldToken, userID,
	)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}

	if _, err := tx.Exec(
		"INSERT INTO refresh_tokens (user_id, token, expires_at) VALUES ($1, $2, NOW() + ($3 || ' seconds')::interval)",
		userID, newToken, expiresInSeconds,
	); err != nil {
		return err
	}

	return tx.Commit()
}

// RevokeRefreshToken deletes all refresh tokens belonging to a user, e.g. on
// logout or password change.
func RevokeRefreshTokens(userID int64) error {
	_, err := database.DB.Exec("DELETE FROM refresh_tokens WHERE user_id = $1", userID)
	return err
}

