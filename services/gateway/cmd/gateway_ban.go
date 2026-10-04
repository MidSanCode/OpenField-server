package main

import (
	"strings"
	"sync"
	"time"

	"github.com/openfield/server/pkg/repository"
)

// Ban enforcement.
//
// user_punishments wrote users.status = 'banned', but nothing on the request
// path ever read it: the gateway validated the JWT and the permission table and
// nothing else, so a banned account kept full access for the remaining lifetime
// of its access token. Checking the status on every request would put a query
// on the hot path of the whole API, so results are cached briefly — short
// enough that a ban takes effect promptly, long enough that the check is
// effectively free.
const (
	banCacheTTL = 30 * time.Second
)

type banEntry struct {
	banned  bool
	expires time.Time
}

type banCheckerType struct {
	mu    sync.RWMutex
	cache map[int64]banEntry
}

var banChecker = &banCheckerType{cache: make(map[int64]banEntry)}

// IsBannedCached reports whether the user is currently banned, consulting a
// short-lived cache. A user row that cannot be read is reported as an error so
// the caller fails closed rather than letting an unknown account through.
func (b *banCheckerType) IsBannedCached(userID int64) (bool, error) {
	now := time.Now()

	b.mu.RLock()
	entry, ok := b.cache[userID]
	b.mu.RUnlock()
	if ok && now.Before(entry.expires) {
		return entry.banned, nil
	}

	user, err := repository.NewUserRepository().GetByID(userID)
	if err != nil {
		return false, err
	}
	var banned bool
	if user != nil {
		banned = repository.NewPunishmentRepository().IsBanned(user, now)
	}

	b.mu.Lock()
	// Opportunistically drop stale entries so the map cannot grow without
	// bound on a long-running gateway.
	if len(b.cache) > 10000 {
		for id, e := range b.cache {
			if now.After(e.expires) {
				delete(b.cache, id)
			}
		}
	}
	b.cache[userID] = banEntry{banned: banned, expires: now.Add(banCacheTTL)}
	b.mu.Unlock()

	return banned, nil
}

// banAllowedPath lists the few routes a banned account may still reach: enough
// to discover why it was banned and to log out, but nothing that acts on the
// service.
func banAllowedPath(method, path string) bool {
	switch {
	case method == "GET" && path == "/api/v1/users/me":
		return true
	case method == "GET" && path == "/api/v1/users/me/punishments":
		return true
	case strings.HasPrefix(path, "/api/v1/auth/logout"):
		return true
	case strings.HasPrefix(path, "/api/v1/auth/refresh"):
		// Refusing refresh would strand the client with an expired token and no
		// way to observe the ban; the refresh path itself re-checks the status
		// and refuses to mint a new token for a banned user.
		return true
	}
	return false
}
