package model

import (
	"math"
	"time"
)

// MemberLevel identifies a purchasable membership tier. 0 means the user has no
// membership. Higher tiers stack on top of lower ones: every tier grants a
// (larger) exp multiplier, and buying a tier directly grants that tier.
type MemberLevel int64

const (
	// MemberNone is the absence of a paid membership.
	MemberNone MemberLevel = 0
	// MemberMist is tier 1 "薄雾": exp ×2.
	MemberMist MemberLevel = 1
	// MemberCampfire is tier 2 "篝火": exp ×2.5.
	MemberCampfire MemberLevel = 2
	// MemberMoon is tier 3 "明月": exp ×3.
	MemberMoon MemberLevel = 3
	// MemberLoneStar is tier 4 "孤星": exp ×3.5.
	MemberLoneStar MemberLevel = 4
)

// MemberDurationDays is how long a purchased membership lasts. It applies to
// both wallet purchases and admin grants.
const MemberDurationDays = 30

// MemberStorageBonusBytes returns the storage-space bonus (in bytes) a
// membership tier grants while active: Lv.1 +100MB, Lv.2 +200MB, Lv.3 +400MB,
// Lv.4 +400MB. Non-membership levels get no bonus.
func MemberStorageBonusBytes(level int64) int64 {
	switch level {
	case 1:
		return 100 * 1024 * 1024
	case 2:
		return 200 * 1024 * 1024
	case 3, 4:
		return 400 * 1024 * 1024
	default:
		return 0
	}
}

// MemberStorageBonusAt returns the storage bonus effective at [now]: the tier
// bonus while the membership is active, otherwise 0 (so an expired membership
// reverts the user to their base quota).
func MemberStorageBonusAt(level int64, expiresAt *time.Time, now time.Time) int64 {
	if !MembershipActive(level, expiresAt, now) {
		return 0
	}
	return MemberStorageBonusBytes(level)
}

// MembershipActive reports whether a membership at [level] with [expiresAt] is
// currently active at [now].
func MembershipActive(level int64, expiresAt *time.Time, now time.Time) bool {
	return level >= int64(MemberMist) && expiresAt != nil && now.Before(*expiresAt)
}

// MemberPresetNameColors is the fixed palette that Lv.1 members may pick from.
// Higher tiers may still use these or enter a custom hex color.
var MemberPresetNameColors = []string{
	"#FF5252", "#FF7043", "#FFA726", "#FFCA28",
	"#9CCC65", "#66BB6A", "#26A69A", "#29B6F6",
	"#42A5F5", "#5C6BC0", "#AB47BC", "#EC407A",
}

// ValidHexColor reports whether s is a supported #RRGGBB hex color.
func ValidHexColor(s string) bool {
	if len(s) != 7 || s[0] != '#' {
		return false
	}
	for _, ch := range s[1:] {
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')) {
			return false
		}
	}
	return true
}

// IsPresetNameColor reports whether s is one of the fixed Lv.1 preset colors.
func IsPresetNameColor(s string) bool {
	for _, c := range MemberPresetNameColors {
		if c == s {
			return true
		}
	}
	return false
}

// MemberNameStyleCap describes what a membership level may set for its display
// name: Lv.1 = solid preset colors only, Lv.2 = any solid hex, Lv.3 = gradient
// (two colors), Lv.4 = dynamic (animated) gradient.
type MemberNameStyleCap struct {
	PresetsOnly  bool // Lv.1: color must be a preset
	AllowGradient bool // Lv.3+: a second color (name_color_to) is allowed
	AllowDynamic  bool // Lv.4: the dynamic animated gradient is allowed
}

// MemberNameStyle allowed for a level when the membership is active. Returns
// zero value (no styling) for non-members / expired memberships.
func MemberNameStyleAllowed(level int64, expiresAt *time.Time, now time.Time) MemberNameStyleCap {
	if !MembershipActive(level, expiresAt, now) {
		return MemberNameStyleCap{}
	}
	switch level {
	case 1:
		return MemberNameStyleCap{PresetsOnly: true}
	case 2:
		return MemberNameStyleCap{}
	case 3:
		return MemberNameStyleCap{AllowGradient: true}
	case 4:
		return MemberNameStyleCap{AllowGradient: true, AllowDynamic: true}
	default:
		return MemberNameStyleCap{}
	}
}

// memberTier is one static membership definition: level, display name,
// marketing text, coin price and the experience multiplier it applies to every
// exp grant while the membership is active.
type memberTier struct {
	level         int64
	name          string
	description   string
	priceCoins    int64
	expMultiplier float64
}

// memberTiers is the fixed membership catalog (walls vs. the derived exp
// level in level.go). Index [level-1]; memberTiers[0] is Lv.1.
var memberTiers = []memberTier{
	{level: 1, name: "薄雾", description: "晨光熹微，薄雾初升。经验加成 ×2。", priceCoins: 200, expMultiplier: 2.0},
	{level: 2, name: "篝火", description: "围炉而坐，火光明灭。经验加成 ×2.5。", priceCoins: 400, expMultiplier: 2.5},
	{level: 3, name: "明月", description: "举头望月，清辉遍野。经验加成 ×3。", priceCoins: 800, expMultiplier: 3.0},
	{level: 4, name: "孤星", description: "万籁俱寂，唯余孤星。经验加成 ×3.5。", priceCoins: 1600, expMultiplier: 3.5},
}

// MemberTier is the catalog entry returned to clients.
type MemberTier struct {
	Level         int64   `json:"level"`
	Name          string  `json:"name"`
	Description   string  `json:"description"`
	Price         int64   `json:"price"`
	ExpMultiplier float64 `json:"exp_multiplier"`
	DurationDays  int64   `json:"duration_days"`
	// StorageBonusMB is the storage-space bonus this tier grants (MB).
	StorageBonusMB int64 `json:"storage_bonus_mb"`
	// AllowGradient is true when this tier may pick a second gradient color
	// (Lv.3+).
	AllowGradient bool `json:"allow_gradient"`
	// AllowDynamic is true when this tier may use the animated gradient
	// (Lv.4).
	AllowDynamic bool `json:"allow_dynamic"`
	// PresetColors lists the fixed display-name colors available at this tier
	// (Lv.1 members may only choose from here; higher tiers enter any hex).
	PresetColors []string `json:"preset_colors,omitempty"`
}

// MembershipStatus is the authenticated user's membership state plus the
// purchase catalog, returned by GET /membership.
type MembershipStatus struct {
	Level       int64        `json:"level"`
	Name        string       `json:"name,omitempty"`
	Active      bool         `json:"active"`
	ExpiresAt   *time.Time   `json:"expires_at,omitempty"`
	Multiplier  float64      `json:"exp_multiplier"`
	Tiers       []MemberTier `json:"tiers"`
	MemberDays  int64        `json:"member_days"`
	MemberPrice int64        `json:"member_price,omitempty"`
}

// MemberTiers returns the static membership catalog.
func MemberTiers() []MemberTier {
	out := make([]MemberTier, 0, len(memberTiers))
	for _, t := range memberTiers {
		out = append(out, t.Catalog())
	}
	return out
}

// Catalog converts a tier definition into its client-facing representation.
func (t memberTier) Catalog() MemberTier {
	presets := MemberPresetNameColors
	if t.level >= 2 {
		presets = nil
	}
	styleCap := MemberNameStyleAllowed(t.level, nil, time.Now())
	return MemberTier{
		Level:          t.level,
		Name:           t.name,
		Description:    t.description,
		Price:          t.priceCoins,
		ExpMultiplier:  t.expMultiplier,
		DurationDays:   MemberDurationDays,
		StorageBonusMB: MemberStorageBonusBytes(t.level) / (1024 * 1024),
		AllowGradient:  styleCap.AllowGradient,
		AllowDynamic:   styleCap.AllowDynamic,
		PresetColors:   presets,
	}
}

// MemberTierForLevel returns the catalog entry for a level, treating out-of-range
// levels as the "no membership" absence (zero value, ok=false).
func MemberTierForLevel(level int64) (MemberTier, bool) {
	for _, t := range memberTiers {
		if t.level == level {
			return t.Catalog(), true
		}
	}
	return MemberTier{}, false
}

// MemberPrice returns the coin price for a membership level (0 for levels
// outside the catalog / the absence of membership).
func MemberPrice(level int64) int64 {
	if tier, ok := MemberTierForLevel(level); ok {
		return tier.Price
	}
	return 0
}

// MemberExpMultiplier returns the raw exp multiplier of a membership level,
// ignoring expiry. Non-membership levels yield 1.0.
func MemberExpMultiplier(level int64) float64 {
	if tier, ok := MemberTierForLevel(level); ok {
		return tier.ExpMultiplier
	}
	return 1.0
}

// MemberMultiplierAt returns the applied exp multiplier for a user at a point in
// time: the tier multiplier while the membership is active, otherwise 1.0.
func MemberMultiplierAt(level int64, expiresAt *time.Time, now time.Time) float64 {
	if level < int64(MemberMist) {
		return 1.0
	}
	if expiresAt == nil || !now.Before(*expiresAt) {
		return 1.0
	}
	return MemberExpMultiplier(level)
}

// ApplyMemberExp scales a base exp grant by the member multiplier effective at
// [now]. The result is rounded down so the bonus never overshoots the nominal
// multiplier (e.g. Lv.1 ×2 always yields exactly 2× the base). Non-active
// memberships return the base amount unchanged.
func ApplyMemberExp(base int64, level int64, expiresAt *time.Time, now time.Time) int64 {
	if base <= 0 {
		return base
	}
	mult := MemberMultiplierAt(level, expiresAt, now)
	if mult <= 1.0 {
		return base
	}
	return int64(math.Floor(float64(base) * mult))
}
