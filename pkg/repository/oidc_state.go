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

// IssueOIDCState stores a fresh single-use login-state nonce together with
// the requested redirect flow ("app" or "web"). The callback uses the flow to
// pick the matching redirect target so web sign-ins do not bounce into the
// openfield:// app protocol.
func IssueOIDCState(state, flow string) error {
	if flow != "web" {
		flow = "app"
	}
	_, err := database.DB.Exec(
		"INSERT INTO oidc_states (state, purpose, flow, expires_at) "+
			"VALUES ($1, 'login', $2, NOW() + ($3 || ' seconds')::interval) "+
			"ON CONFLICT (state) DO UPDATE SET flow = EXCLUDED.flow, expires_at = EXCLUDED.expires_at",
		state, flow, int(oidcLoginStateTTL.Seconds()),
	)
	return err
}

// ConsumeOIDCState validates the nonce and returns the redirect flow chosen
// by the login that issued it. The flow is consumed atomically with the
// deletion so it cannot be replayed.
func ConsumeOIDCState(state string) (flow string, err error) {
	var stored string
	res, err := database.DB.Query(
		"DELETE FROM oidc_states WHERE state = $1 AND purpose = 'login' AND expires_at > NOW() RETURNING flow",
		state,
	)
	if err != nil {
		return "", err
	}
	if res.Next() {
		if err := res.Scan(&stored); err != nil {
			res.Close()
			return "", err
		}
	} else {
		res.Close()
		return "", ErrNotFound
	}
	res.Close()
	if stored == "web" {
		return "web", nil
	}
	return "app", nil
}

// PurgeExpiredOIDCStates removes expired rows. Safe to run concurrently.
func PurgeExpiredOIDCStates() error {
	_, err := database.DB.Exec("DELETE FROM oidc_states WHERE expires_at <= NOW()")
	return err
}
