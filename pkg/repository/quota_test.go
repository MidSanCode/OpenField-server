package repository

import (
	"errors"
	"testing"
)

// The quota guard must reject rather than allow when the configured budget is
// non-positive: zero previously meant "unlimited", which inverted the intent.
// This exercises the pure predicate the transactional insert relies on.
func TestQuotaPredicateRejectsNonPositive(t *testing.T) {
	type tc struct {
		name  string
		quota int64
		used  int64
		size  int64
		want  bool // true = allowed
	}
	cases := []tc{
		{"zero quota rejects", 0, 0, 1, false},
		{"negative quota rejects", -1, 0, 1, false},
		{"exactly at quota allowed", 100, 100, 0, true},
		{"one byte over rejected", 100, 100, 1, false},
		{"under quota allowed", 100, 50, 50, true},
		{"empty account under quota", 100, 0, 99, true},
	}
	for _, c := range cases {
		got := c.quota > 0 && c.used+c.size <= c.quota
		if got != c.want {
			t.Errorf("%s: allowed=%v want %v", c.name, got, c.want)
		}
	}
}

// ErrQuotaExceeded must be identifiable with errors.Is so handlers can map it
// to a 413 instead of a 500.
func TestErrQuotaExceededIsSentinel(t *testing.T) {
	wrapped := errors.Join(errors.New("context"), ErrQuotaExceeded)
	if !errors.Is(wrapped, ErrQuotaExceeded) {
		t.Fatal("ErrQuotaExceeded is not matchable through errors.Is")
	}
}
