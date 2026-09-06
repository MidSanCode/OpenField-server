package repository

import (
	"time"

	"github.com/openfield/server/pkg/model"
)

// groupCreateQuota returns how many groups a user may own: 1000 base, +25%
// per active membership level (Lv.1 → 1250 … Lv.4 → 2000). Joining groups
// never counts against the quota — only groups the user created.
func groupCreateQuota(level int64, expiresAt *time.Time, now time.Time) int64 {
	base := int64(1000)
	if !model.MembershipActive(level, expiresAt, now) {
		return base
	}
	if level > 8 {
		level = 8 // sanity cap for unexpected grants
	}
	return base + base*level/4
}

// GroupCreateQuotaFor computes the current group-creation quota for a user
// from their membership state.
func GroupCreateQuotaFor(level int64, expiresAt *time.Time) int64 {
	return groupCreateQuota(level, expiresAt, time.Now())
}

// campCreateQuota returns how many camps a user may create: 100 base with
// the same +25%-per-level membership bonus as groups.
func campCreateQuota(level int64, expiresAt *time.Time, now time.Time) int64 {
	base := int64(100)
	if !model.MembershipActive(level, expiresAt, now) {
		return base
	}
	if level > 8 {
		level = 8
	}
	return base + base*level/4
}

// CampCreateQuotaFor computes the current camp-creation quota for a user.
func CampCreateQuotaFor(level int64, expiresAt *time.Time) int64 {
	return campCreateQuota(level, expiresAt, time.Now())
}
