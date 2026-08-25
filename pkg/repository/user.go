package repository

import (
	"database/sql"
	"fmt"
	"time"

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

const userColumns = "id, username, nickname, email, avatar_url, banner_url, role, password_hash, needs_registration, bio, is_verified, storage_quota, storage_bucket, oauth2_provider, oauth2_id, oauth2_username, verified_note, verified_by, e2ee_public_key, exp, last_daily_bonus_at, checkin_streak, pin_hash, region, lang, member_level, member_expires_at, auto_renew, name_color, name_color_to, name_dynamic, name_colors, name_gradient_direction, avatar_frame, status, banned_until, hide_follow_lists, created_at, updated_at"

func scanUser(row interface{ Scan(...any) error }) (*model.User, error) {
	user := &model.User{}
	err := row.Scan(&user.ID, &user.Username, &user.Nickname, &user.Email, &user.AvatarURL, &user.BannerURL, &user.Role, &user.PasswordHash, &user.NeedsRegistration, &user.Bio, &user.IsVerified, &user.StorageQuota, &user.StorageBucket, &user.OAuth2Provider, &user.OAuth2ID, &user.OAuth2Username, &user.VerifiedNote, &user.VerifiedBy, &user.E2EEPublicKey, &user.Exp, &user.LastDailyBonusAt, &user.CheckinStreak, &user.PinHash, &user.Region, &user.Lang, &user.MemberLevel, &user.MemberExpiresAt, &user.AutoRenew, &user.NameColor, &user.NameColorTo, &user.NameDynamic, &user.NameColors, &user.NameGradientDirection, &user.AvatarFrame, &user.Status, &user.BannedUntil, &user.HideFollowLists, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		return nil, err
	}
	user.Level = model.LevelForExp(user.Exp)
	user.HasPin = user.PinHash != ""
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

// UpdateNameStyle updates a user's display-name styling (color list, gradient
// direction, the dynamic flag) plus the reserved avatar frame. Returns the
// updated user.
func (r *UserRepository) UpdateNameStyle(userID int64, colors model.NameColorList, direction string, dynamic bool, avatarFrame string) (*model.User, error) {
	color := ""
	var colorTo string
	if len(colors) > 0 {
		color = colors[0]
	}
	if len(colors) > 1 {
		colorTo = colors[1]
	}
	user, err := scanUser(database.DB.QueryRow(
		"UPDATE users SET name_color = $2, name_color_to = $3, name_dynamic = $4, name_colors = $5, name_gradient_direction = $6, avatar_frame = $7, updated_at = NOW() WHERE id = $1 RETURNING "+userColumns,
		userID, color, colorTo, dynamic, colors, direction, avatarFrame,
	))
	if err != nil {
		return nil, fmt.Errorf("failed to update name style: %w", err)
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

// SetStorageBucket moves a user to a logical storage bucket and applies that
// bucket's default quota. Existing attachments are untouched: files already
// uploaded to the previous bucket stay where they are (their records keep the
// original bucket so deletes resolve to the right physical store).
func (r *UserRepository) SetStorageBucket(userID int64, bucket string, quota int64) (*model.User, error) {
	user, err := scanUser(database.DB.QueryRow(
		"UPDATE users SET storage_bucket = $2, storage_quota = $3, updated_at = NOW() WHERE id = $1 RETURNING "+userColumns,
		userID, bucket, quota,
	))
	if err != nil {
		return nil, fmt.Errorf("failed to set storage bucket: %w", err)
	}
	return user, nil
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

// SetE2EEPublicKey publishes the user's X25519 public key used for
// end-to-end-encrypted conversations. Passing an empty string removes it.
func (r *UserRepository) SetE2EEPublicKey(userID int64, publicKey string) error {
	if _, err := database.DB.Exec(
		"UPDATE users SET e2ee_public_key = $2, updated_at = NOW() WHERE id = $1",
		userID, publicKey,
	); err != nil {
		return fmt.Errorf("failed to set e2ee public key: %w", err)
	}
	return nil
}

// UpdateLocale updates the user's region preference and server-message display
// language. The region name drives the client's timezone and language defaults;
// the lang override is stored so server-pushed notifications can be localized
// per user.
func (r *UserRepository) UpdateLocale(userID int64, region, lang string) (*model.User, error) {
	user, err := scanUser(database.DB.QueryRow(
		"UPDATE users SET region = $2, lang = $3, updated_at = NOW() WHERE id = $1 RETURNING "+userColumns,
		userID, region, lang,
	))
	if err != nil {
		return nil, fmt.Errorf("failed to update locale: %w", err)
	}
	return user, nil
}

// SetHideFollowLists toggles whether the user's followers/following/friends
// lists are hidden from everyone except the user themself.
func (r *UserRepository) SetHideFollowLists(userID int64, hide bool) (*model.User, error) {
	user, err := scanUser(database.DB.QueryRow(
		"UPDATE users SET hide_follow_lists = $2, updated_at = NOW() WHERE id = $1 RETURNING "+userColumns,
		userID, hide,
	))
	if err != nil {
		return nil, fmt.Errorf("failed to update follow-list privacy: %w", err)
	}
	return user, nil
}

// AdjustExp is an admin-only manual adjustment of a user's experience points.
func (r *UserRepository) AdjustExp(userID int64, delta int64) (int64, error) {
	var exp int64
	err := database.DB.QueryRow(
		"UPDATE users SET exp = GREATEST(0, exp + $2), updated_at = NOW() WHERE id = $1 RETURNING exp",
		userID, delta,
	).Scan(&exp)
	if err != nil {
		return 0, fmt.Errorf("failed to adjust exp: %w", err)
	}
	return exp, nil
}

// GetPinHash returns the bcrypt hash of the user's payment PIN ("" when unset).
func (r *UserRepository) GetPinHash(userID int64) (string, error) {
	var hash string
	if err := database.DB.QueryRow(
		"SELECT COALESCE(pin_hash, '') FROM users WHERE id = $1", userID,
	).Scan(&hash); err != nil {
		if err == sql.ErrNoRows {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("failed to get pin hash: %w", err)
	}
	return hash, nil
}

// SetPinHash stores the bcrypt hash of the user's payment PIN.
func (r *UserRepository) SetPinHash(userID int64, hash string) error {
	if _, err := database.DB.Exec(
		"UPDATE users SET pin_hash = $2, updated_at = NOW() WHERE id = $1",
		userID, hash,
	); err != nil {
		return fmt.Errorf("failed to set pin hash: %w", err)
	}
	return nil
}

// isSameUTCDay reports whether two timestamps fall on the same calendar day
// under [loc]. Used to gate the daily login bonus.
func isSameUTCDay(a, b time.Time, loc *time.Location) bool {
	ay, am, ad := a.In(loc).Date()
	by, bm, bd := b.In(loc).Date()
	return ay == by && am == bm && ad == bd
}

// GetUsersByIDs retrieves multiple users by their IDs.
func (r *UserRepository) GetUsersByIDs(ids []int64) (map[int64]*model.User, error) {
	if len(ids) == 0 {
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
