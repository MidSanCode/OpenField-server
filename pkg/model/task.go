package model

import "time"

// TaskKind identifies the reward model of a task.
type TaskKind string

const (
	// TaskKindOnce is a one-time achievement claimed at most once.
	TaskKindOnce TaskKind = "once"
	// TaskKindStreak is a recurring daily-sign-in streak task. Progress is the
	// user's current consecutive check-in streak and the target is the streak
	// length required to earn the reward.
	TaskKindStreak TaskKind = "streak"
)

// Task is a built-in achievable reward definition.
type Task struct {
	ID             int64    `json:"id"`
	Code           string   `json:"code"`
	Kind           TaskKind `json:"kind"`
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	RewardExp      int64    `json:"reward_exp"`
	RewardCurrency int64    `json:"reward_currency"`
	Target         int      `json:"target"`
	Sort           int      `json:"sort"`
}

// TaskState is a Task enriched with the requesting user's progress and
// claimability. Progress semantics depend on the task kind:
//
//   - once: 1 when the underlying condition is met (e.g. >= 1 post), else 0.
//   - streak: the user's current consecutive sign-in streak.
//
// Target is the required progress to unlock the reward.
type TaskState struct {
	Task
	// Progress is the user's current progress toward Target.
	Progress int64 `json:"progress"`
	// Completed is whether the reward has already been claimed.
	Completed bool `json:"completed"`
	// Claimable is whether the user can claim the reward right now (progress
	// reached Target and the reward has not been claimed for this cycle).
	Claimable bool `json:"claimable"`
}

// ExpReason identifies the source of an experience award.
type ExpReason string

const (
	ExpReasonDailyBonus ExpReason = "daily_bonus"
	ExpReasonMakeup     ExpReason = "makeup"
	ExpReasonTask       ExpReason = "task"
	ExpReasonAdjust     ExpReason = "adjust"
)

// ExpEntry is one recorded experience award in a user's history.
type ExpEntry struct {
	ID          int64     `json:"id"`
	UserID      int64     `json:"user_id"`
	Amount      int64     `json:"amount"`
	Reason      ExpReason `json:"reason"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

// TransferStatus is the lifecycle state of a user-to-user transfer.
type TransferStatus string

const (
	TransferPending  TransferStatus = "pending"
	TransferAccepted TransferStatus = "accepted"
	TransferDeclined TransferStatus = "declined"
	TransferRefunded TransferStatus = "refunded"
)

// Transfer is a pending or settled user-to-user currency transfer.
type Transfer struct {
	ID              int64          `json:"id"`
	SenderID        int64          `json:"sender_id"`
	RecipientID     int64          `json:"recipient_id"`
	Amount          int64          `json:"amount"`
	Status          TransferStatus `json:"status"`
	Note            string         `json:"note"`
	CreatedAt       time.Time      `json:"created_at"`
	DecidedAt       *time.Time     `json:"decided_at,omitempty"`
	RefundedAt      *time.Time     `json:"refunded_at,omitempty"`
	SenderName      string         `json:"sender_name,omitempty"`
	SenderAvatar    string         `json:"sender_avatar,omitempty"`
	RecipientName   string         `json:"recipient_name,omitempty"`
	RecipientAvatar string         `json:"recipient_avatar,omitempty"`
}
