package ratelimit

import (
	"fmt"
	"testing"
	"time"
)

// newTestLimiter returns a limiter whose clock is driven by the caller, so the
// window and lockout behaviour can be tested without sleeping.
func newTestLimiter(now *time.Time) *Limiter {
	l := New(3, time.Minute, time.Minute)
	l.now = func() time.Time { return *now }
	return l
}

func TestSweepDropsKeysWhoseFailuresAgedOut(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l := newTestLimiter(&now)

	// One failure each, below the threshold, so nothing is locked.
	l.Fail("old")
	l.Fail("also-old")

	now = now.Add(2 * time.Minute)
	l.sweepEntries(now)

	if len(l.entries) != 0 {
		t.Fatalf("expired entries survived the sweep: %d left", len(l.entries))
	}
}

func TestSweepKeepsLockedKeysUntilTheLockoutIsServed(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l := newTestLimiter(&now)

	for i := 0; i < 3; i++ {
		l.Fail("victim")
	}
	if l.RetryAfter("victim") <= 0 {
		t.Fatal("expected the key to be locked out")
	}

	// Inside the lockout: the entry still carries information and must stay.
	l.sweepEntries(now.Add(30 * time.Second))
	if l.RetryAfter("victim") <= 0 {
		t.Fatal("sweep dropped a key that is still locked out")
	}

	// Lockout served in full: the entry is dead weight.
	now = now.Add(2 * time.Minute)
	l.sweepEntries(now)
	if _, ok := l.entries["victim"]; ok {
		t.Fatal("sweep kept an entry whose lockout had expired")
	}
}

func TestFailReclaimsEntriesOnceTheTableIsLarge(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l := newTestLimiter(&now)

	// Every key is a made-up username: one failure, no lockout, then forgotten.
	for i := 0; i < sweepThreshold+100; i++ {
		l.Fail(fmt.Sprintf("invented-%d", i))
	}
	before := len(l.entries)
	if before <= sweepThreshold/2 {
		t.Fatalf("expected the table to still be large before ageing, got %d", before)
	}

	// Once those failures fall out of the window the next failure must reclaim.
	now = now.Add(2 * time.Minute)
	l.Fail("fresh")
	if got := len(l.entries); got > before/2 {
		t.Fatalf("sweep did not reclaim expired entries: %d entries left (was %d)", got, before)
	}
}
