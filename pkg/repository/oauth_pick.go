package repository

import (
	"database/sql"
	"errors"
	"time"

	"github.com/openfield/server/pkg/database"
)

// Single-use OAuth2 account-pick tickets. When one OIDC identity is bound to
// several OpenField accounts (multi-account login), the callback cannot decide
// which account to sign in: the OAuth code is single-use, so the identity is
// parked server-side behind a short-lived ticket. The client then lists the
// bound accounts (or adds a new one, quota permitting) and completes login
// through the pick endpoints.

const oauthPickTTL = 10 * time.Minute

// OAuth2Pick is a pending multi-account login: the OAuth identity and the
// redirect flow the callback would have used.
type OAuth2Pick struct {
	Ticket         string
	Provider       string
	OAuth2ID       string
	OAuth2Username string
	Email          string
	AvatarURL      string
}

// IssueOAuth2Pick stores a fresh pick ticket for an OAuth identity that maps
// to several accounts. The ticket expires after oauthPickTTL.
func IssueOAuth2Pick(ticket, provider, oauth2ID, oauth2Username, email, avatarURL string) error {
	_, err := database.DB.Exec(
		"INSERT INTO oauth2_picks (ticket, provider, oauth2_id, oauth2_username, email, avatar_url, expires_at) "+
			"VALUES ($1, $2, $3, $4, $5, $6, NOW() + ($7 || ' seconds')::interval)",
		ticket, provider, oauth2ID, oauth2Username, email, avatarURL, int(oauthPickTTL.Seconds()),
	)
	return err
}

// GetOAuth2Pick reads a pick ticket without consuming it, so the client can
// re-fetch the account list. Returns ErrNotFound when the ticket is unknown
// or has expired.
func GetOAuth2Pick(ticket string) (*OAuth2Pick, error) {
	var pick OAuth2Pick
	err := database.DB.QueryRow(
		"SELECT ticket, provider, oauth2_id, oauth2_username, email, avatar_url FROM oauth2_picks WHERE ticket = $1 AND expires_at > NOW()",
		ticket,
	).Scan(&pick.Ticket, &pick.Provider, &pick.OAuth2ID, &pick.OAuth2Username, &pick.Email, &pick.AvatarURL)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &pick, nil
}

// ConsumeOAuth2Pick atomically validates and deletes a pick ticket, returning
// the parked identity. The ticket is single-use: select/create consume it so
// an old ticket cannot mint tokens twice.
func ConsumeOAuth2Pick(ticket string) (*OAuth2Pick, error) {
	var pick OAuth2Pick
	err := database.DB.QueryRow(
		"DELETE FROM oauth2_picks WHERE ticket = $1 AND expires_at > NOW() "+
			"RETURNING ticket, provider, oauth2_id, oauth2_username, email, avatar_url",
		ticket,
	).Scan(&pick.Ticket, &pick.Provider, &pick.OAuth2ID, &pick.OAuth2Username, &pick.Email, &pick.AvatarURL)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &pick, nil
}

// PurgeExpiredOAuth2Picks removes expired tickets. Safe to run concurrently.
func PurgeExpiredOAuth2Picks() error {
	_, err := database.DB.Exec("DELETE FROM oauth2_picks WHERE expires_at <= NOW()")
	return err
}
