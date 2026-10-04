package config

import (
	"net/url"
	"strings"
	"testing"
)

// A password containing reserved URL characters must stay inside the password
// slot. Interpolating it raw let '@' end the userinfo section early, so the DSN
// pointed at the wrong host.
func TestDSNEscapesReservedCharacters(t *testing.T) {
	cases := []struct {
		name string
		user string
		pass string
	}{
		{"at sign", "of-user", "p@ssword"},
		{"colon", "of-user", "pa:ss"},
		{"slash", "of-user", "pa/ss"},
		{"question", "of-user", "pa?ss"},
		{"hash", "of-user", "pa#ss"},
		{"space and quote", "of-user", "p a s s'w\\ord"},
		{"everything", "u@ser", "a@b:c/d?e#f"},
	}
	for _, tc := range cases {
		cfg := &Config{}
		cfg.Database.User = tc.user
		cfg.Database.Password = tc.pass
		cfg.Database.Host = "db.internal"
		cfg.Database.Port = 5432
		cfg.Database.DBName = "openfield"
		cfg.Database.SSLMode = "require"

		dsn := cfg.DSN()
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatalf("%s: DSN does not parse (%q): %v", tc.name, dsn, err)
		}
		if u.Hostname() != "db.internal" {
			t.Errorf("%s: host parsed as %q, want db.internal (DSN %q)", tc.name, u.Hostname(), dsn)
		}
		if got := u.User.Username(); got != tc.user {
			t.Errorf("%s: user parsed as %q, want %q", tc.name, got, tc.user)
		}
		gotPass, _ := u.User.Password()
		if gotPass != tc.pass {
			t.Errorf("%s: password round-trip = %q, want %q", tc.name, gotPass, tc.pass)
		}
		if got := strings.TrimPrefix(u.Path, "/"); got != "openfield" {
			t.Errorf("%s: dbname parsed as %q, want openfield", tc.name, got)
		}
		if got := u.Query().Get("sslmode"); got != "require" {
			t.Errorf("%s: sslmode parsed as %q, want require", tc.name, got)
		}
	}
}

// A dbname containing a query separator must not be able to smuggle its own
// sslmode and silently disable TLS.
func TestDSNDBNameCannotInjectQueryParams(t *testing.T) {
	cfg := &Config{}
	cfg.Database.User = "of-user"
	cfg.Database.Password = "pw"
	cfg.Database.Host = "db.internal"
	cfg.Database.Port = 5432
	cfg.Database.DBName = "openfield?sslmode=disable"
	cfg.Database.SSLMode = "verify-full"

	dsn := cfg.DSN()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("DSN does not parse: %v", err)
	}
	if got := u.Query().Get("sslmode"); got != "verify-full" {
		t.Fatalf("sslmode was overridden to %q by the dbname (DSN %q)", got, dsn)
	}
	// The dbname must survive intact as data, not be split into a second
	// parameter: the parsed path is the whole original dbname.
	if got := strings.TrimPrefix(u.Path, "/"); got != "openfield?sslmode=disable" {
		t.Fatalf("dbname parsed as %q; want the literal string preserved", got)
	}
	// Exactly one query parameter is actually present.
	if n := len(u.Query()); n != 1 {
		t.Fatalf("expected exactly 1 query parameter, got %d (%q)", n, dsn)
	}
}

// DATABASE_URL parsing must survive an '@' and a ':' inside the password, and
// must pick up the port and sslmode.
func TestDatabaseURLFromEnvHandlesReservedCharacters(t *testing.T) {
	cfg := &Config{}
	cfg.Database.Port = 5432 // pre-existing default

	cfg.DatabaseURLFromEnv("postgres://user:p%40ss%3Aword@db.example.com:6543/mydb?sslmode=require")

	if cfg.Database.User != "user" {
		t.Errorf("user = %q, want user", cfg.Database.User)
	}
	if cfg.Database.Password != "p@ss:word" {
		t.Errorf("password = %q, want p@ss:word", cfg.Database.Password)
	}
	if cfg.Database.Host != "db.example.com" {
		t.Errorf("host = %q, want db.example.com", cfg.Database.Host)
	}
	if cfg.Database.Port != 6543 {
		t.Errorf("port = %d, want 6543", cfg.Database.Port)
	}
	if cfg.Database.DBName != "mydb" {
		t.Errorf("dbname = %q, want mydb", cfg.Database.DBName)
	}
	if cfg.Database.SSLMode != "require" {
		t.Errorf("sslmode = %q, want require", cfg.Database.SSLMode)
	}
}

// A malformed value must leave the existing configuration untouched rather than
// half-applying a partial parse.
func TestDatabaseURLFromEnvLeavesConfigOnGarbage(t *testing.T) {
	cfg := &Config{}
	cfg.Database.User = "original"
	cfg.Database.Host = "original-host"

	cfg.DatabaseURLFromEnv("not a url at all")

	if cfg.Database.User != "original" || cfg.Database.Host != "original-host" {
		t.Fatalf("garbage input modified the config: user=%q host=%q", cfg.Database.User, cfg.Database.Host)
	}
}
