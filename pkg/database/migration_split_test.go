package database

import (
	"strings"
	"testing"
)

// TestSplitMigrationSQL covers the statement splitter, especially the
// dollar-quoted PL/pgSQL body that used to be shredded into an unterminated
// fragment by the old ";\\n" split.
func TestSplitMigrationSQL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "plain statements",
			in:   "ALTER TABLE a ADD COLUMN x INT;\nALTER TABLE b ADD COLUMN y INT;\n",
			want: []string{"ALTER TABLE a ADD COLUMN x INT", "ALTER TABLE b ADD COLUMN y INT"},
		},
		{
			name: "trailing statement without newline",
			in:   "ALTER TABLE a ADD COLUMN x INT;",
			want: []string{"ALTER TABLE a ADD COLUMN x INT"},
		},
		{
			name: "empty script",
			in:   "\n\t\t\n",
			want: nil,
		},
		{
			name: "semaphore inside a single-quoted literal",
			in:   "INSERT INTO t (a) VALUES ('x;y');\n",
			want: []string{"INSERT INTO t (a) VALUES ('x;y')"},
		},
		{
			name: "doubled quote inside a literal",
			in:   "INSERT INTO t (a) VALUES ('it''s; fine');\n",
			want: []string{"INSERT INTO t (a) VALUES ('it''s; fine')"},
		},
		{
			name: "bound parameter is not a dollar quote",
			in:   "SELECT 1 FROM t WHERE a = $1 AND b = $2;\n",
			want: []string{"SELECT 1 FROM t WHERE a = $1 AND b = $2"},
		},
		{
			name: "tagged dollar quote",
			in:   "DO $body$ SELECT 1; SELECT 2; $body$;\n",
			want: []string{"DO $body$ SELECT 1; SELECT 2; $body$"},
		},
		{
			name: "dollar-quoted block keeps its inner semicolons",
			in: "DO $$\n" +
				"BEGIN\n" +
				"  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'c') THEN\n" +
				"    ALTER TABLE wallets ADD CONSTRAINT c CHECK (balance >= 0);\n" +
				"  END IF;\n" +
				"END\n" +
				"$$;\n" +
				"CREATE UNIQUE INDEX IF NOT EXISTS ux ON users (LOWER(email)) WHERE email <> '';\n",
			want: []string{
				"DO $$\nBEGIN\n  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'c') THEN\n    ALTER TABLE wallets ADD CONSTRAINT c CHECK (balance >= 0);\n  END IF;\nEND\n$$",
				"CREATE UNIQUE INDEX IF NOT EXISTS ux ON users (LOWER(email)) WHERE email <> ''",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitMigrationSQL(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("splitMigrationSQL() returned %d statements %q, want %d %q", len(got), got, len(tc.want), tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("statement %d = %q, want %q", i+1, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestVersionedMigrationsSplitCleanly is the regression guard: every real
// migration must split into statements with balanced dollar-quote delimiters.
// v30's DO $$ ... $$ block failed this before the splitter learned about
// dollar quoting, which made a fresh database impossible to provision.
func TestVersionedMigrationsSplitCleanly(t *testing.T) {
	if len(versionedMigrations) == 0 {
		t.Fatal("no versioned migrations to check")
	}
	for _, m := range versionedMigrations {
		statements := splitMigrationSQL(m.sql)
		if len(statements) == 0 {
			t.Errorf("migration %d (%s) produced no statements", m.version, m.name)
			continue
		}
		for i, stmt := range statements {
			if n := strings.Count(stmt, "$$"); n%2 != 0 {
				t.Errorf("migration %d (%s) statement %d has an unbalanced `$$` (%d occurrences): %q",
					m.version, m.name, i+1, n, stmt)
			}
		}
	}
}
