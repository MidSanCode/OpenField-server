package auth

import (
	"database/sql"
	"fmt"

	"github.com/openfield/server/pkg/database"
	"github.com/openfield/server/pkg/model"
	"github.com/openfield/server/pkg/repository"
)

const (
	RoleUser  = "user"
	RoleAdmin = "admin"
)

// ErrOAuth2AlreadyBound is returned when an OAuth2 identity is already linked
// to a different user account.
var ErrOAuth2AlreadyBound = fmt.Errorf("oauth2 identity already bound to another account")

// findUserByOAuth2 finds a user by OAuth2 provider and provider-specific user ID.
// Returns (nil, nil) when no matching user exists.
func findUserByOAuth2(provider, oauth2ID string) (*model.User, error) {
	user := &model.User{}
	err := database.DB.QueryRow(
		"SELECT id, username, nickname, email, avatar_url, banner_url, role, needs_registration, oauth2_provider, oauth2_id, created_at, updated_at FROM users WHERE oauth2_provider = $1 AND oauth2_id = $2",
		provider, oauth2ID,
	).Scan(&user.ID, &user.Username, &user.Nickname, &user.Email, &user.AvatarURL, &user.BannerURL, &user.Role, &user.NeedsRegistration, &user.OAuth2Provider, &user.OAuth2ID, &user.CreatedAt, &user.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query user: %w", err)
	}
	return user, nil
}

// createUserFromOAuth2 creates a new user from OAuth2 user info.
// New users must complete registration (username/nickname) before using the app.
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
	err = database.DB.QueryRow(
		"INSERT INTO users (username, nickname, email, avatar_url, role, needs_registration, oauth2_provider, oauth2_id) VALUES ($1, $2, $3, $4, $5, TRUE, $6, $7) RETURNING id, username, nickname, email, avatar_url, banner_url, role, needs_registration, oauth2_provider, oauth2_id, created_at, updated_at",
		username, info.Username, info.Email, info.AvatarURL, role, "oidc", info.OAuth2ID,
	).Scan(&user.ID, &user.Username, &user.Nickname, &user.Email, &user.AvatarURL, &user.BannerURL, &user.Role, &user.NeedsRegistration, &user.OAuth2Provider, &user.OAuth2ID, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to create user: %w", err)
	}
	// New users automatically join the default "everyone" group (full permissions).
	_ = repository.NewPermissionRepository().EnsureUserInDefaultGroup(user.ID)
	return &user, nil
}
