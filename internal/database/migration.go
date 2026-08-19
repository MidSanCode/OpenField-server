package database

import (
	"fmt"

	"github.com/openfield/server/internal/logger"
)

// RunMigrations creates the required database tables if they don't exist.
func RunMigrations() error {
	migrations := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id BIGSERIAL PRIMARY KEY,
			username VARCHAR(255) NOT NULL,
			email VARCHAR(255) NOT NULL DEFAULT '',
			avatar_url TEXT DEFAULT '',
			banner_url TEXT DEFAULT '',
			nickname VARCHAR(255) NOT NULL DEFAULT '',
			role VARCHAR(20) NOT NULL DEFAULT 'user',
			password_hash VARCHAR(255) NOT NULL DEFAULT '',
			needs_registration BOOLEAN NOT NULL DEFAULT TRUE,
			oauth2_provider VARCHAR(50) NOT NULL DEFAULT '',
			oauth2_id VARCHAR(255) NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`ALTER TABLE users DROP CONSTRAINT IF EXISTS users_email_key`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS banner_url TEXT DEFAULT ''`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS nickname VARCHAR(255) NOT NULL DEFAULT ''`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS role VARCHAR(20) NOT NULL DEFAULT 'user'`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS password_hash VARCHAR(255) NOT NULL DEFAULT ''`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS needs_registration BOOLEAN NOT NULL DEFAULT TRUE`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS storage_quota BIGINT NOT NULL DEFAULT 104857600`,
		`ALTER TABLE users ALTER COLUMN email TYPE VARCHAR(255)`,
		`ALTER TABLE users ALTER COLUMN email SET DEFAULT ''`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_users_username ON users(username)`,
		`CREATE TABLE IF NOT EXISTS attachments (
			id BIGSERIAL PRIMARY KEY,
			user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			object_key VARCHAR(255) NOT NULL,
			original_name VARCHAR(255) NOT NULL DEFAULT '',
			mime_type VARCHAR(100) NOT NULL DEFAULT '',
			size_bytes BIGINT NOT NULL DEFAULT 0,
			url TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS post_attachments (
			post_id BIGINT NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
			attachment_id BIGINT NOT NULL REFERENCES attachments(id) ON DELETE CASCADE,
			PRIMARY KEY (post_id, attachment_id)
		)`,
		`CREATE TABLE IF NOT EXISTS posts (
			id BIGSERIAL PRIMARY KEY,
			user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			content TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS messages (
			id BIGSERIAL PRIMARY KEY,
			sender_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			receiver_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			content TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`ALTER TABLE messages ADD COLUMN IF NOT EXISTS conversation_id BIGINT REFERENCES conversations(id) ON DELETE CASCADE`,
		`ALTER TABLE messages ADD COLUMN IF NOT EXISTS reply_to_id BIGINT REFERENCES messages(id) ON DELETE SET NULL`,
		`ALTER TABLE messages ADD COLUMN IF NOT EXISTS edited_at TIMESTAMPTZ`,
		`ALTER TABLE messages ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ`,
		`CREATE TABLE IF NOT EXISTS refresh_tokens (
			id BIGSERIAL PRIMARY KEY,
			user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			token VARCHAR(255) NOT NULL UNIQUE,
			expires_at TIMESTAMPTZ NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_users_username ON users(username)`,
		`CREATE INDEX IF NOT EXISTS idx_posts_user_id ON posts(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_posts_created_at ON posts(created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_sender_receiver ON messages(sender_id, receiver_id)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_created_at ON messages(created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_refresh_tokens_user_id ON refresh_tokens(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_refresh_tokens_token ON refresh_tokens(token)`,
		`CREATE INDEX IF NOT EXISTS idx_attachments_user_id ON attachments(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_attachments_object_key ON attachments(object_key)`,
		`CREATE INDEX IF NOT EXISTS idx_post_attachments_attachment ON post_attachments(attachment_id)`,
		`ALTER TABLE attachments ADD COLUMN IF NOT EXISTS sha256 VARCHAR(64) NOT NULL DEFAULT ''`,
		`CREATE INDEX IF NOT EXISTS idx_attachments_sha256 ON attachments(sha256)`,
	}

	for _, m := range migrations {
		if _, err := DB.Exec(m); err != nil {
			return fmt.Errorf("migration failed: %w", err)
		}
	}

	logger.Log.Info("database migrations completed")
	return nil
}
