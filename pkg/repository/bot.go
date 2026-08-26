package repository

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/openfield/server/pkg/database"
	"github.com/openfield/server/pkg/model"
)

// Bot account storage: ownership queries plus static API-token management.
//
// Tokens are opaque ofb_... strings; only their SHA-256 hash is stored, so a
// leaked database dump never yields usable credentials and the plaintext is
// shown exactly once at creation/regeneration.

// BotTokenPrefix marks static bot API tokens so the gateway can tell them
// apart from JWTs by inspection alone.
const BotTokenPrefix = "ofb_"

// MaxBotsPerOwner caps how many bot accounts one human user may own.
const MaxBotsPerOwner = 10

// ErrBotNotFound is returned when a bot id does not exist.
var ErrBotNotFound = errors.New("bot not found")

// ErrBotNotOwned is returned when the bot exists but belongs to someone else.
var ErrBotNotOwned = errors.New("bot not owned by requester")

// randomTokenHex returns n cryptographically random bytes as lowercase hex.
func randomTokenHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// hashBotToken derives the stored hash of a plaintext bot token.
func hashBotToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateBot inserts a bot user row owned by ownerID and returns it. The
// password hash is random garbage: no password ever matches it, so the bot
// cannot log in interactively.
func (r *UserRepository) CreateBot(username, nickname string, ownerID int64) (*model.User, error) {
	unusablePassword, err := randomTokenHex(24)
	if err != nil {
		return nil, fmt.Errorf("failed to generate bot secret: %w", err)
	}
	user, err := scanUser(database.DB.QueryRow(
		"INSERT INTO users (username, nickname, role, needs_registration, password_hash, is_bot, bot_owner_id) VALUES ($1, $2, 'user', FALSE, $3, TRUE, $4) RETURNING "+userColumns,
		username, nickname, unusablePassword, ownerID,
	))
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrUsernameTaken
		}
		return nil, fmt.Errorf("failed to create bot: %w", err)
	}
	return user, nil
}

// CountBotsOwned returns how many bots the owner currently has.
func CountBotsOwned(ownerID int64) (int, error) {
	var count int
	err := database.DB.QueryRow(
		"SELECT COUNT(*) FROM users WHERE is_bot AND bot_owner_id = $1", ownerID).Scan(&count)
	return count, err
}

// RequesterIsBot reports whether userID is itself a bot account. Unknown ids
// fail closed (treated as bots).
func RequesterIsBot(userID int64) bool {
	var isBot bool
	err := database.DB.QueryRow(
		"SELECT is_bot FROM users WHERE id = $1", userID).Scan(&isBot)
	if err != nil {
		return true
	}
	return isBot
}

// BotListItem is what the owner-facing listing returns per bot.
type BotListItem struct {
	ID             int64     `json:"id"`
	Username       string    `json:"username"`
	Nickname       string    `json:"nickname"`
	AvatarURL      string    `json:"avatar_url"`
	CreatedAt      time.Time `json:"created_at"`
	TokenCreatedAt time.Time `json:"token_created_at"`
}

// ListBotsByOwner returns the owner's bots with their token timestamps.
func ListBotsByOwner(ownerID int64) ([]BotListItem, error) {
	rows, err := database.DB.Query(
		`SELECT u.id, u.username, u.nickname, u.avatar_url, u.created_at,
		        COALESCE(bt.created_at, u.created_at)
		 FROM users u
		 LEFT JOIN bot_tokens bt ON bt.user_id = u.id
		 WHERE u.is_bot AND u.bot_owner_id = $1
		 ORDER BY u.id ASC`, ownerID)
	if err != nil {
		return nil, fmt.Errorf("failed to list bots: %w", err)
	}
	defer rows.Close()

	bots := make([]BotListItem, 0)
	for rows.Next() {
		var b BotListItem
		if err := rows.Scan(&b.ID, &b.Username, &b.Nickname, &b.AvatarURL, &b.CreatedAt, &b.TokenCreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan bot row: %w", err)
		}
		bots = append(bots, b)
	}
	return bots, rows.Err()
}

// CheckBotOwnership verifies botID is an existing bot owned by ownerID.
func CheckBotOwnership(botID, ownerID int64) error {
	var dbOwner int64
	err := database.DB.QueryRow(
		"SELECT bot_owner_id FROM users WHERE id = $1 AND is_bot", botID).Scan(&dbOwner)
	if err == sql.ErrNoRows {
		return ErrBotNotFound
	}
	if err != nil {
		return fmt.Errorf("failed to load bot: %w", err)
	}
	if dbOwner != ownerID {
		return ErrBotNotOwned
	}
	return nil
}

// DeleteBot removes the bot user row (messages/posts cascade with it).
// Returns ErrBotNotFound when nothing was deleted.
func DeleteBot(botID, ownerID int64) error {
	res, err := database.DB.Exec(
		"DELETE FROM users WHERE id = $1 AND is_bot AND bot_owner_id = $2", botID, ownerID)
	if err != nil {
		return fmt.Errorf("failed to delete bot: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrBotNotFound
	}
	return nil
}

// IssueBotToken generates a fresh ofb_ token for the bot and persists only
// its SHA-256 hash, replacing any previous token atomically.
func IssueBotToken(botID int64) (string, error) {
	raw, err := randomTokenHex(32)
	if err != nil {
		return "", err
	}
	token := BotTokenPrefix + raw
	_, err = database.DB.Exec(
		`INSERT INTO bot_tokens (user_id, token_hash, created_at, last_used_at)
		 VALUES ($1, $2, NOW(), NULL)
		 ON CONFLICT (user_id) DO UPDATE SET token_hash = EXCLUDED.token_hash, created_at = NOW(), last_used_at = NULL`,
		botID, hashBotToken(token))
	if err != nil {
		return "", fmt.Errorf("failed to store bot token: %w", err)
	}
	return token, nil
}

// ResolveBotToken validates a plaintext ofb_ token and returns the owning
// bot's user id. Called by the gateway on every request carrying such a
// token; a single indexed lookup keeps the cost negligible. Banned bots are
// rejected, mirroring interactive-login ban semantics.
func ResolveBotToken(token string) (int64, bool) {
	if len(token) <= len(BotTokenPrefix) {
		return 0, false
	}
	var userID int64
	var status string
	var bannedUntil sql.NullTime
	err := database.DB.QueryRow(
		`SELECT bt.user_id, u.status, u.banned_until
		 FROM bot_tokens bt
		 JOIN users u ON u.id = bt.user_id
		 WHERE bt.token_hash = $1 AND u.is_bot`,
		hashBotToken(token),
	).Scan(&userID, &status, &bannedUntil)
	if err != nil {
		return 0, false
	}
	now := time.Now()
	if status == "banned" && (!bannedUntil.Valid || now.Before(bannedUntil.Time)) {
		return 0, false
	}
	// Best-effort activity stamp; failure never blocks the request.
	_, _ = database.DB.Exec("UPDATE bot_tokens SET last_used_at = NOW() WHERE user_id = $1", userID)
	return userID, true
}
