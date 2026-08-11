package database

import (
	"context"
	"fmt"
	"time"

	"github.com/lib/pq"
	"github.com/openfield/server/pkg/config"
	"github.com/openfield/server/pkg/logger"
	"github.com/openfield/server/pkg/permission"
)

// schemaMigrationLockID serializes schema upgrades across every service that
// shares a database. Migrations take the lock before mutating the schema and
// release it afterwards, so a new version is only ever applied by one service;
// the rest simply observe the recorded version when they acquire the lock.
const schemaMigrationLockID = 720001

// migration is one numbered, run-once schema step.
type migration struct {
	version int
	name    string
	sql     string
}

// versionedMigrations lists every schema change shipped after the legacy
// baseline. The list is append-only: never edit or renumber an existing entry,
// always add a new higher-version entry. Because each step is recorded in
// schema_migrations and only runs once, future steps may be non-idempotent.
var versionedMigrations = []migration{
	{
		version: 2,
		name:    "tasks-exp-history-transfers",
		sql: `
			ALTER TABLE users ADD COLUMN IF NOT EXISTS checkin_streak BIGINT NOT NULL DEFAULT 0;
			ALTER TABLE users ADD COLUMN IF NOT EXISTS region VARCHAR(32) NOT NULL DEFAULT '';
			ALTER TABLE users ADD COLUMN IF NOT EXISTS lang VARCHAR(8) NOT NULL DEFAULT '';

			CREATE TABLE IF NOT EXISTS exp_history (
				id BIGSERIAL PRIMARY KEY,
				user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				amount BIGINT NOT NULL,
				reason VARCHAR(64) NOT NULL,
				description TEXT NOT NULL DEFAULT '',
				created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
			);
			CREATE INDEX IF NOT EXISTS idx_exp_history_user ON exp_history(user_id, id DESC);

			CREATE TABLE IF NOT EXISTS tasks (
				id BIGSERIAL PRIMARY KEY,
				code VARCHAR(64) NOT NULL UNIQUE,
				kind VARCHAR(16) NOT NULL,
				name VARCHAR(255) NOT NULL,
				description TEXT NOT NULL DEFAULT '',
				reward_exp BIGINT NOT NULL DEFAULT 0,
				reward_currency BIGINT NOT NULL DEFAULT 0,
				target INT NOT NULL DEFAULT 0,
				sort INT NOT NULL DEFAULT 0
			);

			CREATE TABLE IF NOT EXISTS task_completions (
				id BIGSERIAL PRIMARY KEY,
				user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				task_id BIGINT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
				cycle_key VARCHAR(64) NOT NULL DEFAULT '',
				completed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
				UNIQUE (user_id, task_id, cycle_key)
			);
			CREATE INDEX IF NOT EXISTS idx_task_completions_user ON task_completions(user_id, task_id);

			CREATE TABLE IF NOT EXISTS transfers (
				id BIGSERIAL PRIMARY KEY,
				sender_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				recipient_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				amount BIGINT NOT NULL,
				status VARCHAR(16) NOT NULL DEFAULT 'pending',
				note TEXT NOT NULL DEFAULT '',
				created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
				decided_at TIMESTAMPTZ,
				refunded_at TIMESTAMPTZ
			);
			CREATE INDEX IF NOT EXISTS idx_transfers_recipient ON transfers(recipient_id, status, id DESC);
			CREATE INDEX IF NOT EXISTS idx_transfers_sender ON transfers(sender_id, id DESC);
		`,
	},
}

// latestMigrationVersion returns the newest schema version the code knows
// about. The legacy baseline is always version 1.
func latestMigrationVersion() int {
	if len(versionedMigrations) == 0 {
		return 1
	}
	return versionedMigrations[len(versionedMigrations)-1].version
}

// baselineStatements are the pre-versioning schema statements (version 1).
// They are intentionally idempotent (CREATE TABLE IF NOT EXISTS / ADD COLUMN IF
// NOT EXISTS) so they can be applied to any database, including one created
// before schema versioning existed. All new schema work must go into
// versionedMigrations, never here.
var baselineStatements = []string{
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
	`ALTER TABLE users ADD COLUMN IF NOT EXISTS e2ee_public_key TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE users ADD COLUMN IF NOT EXISTS verified_note TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE users ADD COLUMN IF NOT EXISTS verified_by VARCHAR(255) NOT NULL DEFAULT ''`,
	`ALTER TABLE users ADD COLUMN IF NOT EXISTS exp BIGINT NOT NULL DEFAULT 0`,
	`ALTER TABLE users ADD COLUMN IF NOT EXISTS last_daily_bonus_at TIMESTAMPTZ`,
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
	`ALTER TABLE conversations ADD COLUMN IF NOT EXISTS encrypted BOOLEAN NOT NULL DEFAULT FALSE`,
	`ALTER TABLE conversation_members ADD COLUMN IF NOT EXISTS title TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE conversation_members ADD COLUMN IF NOT EXISTS notify_level VARCHAR(20) NOT NULL DEFAULT 'all'`,
	`CREATE INDEX IF NOT EXISTS idx_conversations_public ON conversations(is_public)`,
	`CREATE TABLE IF NOT EXISTS conversation_e2ee_keys (
		id BIGSERIAL PRIMARY KEY,
		conversation_id BIGINT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
		version BIGINT NOT NULL,
		target_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		ciphertext TEXT NOT NULL,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,
	`CREATE INDEX IF NOT EXISTS idx_e2ee_keys_conv_target ON conversation_e2ee_keys(conversation_id, target_user_id, version)`,
	`CREATE TABLE IF NOT EXISTS consent_requests (
		id BIGSERIAL PRIMARY KEY,
		type VARCHAR(20) NOT NULL,
		requester_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		target_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		conversation_id BIGINT REFERENCES conversations(id) ON DELETE CASCADE,
		group_id BIGINT REFERENCES conversations(id) ON DELETE CASCADE,
		message TEXT NOT NULL DEFAULT '',
		encrypted BOOLEAN NOT NULL DEFAULT FALSE,
		status VARCHAR(20) NOT NULL DEFAULT 'pending',
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		responded_at TIMESTAMPTZ
	)`,
	`ALTER TABLE consent_requests ADD COLUMN IF NOT EXISTS encrypted BOOLEAN NOT NULL DEFAULT FALSE`,
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
	`ALTER TABLE messages ADD COLUMN IF NOT EXISTS mentions JSONB NOT NULL DEFAULT '[]'::jsonb`,
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

// RunMigrations upgrades the connected database schema to the latest version
// and (re)seeds the permission catalog and default group. Each numbered
// migration runs exactly once; the baseline (version 1) is idempotent so it
// safely upgrades databases that predate schema versioning.
func RunMigrations() error {
	if err := ensureSchemaMigrationsTable(); err != nil {
		return err
	}

	latest := latestMigrationVersion()
	current, err := currentSchemaVersion()
	if err != nil {
		return fmt.Errorf("failed to read schema version: %w", err)
	}
	if current >= latest {
		logger.Log.Info("database schema is up to date", "version", current)
	} else {
		logger.Log.Info("database schema is outdated, applying upgrade",
			"from", current, "to", latest)
		if err := applyPending(); err != nil {
			return err
		}
	}

	if err := seedPermissions(); err != nil {
		return err
	}
	if err := seedDefaultGroup(); err != nil {
		return err
	}
	if err := seedTasks(); err != nil {
		return err
	}

	logger.Log.Info("database migrations completed", "version", latest)
	return nil
}

// RunMigrationsIfEnabled runs schema migrations when explicitly requested
// (database.migrate: true) or when automatic upgrades are enabled
// (database.auto_migrate, defaults to true) and the database is behind the
// latest schema version. Because migration steps are recorded and applied in
// order, production databases are upgraded in place without operator
// intervention.
func RunMigrationsIfEnabled(cfg *config.Config) error {
	if cfg.Database.Migrate {
		return RunMigrations()
	}
	auto := true
	if cfg.Database.AutoMigrate != nil {
		auto = *cfg.Database.AutoMigrate
	}
	if !auto {
		logger.Log.Info("database migrations skipped (auto_migrate disabled for this service)")
		return nil
	}
	needs, err := SchemaNeedsMigration()
	if err != nil {
		return fmt.Errorf("failed to check database schema state: %w", err)
	}
	if !needs {
		logger.Log.Info("database schema is up to date, migrations skipped")
		return nil
	}
	logger.Log.Info("database schema is outdated, running automatic upgrade")
	return RunMigrations()
}

// applyPending applies every missing migration up to the latest version,
// guarded by a PostgreSQL advisory lock so only one service at a time mutates
// the shared schema. Steps that have already been recorded are skipped.
func applyPending() error {
	conn, err := DB.Conn(context.Background())
	if err != nil {
		return fmt.Errorf("failed to open migration connection: %w", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, schemaMigrationLockID); err != nil {
		return fmt.Errorf("failed to acquire schema migration lock: %w", err)
	}
	defer func() {
		_, _ = conn.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, schemaMigrationLockID)
	}()

	// Re-read under the lock: another service may have finished the upgrade
	// while we were waiting for it.
	applied, err := currentSchemaVersion()
	if err != nil {
		return fmt.Errorf("failed to read schema version: %w", err)
	}

	if applied < 1 {
		tx, err := DB.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("failed to begin baseline migration: %w", err)
		}
		for _, stmt := range baselineStatements {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("baseline migration failed: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, name) VALUES ($1, $2) ON CONFLICT (version) DO NOTHING`,
			1, "baseline",
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("failed to record baseline version: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("failed to commit baseline migration: %w", err)
		}
		applied = 1
		logger.Log.Info("applied database baseline migration", "version", 1)
	}

	for _, m := range versionedMigrations {
		if m.version <= applied {
			continue
		}
		tx, err := DB.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("failed to begin migration %d (%s): %w", m.version, m.name, err)
		}
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d (%s) failed: %w", m.version, m.name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, name) VALUES ($1, $2) ON CONFLICT (version) DO NOTHING`,
			m.version, m.name,
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("failed to record migration %d (%s): %w", m.version, m.name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("failed to commit migration %d (%s): %w", m.version, m.name, err)
		}
		applied = m.version
		logger.Log.Info("applied database migration", "version", m.version, "name", m.name)
	}

	return nil
}

// SchemaNeedsMigration reports whether the connected database is behind the
// latest schema version, by comparing the recorded schema_migrations version
// against the migrations this binary knows about. Cheap: a single row read,
// no DDL.
func SchemaNeedsMigration() (bool, error) {
	if err := ensureSchemaMigrationsTable(); err != nil {
		return false, err
	}
	current, err := currentSchemaVersion()
	if err != nil {
		return false, err
	}
	return current < latestMigrationVersion(), nil
}

func ensureSchemaMigrationsTable() error {
	if _, err := DB.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version BIGINT PRIMARY KEY,
		name TEXT NOT NULL,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`); err != nil {
		return fmt.Errorf("failed to create schema_migrations table: %w", err)
	}
	return nil
}

func currentSchemaVersion() (int, error) {
	var version int
	if err := DB.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		return 0, fmt.Errorf("failed to query schema version: %w", err)
	}
	return version, nil
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

// seedTask is one built-in task definition written into the tasks table.
type seedTask struct {
	code, kind, name, description string
	rewardExp, rewardCurrency     int64
	target, sort                  int
}

// taskSeeds is the built-in task catalog: one-time achievements (once) and a
// repeatable sign-in streak task (streak) whose target is the streak length.
// Seeding is upsert-by-code so operators may tweak entries without duplicating
// rows, and new codes are added to existing installs on the next migration run.
var taskSeeds = []seedTask{
	{
		code: "daily_login", kind: "streak", name: "每日签到",
		description: "每日登录签到，连续签到可累积天数，连续 7 天奖励更多经验与金币",
		rewardExp:   10, rewardCurrency: 5, target: 1, sort: 10,
	},
	{
		code: "login_3", kind: "streak", name: "连续签到 3 天",
		description: "累计连续签到达到 3 天",
		rewardExp:   30, rewardCurrency: 20, target: 3, sort: 20,
	},
	{
		code: "login_7", kind: "streak", name: "连续签到 7 天",
		description: "累计连续签到达到 7 天",
		rewardExp:   120, rewardCurrency: 60, target: 7, sort: 21,
	},
	{
		code: "login_30", kind: "streak", name: "连续签到 30 天",
		description: "累计连续签到达到 30 天",
		rewardExp:   600, rewardCurrency: 300, target: 30, sort: 22,
	},
	{
		code: "first_post", kind: "once", name: "发布第一篇动态",
		description: "发布你的第一篇文字动态",
		rewardExp:   30, rewardCurrency: 10, target: 1, sort: 100,
	},
	{
		code: "first_reply", kind: "once", name: "发表第一条回复",
		description: "在任意动态下发表第一条回复",
		rewardExp:   20, rewardCurrency: 10, target: 1, sort: 101,
	},
	{
		code: "first_follow", kind: "once", name: "关注第一位用户",
		description: "关注任意一位用户",
		rewardExp:   20, rewardCurrency: 10, target: 1, sort: 102,
	},
	{
		code: "first_upload", kind: "once", name: "上传第一个文件",
		description: "上传一张图片或一个文件到你的动态",
		rewardExp:   30, rewardCurrency: 20, target: 1, sort: 103,
	},
	{
		code: "first_chat", kind: "once", name: "发起第一条私聊",
		description: "给任意用户发起一条私聊消息",
		rewardExp:   30, rewardCurrency: 20, target: 1, sort: 104,
	},
	{
		code: "follow_10", kind: "once", name: "关注 10 位用户",
		description: "累计关注 10 位用户",
		rewardExp:   100, rewardCurrency: 50, target: 10, sort: 105,
	},
	{
		code: "posts_10", kind: "once", name: "发布 10 篇动态",
		description: "累计发布 10 篇动态",
		rewardExp:   150, rewardCurrency: 80, target: 10, sort: 106,
	},
}

func seedTasks() error {
	for _, t := range taskSeeds {
		if _, err := DB.Exec(
			`INSERT INTO tasks (code, kind, name, description, reward_exp, reward_currency, target, sort)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			 ON CONFLICT (code) DO UPDATE SET
			   kind = EXCLUDED.kind,
			   name = EXCLUDED.name,
			   description = EXCLUDED.description,
			   reward_exp = EXCLUDED.reward_exp,
			   reward_currency = EXCLUDED.reward_currency,
			   target = EXCLUDED.target,
			   sort = EXCLUDED.sort`,
			t.code, t.kind, t.name, t.description, t.rewardExp, t.rewardCurrency, t.target, t.sort,
		); err != nil {
			return fmt.Errorf("failed to seed task %s: %w", t.code, err)
		}
	}
	return nil
}
