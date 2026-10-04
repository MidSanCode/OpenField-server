package repository

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/lib/pq"
	"github.com/openfield/server/pkg/database"
)

// Sentinel errors returned by repositories.
var (
	ErrUsernameTaken = errors.New("username already taken")
	// ErrCampNameTaken reports that a camp name collides with an existing
	// one (camps.name is UNIQUE).
	ErrCampNameTaken = errors.New("camp name already taken")
	ErrNotFound      = errors.New("not found")
	// ErrForbidden reports that the caller's role/permissions are
	// insufficient for the requested operation.
	ErrForbidden = errors.New("forbidden")
	// ErrNoSuchRow reports that no row was affected (missing or not owned).
	ErrNoSuchRow = sql.ErrNoRows
	// ErrAlreadyHandled reports a consent request that was already accepted or declined.
	ErrAlreadyHandled = errors.New("request already handled")
	// ErrDeletedMessage reports an attempt to edit/delete an already deleted message.
	ErrDeletedMessage = errors.New("message already deleted")
	// ErrInvalidAmount reports a wallet amount outside the accepted range.
	ErrInvalidAmount = errors.New("invalid amount")
	// ErrQuotaExceeded reports that storing an attachment would push the user's
	// total usage past their storage quota.
	ErrQuotaExceeded = errors.New("storage quota exceeded")
	// ErrMemberLimitReached reports that a group chat or camp is at its member
	// ceiling and cannot accept another member.
	ErrMemberLimitReached = errors.New("member limit reached")
)

// isUniqueViolation detects PostgreSQL unique constraint violations. lib/pq
// exposes the SQLSTATE in *pq.Error.Code — its Error() string carries only the
// message (e.g. `pq: duplicate key value violates unique constraint "..."`),
// so matching "23505" against the text silently misses every violation. Both
// the typed check and a text fallback are used so wrapped errors are caught.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return pqErr.Code == "23505"
	}
	msg := err.Error()
	return strings.Contains(msg, "23505") ||
		strings.Contains(msg, "duplicate key value violates unique constraint")
}

// IsUniqueViolation is the exported form of isUniqueViolation, used by
// services that provision rows outside this package (e.g. OAuth account
// creation with username collision retries).
func IsUniqueViolation(err error) bool {
	return isUniqueViolation(err)
}

// hashRefreshToken derives the stored form of a refresh token. Tokens are kept
// hashed so a database leak does not expose usable session credentials; the
// raw token only ever lives in the response body and the client's storage.
func hashRefreshToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ValidateRefreshToken checks if a refresh token is valid and returns its user id.
func ValidateRefreshToken(token string) (int64, error) {
	var userID int64
	// Only the hashed form is ever stored, so only the hashed form may match.
	// An "OR token = $2" plaintext branch used to live here and silently kept
	// legacy plaintext rows usable; call MigrateRefreshTokenHashes once to
	// convert any that remain instead of matching them at request time.
	err := database.DB.QueryRow(
		"SELECT user_id FROM refresh_tokens WHERE token = $1 AND expires_at > NOW()",
		hashRefreshToken(token),
	).Scan(&userID)
	if err != nil {
		return 0, err
	}
	return userID, nil
}

// MigrateRefreshTokenHashes rewrites legacy plaintext refresh-token rows as
// their SHA-256 hashes. Rows are only rewritten when no hashed row already
// holds that digest, so the token column's UNIQUE constraint is respected.
// Safe to run repeatedly; returns the number of rows converted.
func MigrateRefreshTokenHashes() (int64, error) {
	rows, err := database.DB.Query(
		`SELECT id, token FROM refresh_tokens
		  WHERE token !~ '^[0-9a-f]{64}$'`,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to scan plaintext refresh tokens: %w", err)
	}
	type legacyRow struct {
		id    int64
		token string
	}
	legacy := []legacyRow{}
	for rows.Next() {
		var r legacyRow
		if err := rows.Scan(&r.id, &r.token); err != nil {
			rows.Close()
			return 0, fmt.Errorf("failed to scan plaintext refresh token: %w", err)
		}
		legacy = append(legacy, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("failed to scan plaintext refresh tokens: %w", err)
	}
	rows.Close()

	var converted int64
	for _, r := range legacy {
		res, err := database.DB.Exec(
			`UPDATE refresh_tokens SET token = $2
			  WHERE id = $1
			    AND NOT EXISTS (SELECT 1 FROM refresh_tokens other WHERE other.token = $2)`,
			r.id, hashRefreshToken(r.token),
		)
		if err != nil {
			return converted, fmt.Errorf("failed to hash refresh token %d: %w", r.id, err)
		}
		if affected, _ := res.RowsAffected(); affected > 0 {
			converted += affected
		} else {
			// A hashed twin already exists (the same token was written twice
			// by different code paths); the plaintext row is redundant, so
			// drop it rather than leaving the credential readable.
			if _, err := database.DB.Exec(`DELETE FROM refresh_tokens WHERE id = $1`, r.id); err != nil {
				return converted, fmt.Errorf("failed to drop duplicate plaintext refresh token %d: %w", r.id, err)
			}
		}
	}
	return converted, nil
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
		hashRefreshToken(oldToken), userID,
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
		userID, hashRefreshToken(newToken), expiresInSeconds,
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

// PurgeExpiredRefreshTokens removes expired rows. Intended for periodic use;
// safe to run concurrently.
func PurgeExpiredRefreshTokens() error {
	_, err := database.DB.Exec("DELETE FROM refresh_tokens WHERE expires_at <= NOW()")
	return err
}
