// Package ratelimit provides a small in-memory failure-counting limiter used
// to slow down brute-force attacks against password and payment-PIN checks.
//
// The state lives in the process, which matches the current deployment model
// of one instance per service behind the gateway. It deliberately keeps no
// dependency on Redis so protection works even when Redis is disabled.
package ratelimit

import (
	"sync"
	"time"
)

// entry records the failures observed for one key inside the sliding window
// plus the moment the current lockout ends (zero when not locked out).
type entry struct {
	stamps   []time.Time
	lockedTo time.Time
}

// Limiter allows at most maxFailures failures per key inside window; once the
// threshold is hit the key is locked out for lockout duration. Allow must be
// checked before verifying credentials; successful verification calls Reset.
type Limiter struct {
	mu          sync.Mutex
	entries     map[string]*entry
	maxFailures int
	window      time.Duration
	lockout     time.Duration
	now         func() time.Time
}

// New creates a Limiter allowing maxFailures failures per key within window;
// exceeding the budget locks the key out for lockout.
func New(maxFailures int, window, lockout time.Duration) *Limiter {
	return &Limiter{
		entries:     make(map[string]*entry),
		maxFailures: maxFailures,
		window:      window,
		lockout:     lockout,
		now:         time.Now,
	}
}

// sweepThreshold is the entry count above which Fail reclaims dead entries. It
// is a ceiling rather than a timer so the limiter needs no background goroutine
// and behaves identically under test.
const sweepThreshold = 4096

// Allow reports whether the given key may attempt a verification right now.
// Expired window entries are pruned lazily.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	e, ok := l.entries[key]
	if !ok {
		return true
	}
	now := l.now()
	if !e.lockedTo.IsZero() {
		if now.Before(e.lockedTo) {
			return false
		}
		// Lockout served in full; give the key a fresh window.
		delete(l.entries, key)
		return true
	}
	l.prune(e, now)
	return true
}

// Fail registers a failed verification for the key. When the failure budget is
// exhausted the key becomes locked out until the lockout elapses.
func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	e, ok := l.entries[key]
	if !ok {
		e = &entry{}
		l.entries[key] = e
	}
	now := l.now()
	if !e.lockedTo.IsZero() {
		if now.Before(e.lockedTo) {
			return
		}
		delete(l.entries, key)
		e = &entry{}
		l.entries[key] = e
	}
	e.stamps = append(e.stamps, now)
	if len(e.stamps) >= l.maxFailures {
		e.lockedTo = now.Add(l.lockout)
	}
	// Keys are attacker-supplied (a submitted username, a PIN-verify user id that
	// never authenticated). Without this the map only ever shrank on Reset, so a
	// caller feeding fresh keys grew it for as long as the process lived.
	if len(l.entries) > sweepThreshold {
		l.sweepEntries(now)
	}
}

// sweepEntries forgets entries that no longer carry information: a lockout that
// has been served in full, or a key whose every failure has aged out of the
// window. Caller must hold l.mu.
func (l *Limiter) sweepEntries(now time.Time) {
	for key, e := range l.entries {
		if !e.lockedTo.IsZero() {
			if !now.Before(e.lockedTo) {
				delete(l.entries, key)
			}
			continue
		}
		l.prune(e, now)
		if len(e.stamps) == 0 {
			delete(l.entries, key)
		}
	}
}

// Reset clears the failure history for the key after a successful
// verification.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

// RetryAfter reports how long the key is still locked out (0 when allowed).
func (l *Limiter) RetryAfter(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()

	e, ok := l.entries[key]
	if !ok || e.lockedTo.IsZero() {
		return 0
	}
	d := e.lockedTo.Sub(l.now())
	if d < 0 {
		return 0
	}
	return d
}

// prune drops timestamps older than the window. Caller must hold l.mu.
func (l *Limiter) prune(e *entry, now time.Time) {
	cutoff := now.Add(-l.window)
	kept := e.stamps[:0]
	for _, t := range e.stamps {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	e.stamps = kept
}
