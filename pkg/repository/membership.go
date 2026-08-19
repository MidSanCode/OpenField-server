package repository

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/openfield/server/pkg/database"
	"github.com/openfield/server/pkg/logger"
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

// PurchaseResult describes a completed membership purchase: the new state plus
// what was actually charged (the price difference when upgrading) and the kind
// of purchase (purchase | renew | upgrade).
type PurchaseResult struct {
	Status *model.MembershipStatus
	Paid   int64  `json:"paid"`
	Kind   string `json:"kind"` // purchase | renew | upgrade
}

// Purchase buys a membership for a user: it deducts the tier's coin price from
// the wallet (in cents), extends/starts the membership and records a purchase
// history row. Pricing rules:
//
//   - New purchase (no active membership): full price of the target tier,
//     30 days from today.
//   - Renewal (buying the currently active tier again): full price, 30 days
//     added to the current expiry.
//   - Upgrade (buying a higher tier while active): only the price difference
//     is charged and the remaining membership time is kept; the level moves to
//     the higher tier without adding extra days.
//
// Buying a lower tier than the currently active one is rejected.
//
// The wallet debit and membership write share one transaction so a rejected
// payment never grants membership.
func (r *MembershipRepository) Purchase(userID, level int64, now time.Time) (*PurchaseResult, error) {
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

	active := currentLevel > 0 && expiresAt.Valid && now.Before(expiresAt.Time)

	var (
		newLevel   int64
		newExpiry  time.Time
		charge     int64
		kind       string
	)
	if !active {
		// Fresh purchase: full price, 30 days from today.
		newLevel = level
		newExpiry = now.AddDate(0, 0, model.MemberDurationDays)
		charge = price
		kind = "purchase"
	} else if level == currentLevel {
		// Renewal: full price, 30 days added to the current expiry.
		newLevel = currentLevel
		newExpiry = expiresAt.Time.AddDate(0, 0, model.MemberDurationDays)
		charge = price
		kind = "renew"
	} else if level > currentLevel {
		// Upgrade by price difference: keep the remaining time.
		currentPrice := model.MemberPrice(currentLevel)
		charge = price - currentPrice
		if charge <= 0 {
			return nil, ErrInvalidMemberLevel
		}
		newLevel = level
		newExpiry = expiresAt.Time
		kind = "upgrade"
	} else {
		return nil, ErrInvalidMemberLevel
	}

	// Debit the wallet (fails on insufficient balance, rolling back everything).
	if err := adjustBalanceTx(tx, userID, -model.MoneyScale*charge, userID, "membership", "购买会员"); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(
		"UPDATE users SET member_level = $2, member_expires_at = $3, updated_at = NOW() WHERE id = $1",
		userID, newLevel, newExpiry,
	); err != nil {
		return nil, fmt.Errorf("failed to grant membership: %w", err)
	}
	if _, err := tx.Exec(
		`INSERT INTO membership_purchases (user_id, level, price_coins, kind)
		 VALUES ($1, $2, $3, $4)`,
		userID, newLevel, charge, kind,
	); err != nil {
		return nil, fmt.Errorf("failed to record membership purchase: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit membership purchase: %w", err)
	}

	status, err := r.GetForUser(userID, now)
	if err != nil {
		return nil, err
	}
	return &PurchaseResult{Status: status, Paid: charge, Kind: kind}, nil
}

// ListPurchases returns the user's membership purchase history, newest first.
func (r *MembershipRepository) ListPurchases(userID int64, page, limit int) ([]model.MembershipPurchase, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	offset := (page - 1) * limit

	rows, err := database.DB.Query(
		`SELECT id, level, price_coins, kind, created_at
		 FROM membership_purchases
		 WHERE user_id = $1
		 ORDER BY id DESC
		 LIMIT $2 OFFSET $3`,
		userID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list membership purchases: %w", err)
	}
	defer rows.Close()

	purchases := make([]model.MembershipPurchase, 0)
	for rows.Next() {
		var p model.MembershipPurchase
		if err := rows.Scan(&p.ID, &p.Level, &p.PriceCoins, &p.Kind, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan membership purchase: %w", err)
		}
		if tier, ok := model.MemberTierForLevel(p.Level); ok {
			p.TierName = tier.Name
		}
		purchases = append(purchases, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return purchases, nil
}

// SetAutoRenew flips the user's opt-in automatic-renewal flag. Automatic
// renewal re-charges the current tier within 24h of expiry (or up to a week
// late) and extends the membership by the standard duration; it is switched
// back off when a renewal cannot be paid.
func (r *MembershipRepository) SetAutoRenew(userID int64, enabled bool) error {
	if _, err := database.DB.Exec(
		"UPDATE users SET auto_renew = $2, updated_at = NOW() WHERE id = $1",
		userID, enabled,
	); err != nil {
		return fmt.Errorf("failed to set auto-renew: %w", err)
	}
	return nil
}

// renewWindowBefore / renewWindowAfter bound when a membership qualifies for an
// automatic renewal: within the last day of the term, or up to a week after it
// lapsed (covering offline users whose sweeper run was late).
const (
	renewWindowBefore = 24 * time.Hour
	renewWindowAfter  = 7 * 24 * time.Hour
)

// RenewDue auto-renews every membership whose owner opted in and whose term is
// inside the renewal window. Each user is charged the full price of their
// current tier (like a manual renewal) and the term is extended by 30 days;
// when the wallet cannot cover the charge the auto-renew flag is turned off so
// the sweeper stops retrying. Returns the number of successfully renewed
// memberships.
func (r *MembershipRepository) RenewDue(now time.Time) (int, error) {
	rows, err := database.DB.Query(
		`SELECT id, member_level
		 FROM users
		 WHERE auto_renew = TRUE
		   AND member_level BETWEEN 1 AND $1
		   AND member_expires_at IS NOT NULL
		   AND member_expires_at <= $2
		   AND member_expires_at > $3`,
		int64(model.MemberLoneStar), now.Add(-renewWindowBefore), now.Add(-renewWindowAfter),
	)
	if err != nil {
		return 0, fmt.Errorf("failed to query due renewals: %w", err)
	}
	defer rows.Close()

	type due struct {
		userID int64
		level  int64
	}
	var dueList []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.userID, &d.level); err != nil {
			return 0, fmt.Errorf("failed to scan due renewal: %w", err)
		}
		dueList = append(dueList, d)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("rows error: %w", err)
	}

	renewed := 0
	for _, d := range dueList {
		if _, err := r.Purchase(d.userID, d.level, now); err != nil {
			if errors.Is(err, ErrInsufficientBalance) {
				logger.Log.Warn("auto-renew skipped: insufficient balance", "user_id", d.userID)
				if setErr := r.SetAutoRenew(d.userID, false); setErr != nil {
					logger.Log.Error("failed to disable auto-renew", "error", setErr, "user_id", d.userID)
				}
				continue
			}
			logger.Log.Error("auto-renew failed", "error", err, "user_id", d.userID, "level", d.level)
			continue
		}
		renewed++
	}
	return renewed, nil
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
		AutoRenew:   user.AutoRenew,
	}
	if tier, ok := model.MemberTierForLevel(user.MemberLevel); ok {
		status.Name = tier.Name
	}
	return status
}
