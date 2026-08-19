package model

import "time"

// PostTip is one coin tip on a post. The tipper is charged Amount (in cents);
// NetAmount (95% of Amount) is credited to the post author's wallet and the
// remaining 5% is the platform fee. When the post is deleted the tipper gets
// NetAmount back (refunded from the author's wallet) and RefundedAt is set.
type PostTip struct {
	ID         int64      `json:"id"`
	PostID     int64      `json:"post_id"`
	UserID     int64      `json:"user_id"`
	// Amount is the total tip the tipper paid, in cents.
	Amount int64 `json:"amount"`
	// NetAmount is the 95% that went to (and is refunded from) the author.
	NetAmount int64      `json:"net_amount"`
	RefundedAt *time.Time `json:"refunded_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}