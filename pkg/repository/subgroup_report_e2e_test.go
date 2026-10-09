package repository

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/openfield/server/pkg/config"
	"github.com/openfield/server/pkg/database"
	"github.com/openfield/server/pkg/logger"
	"github.com/openfield/server/pkg/model"
)

// E2E tests for camp subgroups and the moderation report inbox.
//
// They run against a scratch PostgreSQL database so they can create and
// delete real rows; the development database is never touched. Gated like
// pkg/database's migration e2e test:
//
//	createdb -U of-user of_scratch
//	OF_REPO_E2E=1 OF_REPO_E2E_DB=of_scratch go test ./pkg/repository/
func connectRepoE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("OF_REPO_E2E") != "1" {
		t.Skip("set OF_REPO_E2E=1 and OF_REPO_E2E_DB=<scratch db> to run")
	}
	dbName := os.Getenv("OF_REPO_E2E_DB")
	if dbName == "" {
		t.Fatal("OF_REPO_E2E_DB must name the scratch database")
	}
	if logger.Log == nil {
		logger.Init()
	}
	cfg := &config.Config{Database: config.DatabaseConfig{
		Host:     "127.0.0.1",
		Port:     5432,
		User:     "of-user",
		Password: "of-user-1207",
		DBName:   dbName,
		SSLMode:  "disable",
	}}
	if err := database.Connect(cfg); err != nil {
		t.Fatalf("connect to scratch database failed: %v", err)
	}
	t.Cleanup(database.Close)
	if err := database.RunMigrations(); err != nil {
		t.Fatalf("migrating the scratch database failed: %v", err)
	}
}

// makeE2EUser inserts a throwaway user and removes it (with everything that
// cascades from it) when the test ends.
func makeE2EUser(t *testing.T, prefix string) int64 {
	t.Helper()
	name := fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	var id int64
	if err := database.DB.QueryRow("INSERT INTO users (username) VALUES ($1) RETURNING id", name).Scan(&id); err != nil {
		t.Fatalf("creating user %s failed: %v", name, err)
	}
	t.Cleanup(func() { database.DB.Exec("DELETE FROM users WHERE id = $1", id) })
	return id
}

func countRows(t *testing.T, query string, args ...interface{}) int {
	t.Helper()
	var n int
	if err := database.DB.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	return n
}

// TestCampSubgroupLifecycleE2E covers the whole subgroup contract: a subgroup
// is private by construction, members are pulled from the parent camp's
// roster, it never surfaces in the public list, and it dies with its parent
// (including its posts, which must not leak into the global feed).
func TestCampSubgroupLifecycleE2E(t *testing.T) {
	connectRepoE2E(t)
	repo := NewCampRepository()
	suffix := time.Now().UnixNano()
	owner := makeE2EUser(t, "e2e_sub_owner")
	member := makeE2EUser(t, "e2e_sub_member")
	outsider := makeE2EUser(t, "e2e_sub_outsider")

	parent, err := repo.Create(owner, 0, fmt.Sprintf("e2e_parent_%d", suffix), "parent", true, true, true, false)
	if err != nil {
		t.Fatalf("creating the parent camp failed: %v", err)
	}
	if parent.ParentCampID != 0 || !parent.IsVisible {
		t.Fatalf("a top-level camp should be public with no parent, got %+v", parent)
	}
	t.Cleanup(func() {
		database.DB.Exec("DELETE FROM camps WHERE id = $1 OR parent_camp_id = $1", parent.ID)
	})

	// Ask for a visible, directly joinable camp: a subgroup must refuse both.
	sub, err := repo.Create(owner, parent.ID, fmt.Sprintf("e2e_sub_%d", suffix), "sub", true, true, true, false)
	if err != nil {
		t.Fatalf("creating the subgroup failed: %v", err)
	}
	if sub.ParentCampID != parent.ID {
		t.Errorf("subgroup parent_camp_id = %d, want %d", sub.ParentCampID, parent.ID)
	}
	if sub.IsVisible {
		t.Error("subgroup is visible: it must always be hidden")
	}
	if sub.DirectJoin {
		t.Error("subgroup allows direct join: it must always be invitation-only")
	}
	// Nesting is one level deep only.
	if _, err := repo.Create(owner, sub.ID, fmt.Sprintf("e2e_nested_%d", suffix), "nested", false, false, true, false); !errors.Is(err, ErrNotFound) {
		t.Errorf("a subgroup must not accept subgroups of its own, got %v", err)
	}

	// The public list must never leak a subgroup.
	list, err := repo.List(owner, "", 100)
	if err != nil {
		t.Fatalf("listing camps failed: %v", err)
	}
	var foundParent, foundSub bool
	for _, c := range list {
		if c.ID == parent.ID {
			foundParent = true
		}
		if c.ID == sub.ID {
			foundSub = true
		}
	}
	if !foundParent {
		t.Error("the parent camp is missing from the public list")
	}
	if foundSub {
		t.Error("a subgroup leaked into the public camp list")
	}

	// Members come from the parent camp only.
	if _, err := repo.AddMember(parent.ID, member, model.CampRoleMember); err != nil {
		t.Fatalf("adding a member to the parent camp failed: %v", err)
	}
	added, err := repo.AddMember(sub.ID, member, model.CampRoleMember)
	if err != nil || !added {
		t.Fatalf("a parent-camp member must be pullable into a subgroup: added=%v err=%v", added, err)
	}
	if _, err := repo.AddMember(sub.ID, outsider, model.CampRoleMember); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a non-member of the parent camp must be refused, got %v", err)
	}

	subs, err := repo.ListSubgroups(parent.ID, member, 50)
	if err != nil {
		t.Fatalf("listing subgroups failed: %v", err)
	}
	if len(subs) != 1 || subs[0].ID != sub.ID {
		t.Fatalf("ListSubgroups returned %+v, want just subgroup %d", subs, sub.ID)
	}
	if !subs[0].IsMember || subs[0].MyRole != model.CampRoleMember {
		t.Errorf("subgroup membership not personalized: %+v", subs[0])
	}

	// A private post inside the subgroup plus a public post in the parent.
	var subPostID, parentPostID int64
	if err := database.DB.QueryRow(
		"INSERT INTO posts (user_id, content, camp_id) VALUES ($1, $2, $3) RETURNING id",
		owner, "private subgroup post", sub.ID,
	).Scan(&subPostID); err != nil {
		t.Fatalf("inserting the subgroup post failed: %v", err)
	}
	if err := database.DB.QueryRow(
		"INSERT INTO posts (user_id, content, camp_id) VALUES ($1, $2, $3) RETURNING id",
		owner, "public parent post", parent.ID,
	).Scan(&parentPostID); err != nil {
		t.Fatalf("inserting the parent post failed: %v", err)
	}

	if err := repo.Delete(parent.ID, owner); err != nil {
		t.Fatalf("deleting the parent camp failed: %v", err)
	}
	if n := countRows(t, "SELECT COUNT(*) FROM camps WHERE id = $1", sub.ID); n != 0 {
		t.Errorf("the subgroup survived its parent's deletion (%d rows)", n)
	}
	if n := countRows(t, "SELECT COUNT(*) FROM posts WHERE id = $1", subPostID); n != 0 {
		t.Error("a private subgroup post leaked after the parent camp was deleted")
	}
	if n := countRows(t, "SELECT COUNT(*) FROM posts WHERE id = $1", parentPostID); n != 1 {
		t.Error("a public camp's post should survive its camp's deletion (camp_id set to NULL)")
	}
	database.DB.Exec("DELETE FROM posts WHERE id = $1", parentPostID)
}

// TestReportRepositoryE2E covers filing reports against posts, chat messages
// and users, the anti-duplicate and anti-spam guards, and reading them back.
func TestReportRepositoryE2E(t *testing.T) {
	connectRepoE2E(t)
	repo := NewReportRepository()
	reporter := makeE2EUser(t, "e2e_rep_reporter")
	author := makeE2EUser(t, "e2e_rep_author")
	target := makeE2EUser(t, "e2e_rep_target")

	var postID int64
	if err := database.DB.QueryRow(
		"INSERT INTO posts (user_id, content) VALUES ($1, $2) RETURNING id",
		author, "reportable post",
	).Scan(&postID); err != nil {
		t.Fatalf("inserting the post failed: %v", err)
	}
	var convID int64
	if err := database.DB.QueryRow(
		"INSERT INTO conversations (type, title, owner_id) VALUES ('group', 'reportable group', $1) RETURNING id",
		author,
	).Scan(&convID); err != nil {
		t.Fatalf("inserting the conversation failed: %v", err)
	}
	var msgID int64
	if err := database.DB.QueryRow(
		"INSERT INTO messages (conversation_id, sender_id, content) VALUES ($1, $2, $3) RETURNING id",
		convID, author, "reportable message",
	).Scan(&msgID); err != nil {
		t.Fatalf("inserting the message failed: %v", err)
	}
	t.Cleanup(func() {
		database.DB.Exec("DELETE FROM posts WHERE id = $1", postID)
		database.DB.Exec("DELETE FROM conversations WHERE id = $1", convID)
	})

	for _, tc := range []struct {
		targetType string
		targetID   int64
	}{
		{model.ReportTargetPost, postID},
		{model.ReportTargetMessage, msgID},
		{model.ReportTargetUser, target},
	} {
		rep, err := repo.Create(reporter, tc.targetType, tc.targetID, "looks abusive")
		if err != nil {
			t.Fatalf("reporting %s %d failed: %v", tc.targetType, tc.targetID, err)
		}
		if rep.Status != model.ReportStatusPending {
			t.Errorf("new report status = %q, want %q", rep.Status, model.ReportStatusPending)
		}
	}

	// A second open report on the same target is refused; resolving the first
	// one frees the slot again.
	if _, err := repo.Create(reporter, model.ReportTargetPost, postID, "again"); !errors.Is(err, ErrAlreadyReported) {
		t.Fatalf("duplicate open report should be refused, got %v", err)
	}
	if _, err := database.DB.Exec(
		"UPDATE reports SET status = 'dismissed', reviewed_at = NOW() WHERE reporter_id = $1 AND target_type = 'post' AND target_id = $2",
		reporter, postID,
	); err != nil {
		t.Fatalf("resolving the report failed: %v", err)
	}
	if _, err := repo.Create(reporter, model.ReportTargetPost, postID, "after review"); err != nil {
		t.Fatalf("reporting again after review should be allowed, got %v", err)
	}

	// Unknown target type and missing targets are rejected.
	if _, err := repo.Create(reporter, "camps", postID, "wrong type"); err == nil {
		t.Error("an unknown target_type must be rejected")
	}
	if _, err := repo.Create(reporter, model.ReportTargetPost, int64(1)<<40, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a missing target must be reported as ErrNotFound, got %v", err)
	}

	mine, err := repo.ListMine(reporter, 50)
	if err != nil {
		t.Fatalf("listing my reports failed: %v", err)
	}
	// 4 rows: the three initial reports plus the one filed after review.
	if len(mine) != 4 {
		t.Fatalf("ListMine returned %d reports, want 4", len(mine))
	}
	if mine[0].ReporterName == "" {
		t.Error("ListMine did not resolve the reporter name")
	}

	// Filling the hourly budget refuses the next report. The filler rows are
	// dismissed so they do not trip the one-open-report index.
	for i := 0; i < MaxReportsPerHour; i++ {
		if _, err := database.DB.Exec(
			"INSERT INTO reports (reporter_id, target_type, target_id, reason, status) VALUES ($1, 'user', $2, 'filler', 'dismissed')",
			reporter, target,
		); err != nil {
			t.Fatalf("inserting filler report %d failed: %v", i, err)
		}
	}
	if _, err := repo.Create(reporter, model.ReportTargetUser, target, "over budget"); !errors.Is(err, ErrReportRateLimit) {
		t.Fatalf("the hourly report budget was not enforced, got %v", err)
	}
}
