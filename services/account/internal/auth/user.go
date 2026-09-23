package auth

import (
	"fmt"

	"github.com/openfield/server/pkg/database"
	"github.com/openfield/server/pkg/model"
	"github.com/openfield/server/pkg/repository"
)

const (
	RoleUser  = "user"
	RoleAdmin = "admin"

	// MaxOAuth2Accounts is the default per-identity limit on how many
	// OpenField accounts one OAuth identity may be bound to (multi-account
	// login). When the quota is full, the account picker hides "add new".
	MaxOAuth2Accounts = 5
)

// ErrOAuth2AlreadyBound is returned when an OAuth2 identity is already linked
// to a different user account.
var ErrOAuth2AlreadyBound = fmt.Errorf("oauth2 identity already bound to another account")

// ErrOAuth2QuotaExceeded is returned when binding or adding an account would
// exceed the per-identity account quota (MaxOAuth2Accounts).
var ErrOAuth2QuotaExceeded = fmt.Errorf("oauth2 identity account quota exceeded")

// findUsersByOAuth2 returns every non-deleted user bound to the given OAuth2
// identity (provider + provider-specific id). An empty slice (never nil) means
// the identity is not bound to any account yet.
func findUsersByOAuth2(provider, oauth2ID string) ([]*model.User, error) {
	return repository.NewUserRepository().FindByOAuth2(provider, oauth2ID)
}

// countUsersByOAuth2 counts the non-deleted accounts bound to an OAuth2
// identity, used to enforce the multi-account quota.
func countUsersByOAuth2(provider, oauth2ID string) (int, error) {
	return repository.NewUserRepository().CountOAuth2Accounts(provider, oauth2ID)
}

// createUserFromOAuth2 creates a new user from OAuth2 user info.
// New users must complete registration (username/nickname) before using the app.
// The provisional username is derived from the identity but made unique with a
// numeric suffix: with multi-account login the same identity may provision
// several accounts, and the users.username column is globally unique.
func createUserFromOAuth2(info *UserInfo) (*model.User, error) {
	role := RoleUser
	var count int64
	err := database.DB.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
	if err != nil {
		return nil, fmt.Errorf("failed to count users: %w", err)
	}
	// The first user in the system becomes an administrator.
	if count == 0 {
		role = RoleAdmin
	}

	username := info.Username
	if username == "" {
		username = info.Email
	}
	if username == "" {
		username = info.OAuth2ID
	}

	var user model.User
	for attempt := 1; attempt <= 100; attempt++ {
		probe := username
		if attempt > 1 {
			probe = fmt.Sprintf("%s%d", username, attempt)
		}
		err = database.DB.QueryRow(
			"INSERT INTO users (username, nickname, email, avatar_url, role, needs_registration, oauth2_provider, oauth2_id, oauth2_username) VALUES ($1, $2, $3, $4, $5, TRUE, $6, $7, $8) RETURNING id, username, nickname, email, avatar_url, banner_url, role, needs_registration, oauth2_provider, oauth2_id, oauth2_username, created_at, updated_at",
			probe, info.Username, info.Email, info.AvatarURL, role, "oidc", info.OAuth2ID, info.Username,
		).Scan(&user.ID, &user.Username, &user.Nickname, &user.Email, &user.AvatarURL, &user.BannerURL, &user.Role, &user.NeedsRegistration, &user.OAuth2Provider, &user.OAuth2ID, &user.OAuth2Username, &user.CreatedAt, &user.UpdatedAt)
		if err == nil {
			break
		}
		if repository.IsUniqueViolation(err) {
			continue
		}
		return nil, fmt.Errorf("failed to create user: %w", err)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to create user after retries: %w", err)
	}
	// New users automatically join the default "everyone" group (full permissions).
	_ = repository.NewPermissionRepository().EnsureUserInDefaultGroup(user.ID)
	return &user, nil
}
