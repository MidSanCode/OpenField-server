package repository

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/openfield/server/pkg/database"
)

// QR login handshakes. The device that wants to log in (usually desktop)
// creates a code and renders it as a QR image; an already-authenticated device
// (phone) scans it and approves; the requesting device polls until tokens show
// up. Codes are single-use, expire quickly, and never contain secrets.

// qrLoginTTL is how long a handshake code stays approvable. The requesting
// device renders the code as a QR on its own login screen and polls; five
// minutes leaves comfortable time to open the camera on another device
// without letting stale codes linger.
const qrLoginTTL = 5 * time.Minute

// ErrConflict marks an optimistic-concurrency failure (e.g. a handshake that
// was already consumed by someone else).
var ErrConflict = errors.New("conflict")

// QrLogin is one scan-to-sign-in handshake.
type QrLogin struct {
	Code         string    `json:"code"`
	Status       string    `json:"status"` // pending | confirmed | expired
	UserID       int64     `json:"-"`
	AccessToken  string    `json:"-"`
	RefreshToken string    `json:"-"`
	DeviceLabel  string    `json:"device_label,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// CreateQrLogin mints a fresh pending handshake code.
func CreateQrLogin(code string, deviceLabel string) error {
	_, err := database.DB.Exec(
		`INSERT INTO qr_logins (code, status, device_label, expires_at)
		 VALUES ($1, 'pending', $2, NOW() + ($3 || ' seconds')::interval)
		 ON CONFLICT (code) DO UPDATE
		   SET status = 'pending', user_id = NULL, access_token = '', refresh_token = '',
		       device_label = EXCLUDED.device_label, created_at = NOW(), expires_at = EXCLUDED.expires_at`,
		code, deviceLabel, int(qrLoginTTL.Seconds()),
	)
	if err != nil {
		return fmt.Errorf("failed to create qr login: %w", err)
	}
	return nil
}

// GetQrLogin returns the current state of a handshake, treating lapsed rows
// as expired without rewriting them.
func GetQrLogin(code string) (*QrLogin, error) {
	q := &QrLogin{}
	err := database.DB.QueryRow(
		`SELECT code, status, COALESCE(user_id, 0), access_token, refresh_token,
		        device_label, created_at, expires_at
		 FROM qr_logins WHERE code = $1`, code,
	).Scan(&q.Code, &q.Status, &q.UserID, &q.AccessToken, &q.RefreshToken, &q.DeviceLabel, &q.CreatedAt, &q.ExpiresAt)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get qr login: %w", err)
	}
	if time.Now().After(q.ExpiresAt) {
		q.Status = "expired"
	}
	return q, nil
}

// ConfirmQrLogin atomically attaches the approving user's tokens to a still
// pending handshake. Returns ErrConflict when the code was already consumed.
func ConfirmQrLogin(code string, userID int64, accessToken, refreshToken string) error {
	res, err := database.DB.Exec(
		`UPDATE qr_logins SET user_id = $2, status = 'confirmed',
		        access_token = $3, refresh_token = $4
		 WHERE code = $1 AND status = 'pending' AND expires_at > NOW()`,
		code, userID, accessToken, refreshToken,
	)
	if err != nil {
		return fmt.Errorf("failed to confirm qr login: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return ErrConflict
	}
	return nil
}

// PurgeExpiredQrLogins removes stale handshake rows.
func PurgeExpiredQrLogins() error {
	_, err := database.DB.Exec("DELETE FROM qr_logins WHERE expires_at <= NOW() - interval '1 hour'")
	return err
}
