package database

import (
	"fmt"

	"github.com/lib/pq"
	"github.com/openfield/server/pkg/config"
	"github.com/openfield/server/pkg/logger"
	"github.com/openfield/server/pkg/permission"
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
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS bio TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS is_verified BOOLEAN NOT NULL DEFAULT FALSE`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS oauth2_username VARCHAR(255) NOT NULL DEFAULT ''`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS verified_note TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS verified_by VARCHAR(255) NOT NULL DEFAULT ''`,
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
		`ALTER TABLE attachments ADD COLUMN IF NOT EXISTS visibility VARCHAR(20) NOT NULL DEFAULT 'public'`,
		`ALTER TABLE attachments ADD COLUMN IF NOT EXISTS thumb_url TEXT NOT NULL DEFAULT ''`,
		`CREATE TABLE IF NOT EXISTS posts (
			id BIGSERIAL PRIMARY KEY,
			user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			content TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS post_attachments (
			post_id BIGINT NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
			attachment_id BIGINT NOT NULL REFERENCES attachments(id) ON DELETE CASCADE,
			PRIMARY KEY (post_id, attachment_id)
		)`,
		`CREATE TABLE IF NOT EXISTS post_replies (
			id BIGSERIAL PRIMARY KEY,
			post_id BIGINT NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
			user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			content TEXT NOT NULL,
			parent_id BIGINT REFERENCES post_replies(id) ON DELETE CASCADE,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			deleted_at TIMESTAMPTZ
		)`,
		`CREATE INDEX IF NOT EXISTS idx_post_replies_post ON post_replies(post_id, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_post_replies_parent ON post_replies(parent_id)`,
		`CREATE TABLE IF NOT EXISTS reply_attachments (
			reply_id BIGINT NOT NULL REFERENCES post_replies(id) ON DELETE CASCADE,
			attachment_id BIGINT NOT NULL REFERENCES attachments(id) ON DELETE CASCADE,
			PRIMARY KEY (reply_id, attachment_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_reply_attachments_attachment ON reply_attachments(attachment_id)`,

		// ---- permission system ----
		`CREATE TABLE IF NOT EXISTS permissions (
			key TEXT PRIMARY KEY,
			name TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS groups (
			id BIGSERIAL PRIMARY KEY,
			name VARCHAR(255) NOT NULL UNIQUE,
			description TEXT NOT NULL DEFAULT '',
			is_default BOOLEAN NOT NULL DEFAULT FALSE,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS group_permissions (
			group_id BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
			permission_key TEXT NOT NULL REFERENCES permissions(key) ON DELETE CASCADE,
			PRIMARY KEY (group_id, permission_key)
		)`,
		`CREATE TABLE IF NOT EXISTS user_groups (
			user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			group_id BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (user_id, group_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_user_groups_user ON user_groups(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_user_groups_group ON user_groups(group_id)`,

		// ---- chat ----
		`CREATE TABLE IF NOT EXISTS conversations (
			id BIGSERIAL PRIMARY KEY,
			type VARCHAR(20) NOT NULL DEFAULT 'private',
			title TEXT NOT NULL DEFAULT '',
			avatar_url TEXT NOT NULL DEFAULT '',
			owner_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS conversation_members (
			conversation_id BIGINT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
			user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			role VARCHAR(20) NOT NULL DEFAULT 'member',
			note TEXT NOT NULL DEFAULT '',
			group_nickname TEXT NOT NULL DEFAULT '',
			status VARCHAR(20) NOT NULL DEFAULT 'active',
			added_by BIGINT REFERENCES users(id) ON DELETE SET NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (conversation_id, user_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_conv_members_user ON conversation_members(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_conv_members_status ON conversation_members(conversation_id, status)`,
		`ALTER TABLE conversation_members ADD COLUMN IF NOT EXISTS last_read_message_id BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE conversations ADD COLUMN IF NOT EXISTS is_public BOOLEAN NOT NULL DEFAULT FALSE`,
		`ALTER TABLE conversations ADD COLUMN IF NOT EXISTS allow_join BOOLEAN NOT NULL DEFAULT FALSE`,
		`ALTER TABLE conversations ADD COLUMN IF NOT EXISTS mute_all_until TIMESTAMPTZ`,
		`ALTER TABLE conversation_members ADD COLUMN IF NOT EXISTS muted_until TIMESTAMPTZ`,
		`CREATE INDEX IF NOT EXISTS idx_conversations_public ON conversations(is_public)`,
		`CREATE TABLE IF NOT EXISTS consent_requests (
			id BIGSERIAL PRIMARY KEY,
			type VARCHAR(20) NOT NULL,
			requester_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			target_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			conversation_id BIGINT REFERENCES conversations(id) ON DELETE CASCADE,
			group_id BIGINT REFERENCES conversations(id) ON DELETE CASCADE,
			message TEXT NOT NULL DEFAULT '',
			status VARCHAR(20) NOT NULL DEFAULT 'pending',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			responded_at TIMESTAMPTZ
		)`,
		`CREATE INDEX IF NOT EXISTS idx_consent_target ON consent_requests(target_user_id, status)`,
		`CREATE INDEX IF NOT EXISTS idx_consent_requester ON consent_requests(requester_id, status)`,

		`CREATE TABLE IF NOT EXISTS messages (
			id BIGSERIAL PRIMARY KEY,
			conversation_id BIGINT REFERENCES conversations(id) ON DELETE CASCADE,
			sender_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			content TEXT NOT NULL,
			reply_to_id BIGINT REFERENCES messages(id) ON DELETE SET NULL,
			edited_at TIMESTAMPTZ,
			deleted_at TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`ALTER TABLE messages ADD COLUMN IF NOT EXISTS conversation_id BIGINT REFERENCES conversations(id) ON DELETE CASCADE`,
		`ALTER TABLE messages ADD COLUMN IF NOT EXISTS reply_to_id BIGINT REFERENCES messages(id) ON DELETE SET NULL`,
		`ALTER TABLE messages ADD COLUMN IF NOT EXISTS edited_at TIMESTAMPTZ`,
		`ALTER TABLE messages ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ`,
		`ALTER TABLE messages ADD COLUMN IF NOT EXISTS kind VARCHAR(20) NOT NULL DEFAULT 'text'`,
		`ALTER TABLE messages DROP COLUMN IF EXISTS receiver_id`,
		`CREATE INDEX IF NOT EXISTS idx_messages_conversation ON messages(conversation_id, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_sender_receiver ON messages(sender_id)`,

		`CREATE TABLE IF NOT EXISTS message_attachments (
			message_id BIGINT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
			attachment_id BIGINT NOT NULL REFERENCES attachments(id) ON DELETE CASCADE,
			PRIMARY KEY (message_id, attachment_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_message_attachments_attachment ON message_attachments(attachment_id)`,

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
		`CREATE INDEX IF NOT EXISTS idx_refresh_tokens_user_id ON refresh_tokens(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_refresh_tokens_token ON refresh_tokens(token)`,
		`CREATE INDEX IF NOT EXISTS idx_attachments_user_id ON attachments(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_attachments_object_key ON attachments(object_key)`,
		`CREATE INDEX IF NOT EXISTS idx_post_attachments_attachment ON post_attachments(attachment_id)`,

		// ---- post analytics: views ----
		`ALTER TABLE posts ADD COLUMN IF NOT EXISTS view_count BIGINT NOT NULL DEFAULT 0`,
		`CREATE TABLE IF NOT EXISTS post_views (
			id BIGSERIAL PRIMARY KEY,
			post_id BIGINT NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
			viewer_key TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE (post_id, viewer_key)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_post_views_post ON post_views(post_id)`,

		// ---- post reactions ----
		`CREATE TABLE IF NOT EXISTS post_reactions (
			post_id BIGINT NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
			user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			reaction VARCHAR(32) NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (post_id, user_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_post_reactions_reaction ON post_reactions(reaction)`,

		// ---- follows ----
		`CREATE TABLE IF NOT EXISTS user_follows (
			follower_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			followee_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (follower_id, followee_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_user_follows_followee ON user_follows(followee_id)`,
		`CREATE INDEX IF NOT EXISTS idx_user_follows_follower ON user_follows(follower_id)`,

		// ---- wallet ----
		`CREATE TABLE IF NOT EXISTS wallets (
			user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			balance BIGINT NOT NULL DEFAULT 0,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (user_id)
		)`,
		`CREATE TABLE IF NOT EXISTS wallet_transactions (
			id BIGSERIAL PRIMARY KEY,
			user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			amount BIGINT NOT NULL,
			balance_after BIGINT NOT NULL,
			type VARCHAR(32) NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			operator_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_wallet_transactions_user ON wallet_transactions(user_id, id DESC)`,
	}

	for _, m := range migrations {
		if _, err := DB.Exec(m); err != nil {
			return fmt.Errorf("migration failed: %w", err)
		}
	}

	// seed permission catalog
	if err := seedPermissions(); err != nil {
		return err
	}
	// seed the default "everyone" group with all permissions
	if err := seedDefaultGroup(); err != nil {
		return err
	}

	logger.Log.Info("database migrations completed")
	return nil
}

// RunMigrationsIfEnabled runs schema migrations only when the service is
// configured as the schema owner (database.migrate: true). Other services skip
// migrations and simply reuse the schema created by the owner, so they start
// faster and avoid redundant DDL on every boot.
func RunMigrationsIfEnabled(cfg *config.Config) error {
	if !cfg.Database.Migrate {
		logger.Log.Info("database migrations skipped (migrate disabled for this service)")
		return nil
	}
	return RunMigrations()
}

func seedPermissions() error {
	keys := permission.All()
	if len(keys) == 0 {
		return nil
	}
	if _, err := DB.Exec(
		`INSERT INTO permissions (key, name)
		 SELECT k, k FROM unnest($1::text[]) AS k
		 ON CONFLICT (key) DO NOTHING`,
		pq.Array(keys),
	); err != nil {
		return fmt.Errorf("failed to seed permissions: %w", err)
	}
	return nil
}

func seedDefaultGroup() error {
	var groupID int64
	err := DB.QueryRow(
		`SELECT id FROM groups WHERE is_default = TRUE LIMIT 1`,
	).Scan(&groupID)
	if err != nil {
		groupID, err = createDefaultGroup()
		if err != nil {
			return err
		}
	}

	// Backfill: keep the default group granted every permission (including new
	// ones added after the group already existed).
	for _, key := range permission.All() {
		if _, err := DB.Exec(
			`INSERT INTO group_permissions (group_id, permission_key) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			groupID, key,
		); err != nil {
			return fmt.Errorf("failed to seed group permission %s: %w", key, err)
		}
	}

	// Backfill: ensure every existing user belongs to the default group so the
	// permission system grants them access immediately. Only scan when there is
	// at least one user missing a default-group membership.
	var missing int64
	if err := DB.QueryRow(
		`SELECT count(*) FROM users u
		 WHERE NOT EXISTS (
		   SELECT 1 FROM user_groups ug
		   WHERE ug.user_id = u.id AND ug.group_id = $1
		 )`,
		groupID,
	).Scan(&missing); err != nil {
		return fmt.Errorf("failed to check default group membership: %w", err)
	}
	if missing > 0 {
		if _, err := DB.Exec(
			`INSERT INTO user_groups (user_id, group_id)
			 SELECT id, $1 FROM users
			 ON CONFLICT DO NOTHING`,
			groupID,
		); err != nil {
			return fmt.Errorf("failed to backfill default group membership: %w", err)
		}
	}
	return nil
}

func createDefaultGroup() (int64, error) {
	var groupID int64
	err := DB.QueryRow(
		`INSERT INTO groups (name, description, is_default) VALUES ($1, $2, TRUE) RETURNING id`,
		permission.DefaultGroupName, "内置默认用户组，默认拥有全部权限",
	).Scan(&groupID)
	if err != nil {
		return 0, fmt.Errorf("failed to seed default group: %w", err)
	}
	return groupID, nil
}
