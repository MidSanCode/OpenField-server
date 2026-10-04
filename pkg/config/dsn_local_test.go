package config

import "testing"

// The DSN that the local development configuration produces must stay a valid,
// connectable URL: the escaping change must not alter the simple case.
func TestDSNLocalDevelopmentShape(t *testing.T) {
	cfg := &Config{}
	cfg.Database.Host = "localhost"
	cfg.Database.Port = 5432
	cfg.Database.User = "of-user"
	cfg.Database.Password = "of-user-1207"
	cfg.Database.DBName = "openfield"
	cfg.Database.SSLMode = "disable"

	want := "postgres://of-user:of-user-1207@localhost:5432/openfield?sslmode=disable"
	if got := cfg.DSN(); got != want {
		t.Fatalf("DSN() = %q, want %q", got, want)
	}
}
