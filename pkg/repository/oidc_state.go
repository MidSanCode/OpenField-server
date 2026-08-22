package repository

import (
	"time"

	"github.com/openfield/server/pkg/database"
)

// Single-use OIDC authorization states. Every login attempt first obtains a
// random nonce stored server-side; the callback accepts the code exchange only
// when it carries such a nonce, which prevents login CSRF (an attacker tricking
// a victim's browser into completing an OAuth login with the attacker's code)
// and replay of old callbacks.

const oidcLoginStateTTL = 10 * time.Minute

// IssueOIDCState stores a fresh single-use login-state nonce.
func IssueOIDCState(state string) error {
	_, err := database.DB.Exec(
		"INSERT INTO oidc_states (state, purpose, expires_at) "+
			"VALUES ($1, 'login', NOW() + ($2 || ' seconds')::interval) "+
			"ON CONFLICT (state) DO UPDATE SET expires_at = EXCLUDED.expires_at",
		state, int(oidcLoginStateTTL.Seconds()),
	)
	return err
}

// ConsumeOIDCState validates that the nonce was issued for a login and has not
// expired, then deletes it so it cannot be replayed. Returns ErrNotFound when
// the state is unknown, already used, or expired.
func ConsumeOIDCState(state string) error {
	res, err := database.DB.Exec(
		"DELETE FROM oidc_states WHERE state = $1 AND purpose = 'login' AND expires_at > NOW()",
		state,
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
	return nil
}

// PurgeExpiredOIDCStates removes expired rows. Safe to run concurrently.
func PurgeExpiredOIDCStates() error {
	_, err := database.DB.Exec("DELETE FROM oidc_states WHERE expires_at <= NOW()")
	return err
}
