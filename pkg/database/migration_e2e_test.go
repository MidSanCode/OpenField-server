package database

import (
	"context"
	"os"
	"testing"

	"github.com/openfield/server/pkg/config"
	"github.com/openfield/server/pkg/logger"
)

// e2eConfig builds a config pointed at the local development PostgreSQL.
func e2eConfig(dbName string) *config.Config {
	return &config.Config{
		Database: config.DatabaseConfig{
			Host:     "127.0.0.1",
			Port:     5432,
			User:     "of-user",
			Password: "of-user-1207",
			DBName:   dbName,
			SSLMode:  "disable",
		},
	}
}

// columnExists reports whether table.column is present in the database.
func columnExists(t *testing.T, table, column string) bool {
	t.Helper()
	var n int
	if err := DB.QueryRow(
		`SELECT COUNT(*) FROM information_schema.columns WHERE table_name = $1 AND column_name = $2`,
		table, column,
	).Scan(&n); err != nil {
		t.Fatalf("column check failed: %v", err)
	}
	return n > 0
}

// TestFreshDatabaseMigrationE2E exercises the full upgrade path against a
// real (empty) PostgreSQL database: baseline + every versioned migration in
// order, a second run to prove idempotency, and the v25 repair path for
// databases that recorded a v24 with the camp_id column missing. Skipped
// unless OF_MIGRATION_E2E=1 and a scratch database named <dbname> exists.
func TestFreshDatabaseMigrationE2E(t *testing.T) {
	if os.Getenv("OF_MIGRATION_E2E") != "1" {
		t.Skip("set OF_MIGRATION_E2E=1 and create a scratch database to run")
	}
	if logger.Log == nil {
		logger.Init()
	}
	cfg := e2eConfig(os.Getenv("OF_MIGRATION_E2E_DB"))
	if cfg.Database.DBName == "" {
		t.Fatal("OF_MIGRATION_E2E_DB must name the scratch database")
	}
	if err := Connect(cfg); err != nil {
		t.Fatalf("connect failed: %v", err)
	}
	defer Close()
	ctx := context.Background()

	// Phase 1: fresh install — baseline + v2..v25, the path that used to
	// crash on v24's ordering bug.
	if err := RunMigrations(); err != nil {
		t.Fatalf("fresh RunMigrations failed: %v", err)
	}
	version, err := currentSchemaVersion()
	if err != nil {
		t.Fatalf("read version failed: %v", err)
	}
	if version != latestMigrationVersion() {
		t.Fatalf("version = %d, want %d", version, latestMigrationVersion())
	}

	// Phase 2: every critical column the application queries must exist.
	for _, pair := range [][2]string{
		{"users", "member_level"}, {"users", "last_seen_at"}, {"users", "auto_renew"},
		{"posts", "pinned"}, {"posts", "camp_id"}, {"posts", "quoted_post_id"},
		{"posts", "camp_pinned"},
		{"camps", "member_post"}, {"camps", "member_pin"}, {"camps", "announcement"},
		{"attachments", "preview_url"}, {"attachments", "burn_at"},
		{"messages", "check_id"}, {"messages", "burn_at"},
		{"conversations", "is_public"}, {"conversation_members", "notify_level"},
		{"refresh_tokens", "device_label"},
	} {
		if !columnExists(t, pair[0], pair[1]) {
			t.Errorf("missing column %s.%s after full migration", pair[0], pair[1])
		}
	}

	// Phase 3: re-running on an up-to-date database must be a clean no-op.
	if err := RunMigrations(); err != nil {
		t.Fatalf("idempotent RunMigrations failed: %v", err)
	}

	// Phase 4: simulate the v24 broken state — versions 25+ rewound, camp_id
	// dropped — then confirm the repairs re-apply on the next upgrade pass.
	if _, err := DB.ExecContext(ctx, `ALTER TABLE posts DROP COLUMN camp_id`); err != nil {
		t.Fatalf("simulated drift failed: %v", err)
	}
	if _, err := DB.ExecContext(ctx, `ALTER TABLE posts DROP COLUMN camp_pinned`); err != nil {
		t.Fatalf("simulated drift failed: %v", err)
	}
	if _, err := DB.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version >= 25`); err != nil {
		t.Fatalf("version rewind failed: %v", err)
	}
	if err := RunMigrations(); err != nil {
		t.Fatalf("repair RunMigrations failed: %v", err)
	}
	if !columnExists(t, "posts", "camp_id") {
		t.Fatal("camp_id was not repaired by v25")
	}
	if !columnExists(t, "posts", "camp_pinned") {
		t.Fatal("camp_pinned was not re-added by v26")
	}
}
