package repository

import (
	"database/sql"
	"fmt"

	"github.com/lib/pq"
	"github.com/openfield/server/pkg/database"
	"github.com/openfield/server/pkg/model"
)

// UserRepository handles user-related database operations.
type UserRepository struct{}

// NewUserRepository creates a new UserRepository.
func NewUserRepository() *UserRepository {
	return &UserRepository{}
}

const userColumns = "id, username, nickname, email, avatar_url, banner_url, role, password_hash, needs_registration, bio, is_verified, storage_quota, oauth2_provider, oauth2_id, oauth2_username, verified_note, verified_by, created_at, updated_at"

func scanUser(row interface{ Scan(...any) error }) (*model.User, error) {
	user := &model.User{}
	err := row.Scan(&user.ID, &user.Username, &user.Nickname, &user.Email, &user.AvatarURL, &user.BannerURL, &user.Role, &user.PasswordHash, &user.NeedsRegistration, &user.Bio, &user.IsVerified, &user.StorageQuota, &user.OAuth2Provider, &user.OAuth2ID, &user.OAuth2Username, &user.VerifiedNote, &user.VerifiedBy, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return user, nil
}

// GetByID retrieves a user by ID.
func (r *UserRepository) GetByID(id int64) (*model.User, error) {
	user, err := scanUser(database.DB.QueryRow("SELECT "+userColumns+" FROM users WHERE id = $1", id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return user, nil
}

// GetByUsername retrieves a user by username.
func (r *UserRepository) GetByUsername(username string) (*model.User, error) {
	user, err := scanUser(database.DB.QueryRow("SELECT "+userColumns+" FROM users WHERE username = $1", username))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return user, nil
}

// GetByEmail retrieves a user by email.
func (r *UserRepository) GetByEmail(email string) (*model.User, error) {
	user, err := scanUser(database.DB.QueryRow("SELECT "+userColumns+" FROM users WHERE email = $1", email))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return user, nil
}

// UpdateProfile updates a user's registration profile (username, nickname, bio).
// Returns the updated user or a conflict error if username is taken.
func (r *UserRepository) UpdateProfile(userID int64, username, nickname, bio string) (*model.User, error) {
	user, err := scanUser(database.DB.QueryRow(
		"UPDATE users SET username = $2, nickname = $3, bio = $4, needs_registration = FALSE, updated_at = NOW() WHERE id = $1 RETURNING "+userColumns,
		userID, username, nickname, bio,
	))
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrUsernameTaken
		}
		return nil, fmt.Errorf("failed to update profile: %w", err)
	}
	return user, nil
}

// UpdateAvatar updates a user's avatar URL.
func (r *UserRepository) UpdateAvatar(userID int64, avatarURL string) error {
	_, err := database.DB.Exec("UPDATE users SET avatar_url = $2, updated_at = NOW() WHERE id = $1", userID, avatarURL)
	if err != nil {
		return fmt.Errorf("failed to update avatar: %w", err)
	}
	return nil
}

// UpdateBanner updates a user's banner URL.
func (r *UserRepository) UpdateBanner(userID int64, bannerURL string) error {
	_, err := database.DB.Exec("UPDATE users SET banner_url = $2, updated_at = NOW() WHERE id = $1", userID, bannerURL)
	if err != nil {
		return fmt.Errorf("failed to update banner: %w", err)
	}
	return nil
}

// SetPassword sets a user's password hash (for local password login).
func (r *UserRepository) SetPassword(userID int64, passwordHash string) error {
	_, err := database.DB.Exec("UPDATE users SET password_hash = $2, updated_at = NOW() WHERE id = $1", userID, passwordHash)
	if err != nil {
		return fmt.Errorf("failed to set password: %w", err)
	}
	return nil
}

// SetStorageQuota updates a user's storage quota in bytes.
func (r *UserRepository) SetStorageQuota(userID int64, quota int64) error {
	_, err := database.DB.Exec("UPDATE users SET storage_quota = $2, updated_at = NOW() WHERE id = $1", userID, quota)
	if err != nil {
		return fmt.Errorf("failed to set storage quota: %w", err)
	}
	return nil
}

// BindOAuth links an OAuth2 identity to an existing user account.
func (r *UserRepository) BindOAuth(userID int64, provider, oauth2ID, oauth2Username string) (*model.User, error) {
	user, err := scanUser(database.DB.QueryRow(
		"UPDATE users SET oauth2_provider = $2, oauth2_id = $3, oauth2_username = $4, updated_at = NOW() WHERE id = $1 RETURNING "+userColumns,
		userID, provider, oauth2ID, oauth2Username,
	))
	if err != nil {
		return nil, fmt.Errorf("failed to bind oauth identity: %w", err)
	}
	return user, nil
}

// GetUsersByIDs retrieves multiple users by their IDs.
func (r *UserRepository) GetUsersByIDs(ids []int64) (map[int64]*model.User, error) {	if len(ids) == 0 {
		return map[int64]*model.User{}, nil
	}

	rows, err := database.DB.Query("SELECT "+userColumns+" FROM users WHERE id = ANY($1)", pq.Array(ids))
	if err != nil {
		return nil, fmt.Errorf("failed to query users: %w", err)
	}
	defer rows.Close()

	users := make(map[int64]*model.User)
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan user: %w", err)
		}
		users[user.ID] = user
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}

	return users, nil
}

// Search finds users by username or nickname, returning up to `limit` results.
func (r *UserRepository) Search(query string, limit int) ([]model.User, error) {
	if limit < 1 {
		limit = 20
	}
	pattern := "%" + query + "%"
	rows, err := database.DB.Query(
		"SELECT "+userColumns+" FROM users WHERE username ILIKE $1 OR nickname ILIKE $1 ORDER BY username ASC LIMIT $2",
		pattern, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to search users: %w", err)
	}
	defer rows.Close()

	users := make([]model.User, 0)
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan user: %w", err)
		}
		users = append(users, *user)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return users, nil
}
