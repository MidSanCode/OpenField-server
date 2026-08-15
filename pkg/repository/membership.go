package repository

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/openfield/server/pkg/database"
	"github.com/openfield/server/pkg/model"
)

// ErrInvalidMemberLevel reports buying or granting a membership level outside
// the purchaseable catalog (1-4).
var ErrInvalidMemberLevel = fmt.Errorf("invalid membership level")

// membershipRepoMux unpins the helper types shared with wallet.go.
type MembershipRepository struct {
	userRepo *UserRepository
}

// NewMembershipRepository creates a new MembershipRepository.
func NewMembershipRepository() *MembershipRepository {
	return &MembershipRepository{userRepo: NewUserRepository()}
}

// SetForUser directly writes a membership level + expiry for a user. Used by
// admin grants and the atomic purchase flow. An out-of-range level (0) clears
// the membership.
func (r *MembershipRepository) SetForUser(userID, level int64, expiresAt *time.Time) error {
	if level < 0 || level > int64(model.MemberLoneStar) {
		return ErrInvalidMemberLevel
	}
	if level == 0 {
		expiresAt = nil
	}
	if _, err := database.DB.Exec(
		"UPDATE users SET member_level = $2, member_expires_at = $3, updated_at = NOW() WHERE id = $1",
		userID, level, expiresAt,
	); err != nil {
		return fmt.Errorf("failed to set membership: %w", err)
	}
	return nil
}

// Grant applies an admin membership grant: the target level is written directly
// and expires [days] from now (0 days is a no-op / clear). The buyer-less grant
// never touches the wallet.
func (r *MembershipRepository) Grant(userID, level, days int64, now time.Time) error {
	if level < 0 || level > int64(model.MemberLoneStar) {
		return ErrInvalidMemberLevel
	}
	if level == 0 {
		return r.SetForUser(userID, 0, nil)
	}
	if days <= 0 {
		days = model.MemberDurationDays
	}
	expires := now.AddDate(0, 0, int(days))
	return r.SetForUser(userID, level, &expires)
}

// GetForUser returns the user's membership state at a given time. A record that
// lacks a future expiry is reported as inactive (level stays stored).
func (r *MembershipRepository) GetForUser(userID int64, now time.Time) (*model.MembershipStatus, error) {
	user, err := r.userRepo.GetByID(userID)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, ErrNotFound
	}
	return membershipStatus(user, now), nil
}

// Purchase buys a membership for a user: it deducts the tier's coin price from
// the wallet (in cents) and extends/starts the membership. Buying a higher tier
// while already active upgrades the level; buying any tier while active renews
// from the current expiry. The wallet debit and membership write share one
// transaction so a rejected payment never grants membership.
func (r *MembershipRepository) Purchase(userID, level int64, now time.Time) (*model.MembershipStatus, error) {
	if level < 1 || level > int64(model.MemberLoneStar) {
		return nil, ErrInvalidMemberLevel
	}
	price := model.MemberPrice(level)
	if price <= 0 {
		return nil, ErrInvalidMemberLevel
	}

	tx, err := database.DB.Begin()
	if err != nil {
		return nil, fmt.Errorf("failed to begin membership purchase: %w", err)
	}
	defer tx.Rollback()

	var currentLevel int64
	var expiresAt sql.NullTime
	if err := tx.QueryRow(
		"SELECT member_level, member_expires_at FROM users WHERE id = $1 FOR UPDATE",
		userID,
	).Scan(&currentLevel, &expiresAt); err != nil {
		return nil, fmt.Errorf("failed to lock user for membership: %w", err)
	}

	// Renewal base: the current expiry when it is still in the future,
	// otherwise today. An upgrade always extends from today's purchase.
	base := now
	active := currentLevel > 0 && expiresAt.Valid && now.Before(expiresAt.Time)
	if active {
		base = expiresAt.Time
	}
	newLevel := level
	if active && currentLevel > level {
		newLevel = currentLevel
	}
	newExpiry := base.AddDate(0, 0, model.MemberDurationDays)

	// Debit the wallet (fails on insufficient balance, rolling back everything).
	if err := adjustBalanceTx(tx, userID, -model.MoneyScale*price, userID, "membership", "购买会员"); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(
		"UPDATE users SET member_level = $2, member_expires_at = $3, updated_at = NOW() WHERE id = $1",
		userID, newLevel, newExpiry,
	); err != nil {
		return nil, fmt.Errorf("failed to grant membership: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit membership purchase: %w", err)
	}

	return r.GetForUser(userID, now)
}

// membershipStatus builds a MembershipStatus from a loaded user row.
func membershipStatus(user *model.User, now time.Time) *model.MembershipStatus {
	active := user.MemberLevel > 0 && user.MemberExpiresAt != nil && now.Before(*user.MemberExpiresAt)
	mult := model.MemberMultiplierAt(user.MemberLevel, user.MemberExpiresAt, now)
	status := &model.MembershipStatus{
		Level:       user.MemberLevel,
		Active:      active,
		ExpiresAt:   user.MemberExpiresAt,
		Multiplier:  mult,
		Tiers:       model.MemberTiers(),
		MemberDays:  model.MemberDurationDays,
		MemberPrice: model.MemberPrice(user.MemberLevel),
	}
	if tier, ok := model.MemberTierForLevel(user.MemberLevel); ok {
		status.Name = tier.Name
	}
	return status
}
