package model

import "time"

// Check split modes.
const (
	// CheckModeRandom splits the pool WeChat-style: each claim draws a random
	// share bounded around the remaining average; the last claim takes what is
	// left, so every cent is handed out exactly once.
	CheckModeRandom = "random"
	// CheckModeAverage gives every claimer an equal share; the last claimer
	// absorbs the rounding remainder.
	CheckModeAverage = "average"
)

// Check lifecycle statuses.
const (
	// CheckStatusActive means money is still escrowed and claims are open.
	CheckStatusActive = "active"
	// CheckStatusSettled means every share has been claimed.
	CheckStatusSettled = "settled"
	// CheckStatusRefunded means the check expired and unclaimed money went
	// back to the creator's wallet.
	CheckStatusRefunded = "refunded"
)

// CheckClaim is one user's payout from a check.
type CheckClaim struct {
	ID        int64     `json:"id"`
	CheckID   int64     `json:"check_id"`
	UserID    int64     `json:"user_id"`
	Amount    Cents     `json:"amount"`
	CreatedAt time.Time `json:"created_at"`
	// Denormalized for display.
	UserName   string `json:"user_name,omitempty"`
	UserAvatar string `json:"user_avatar,omitempty"`
}

// Check is a divisible amount of money (like a red packet) escrowed from the
// creator's wallet and claimable by other users until it expires. Checks can
// be attached to a post or sent as a chat message; unclaimed shares are
// refunded to the creator when [ExpiresAt] passes.
type Check struct {
	ID        int64 `json:"id"`
	CreatorID int64 `json:"creator_id"`
	// Total is the escrowed amount in cents.
	Total Cents `json:"total"`
	// Shares is how many users may claim a portion.
	Shares int64  `json:"shares"`
	Mode   string `json:"mode"` // random | average
	Status string `json:"status"`
	// PostID is set when the check is attached to a post.
	PostID     *int64     `json:"post_id,omitempty"`
	ExpiresAt  time.Time  `json:"expires_at"`
	RefundedAt *time.Time `json:"refunded_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`

	// Populated on reads:
	CreatorName   string       `json:"creator_name,omitempty"`
	CreatorAvatar string       `json:"creator_avatar,omitempty"`
	Claims        []CheckClaim `json:"claims,omitempty"`
	// ClaimedTotal is the sum of claimed amounts in cents.
	ClaimedTotal int64 `json:"claimed_total,omitempty"`
	// ClaimedByMe / MyAmount describe the viewer's own claim, when present.
	ClaimedByMe bool  `json:"claimed_by_me,omitempty"`
	MyAmount    Cents `json:"my_amount,omitempty"`
}
