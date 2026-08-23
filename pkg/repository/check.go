package repository

import (
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"time"

	"github.com/lib/pq"
	"github.com/openfield/server/pkg/database"
	"github.com/openfield/server/pkg/model"
)

// Check errors.
var (
	// ErrInvalidCheck reports a malformed check request (bad amount/shares/mode/TTL).
	ErrInvalidCheck = errors.New("invalid check")
	// ErrCheckSettled reports claiming an already fully claimed or refunded check.
	ErrCheckSettled = errors.New("check is no longer claimable")
	// ErrCheckAlreadyClaimed reports a second claim by the same user.
	ErrCheckAlreadyClaimed = errors.New("you have already claimed this check")
)

// Check limits: between 0.01 and 10000 coins total, 1..500 shares, and a
// lifetime of at least one hour and at most 30 days.
const (
	checkMinTotalCents = 1
	checkMaxTotalCents = 10000 * model.MoneyScale
	checkMaxShares     = 500
)

// CheckRepository handles red-packet style checks: escrow on create,
// per-share payouts on claim and expiry refunds.
type CheckRepository struct{}

// NewCheckRepository creates a new CheckRepository.
func NewCheckRepository() *CheckRepository {
	return &CheckRepository{}
}

// Create escrows [totalCents] from the creator's wallet and stores a check
// with [shares] claims expiring after [ttl]. The whole operation is atomic:
// when the wallet cannot cover the total nothing is stored.
func (r *CheckRepository) Create(creatorID int64, totalCents int64, shares int64, mode string, ttl time.Duration) (*model.Check, error) {
	if totalCents < checkMinTotalCents || totalCents > checkMaxTotalCents {
		return nil, ErrInvalidCheck
	}
	if shares < 1 || shares > checkMaxShares {
		return nil, ErrInvalidCheck
	}
	if mode != model.CheckModeRandom && mode != model.CheckModeAverage {
		return nil, ErrInvalidCheck
	}
	if ttl < time.Hour || ttl > 30*24*time.Hour {
		return nil, ErrInvalidCheck
	}

	tx, err := database.DB.Begin()
	if err != nil {
		return nil, fmt.Errorf("failed to begin check create: %w", err)
	}
	defer tx.Rollback()

	if err := adjustBalanceTx(tx, creatorID, -totalCents, creatorID, "check_send", "支票冻结"); err != nil {
		return nil, err
	}

	now := time.Now()
	c := &model.Check{}
	var postID sql.NullInt64
	err = tx.QueryRow(
		`INSERT INTO checks (creator_id, total, shares, mode, status, expires_at)
		 VALUES ($1, $2, $3, $4, 'active', $5)
		 RETURNING id, creator_id, total, shares, mode, status, post_id, expires_at, created_at`,
		creatorID, totalCents, shares, mode, now.Add(ttl),
	).Scan(&c.ID, &c.CreatorID, &c.Total, &c.Shares, &c.Mode, &c.Status, &postID, &c.ExpiresAt, &c.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to insert check: %w", err)
	}
	if postID.Valid {
		id := postID.Int64
		c.PostID = &id
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit check create: %w", err)
	}
	return c, nil
}

// AttachToPost binds an active, unattached check owned by [userID] to a post.
func (r *CheckRepository) AttachToPost(checkID, userID, postID int64) error {
	result, err := database.DB.Exec(
		`UPDATE checks SET post_id = $3
		 WHERE id = $1 AND creator_id = $2 AND status = 'active' AND expires_at > NOW() AND post_id IS NULL`,
		checkID, userID, postID,
	)
	if err != nil {
		return fmt.Errorf("failed to attach check: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return ErrInvalidCheck
	}
	return nil
}

// ValidateOwnedActive reports whether [checkID] names an existing check
// created by [userID] that is still claimable. Used by senders (posts/chat)
// before attaching.
func (r *CheckRepository) ValidateOwnedActive(checkID, userID int64) error {
	var status string
	var creatorID int64
	var expiresAt time.Time
	err := database.DB.QueryRow(
		`SELECT creator_id, status, expires_at FROM checks WHERE id = $1`,
		checkID,
	).Scan(&creatorID, &status, &expiresAt)
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("failed to load check: %w", err)
	}
	if creatorID != userID || status != model.CheckStatusActive || !time.Now().Before(expiresAt) {
		return ErrInvalidCheck
	}
	return nil
}

// GetByID loads a check with creator info, claims and viewer-specific fields.
// An expired but unrefunded check is lazily refunded before being returned so
// clients never observe stale "claimable" state.
func (r *CheckRepository) GetByID(id, viewerID int64) (*model.Check, error) {
	expired, err := r.refundIfExpired(id)
	if err == nil && expired {
		// fall through: re-read below reflects the refunded state.
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	c := &model.Check{}
	var postID sql.NullInt64
	err = database.DB.QueryRow(
		`SELECT c.id, c.creator_id, c.total, c.shares, c.mode, c.status, c.post_id, c.expires_at, c.refunded_at, c.created_at,
		        COALESCE(NULLIF(u.nickname, ''), u.username), u.avatar_url
		 FROM checks c
		 JOIN users u ON c.creator_id = u.id
		 WHERE c.id = $1`,
		id,
	).Scan(&c.ID, &c.CreatorID, &c.Total, &c.Shares, &c.Mode, &c.Status, &postID, &c.ExpiresAt, &c.RefundedAt, &c.CreatedAt, &c.CreatorName, &c.CreatorAvatar)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load check: %w", err)
	}
	if postID.Valid {
		pid := postID.Int64
		c.PostID = &pid
	}

	rows, err := database.DB.Query(
		`SELECT cl.id, cl.check_id, cl.user_id, cl.amount, cl.created_at,
		        COALESCE(NULLIF(u.nickname, ''), u.username), u.avatar_url
		 FROM check_claims cl
		 JOIN users u ON cl.user_id = u.id
		 WHERE cl.check_id = $1
		 ORDER BY cl.id ASC`,
		id,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load check claims: %w", err)
	}
	defer rows.Close()
	claims := make([]model.CheckClaim, 0)
	var claimedTotal int64
	for rows.Next() {
		var cl model.CheckClaim
		if err := rows.Scan(&cl.ID, &cl.CheckID, &cl.UserID, &cl.Amount, &cl.CreatedAt, &cl.UserName, &cl.UserAvatar); err != nil {
			return nil, fmt.Errorf("failed to scan check claim: %w", err)
		}
		claims = append(claims, cl)
		claimedTotal += int64(cl.Amount)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	c.Claims = claims
	c.ClaimedTotal = claimedTotal
	for _, cl := range claims {
		if cl.UserID == viewerID {
			c.ClaimedByMe = true
			c.MyAmount = cl.Amount
			break
		}
	}
	return c, nil
}

// Claim pays the caller one share of the check inside a single transaction.
// Random mode draws WeChat-style: each share is random in [0.01, 2×average]
// of the remaining pool, guaranteeing every later share keeps ≥ 0.01; the
// final share takes whatever remains. Average mode splits equally and the
// last claimer absorbs the rounding remainder.
func (r *CheckRepository) Claim(id, userID int64) (*model.CheckClaim, error) {
	tx, err := database.DB.Begin()
	if err != nil {
		return nil, fmt.Errorf("failed to begin claim: %w", err)
	}
	defer tx.Rollback()

	c := &model.Check{}
	err = tx.QueryRow(
		`SELECT id, creator_id, total, shares, mode, status, expires_at FROM checks WHERE id = $1 FOR UPDATE`,
		id,
	).Scan(&c.ID, &c.CreatorID, &c.Total, &c.Shares, &c.Mode, &c.Status, &c.ExpiresAt)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to lock check: %w", err)
	}

	claimedCount := int64(0)
	var claimedSum int64
	if err := tx.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(amount), 0) FROM check_claims WHERE check_id = $1`,
		id,
	).Scan(&claimedCount, &claimedSum); err != nil {
		return nil, fmt.Errorf("failed to count check claims: %w", err)
	}

	// Expired while still holding money: refund the remainder to the creator
	// and reject the claim.
	if c.Status == model.CheckStatusActive && !time.Now().Before(c.ExpiresAt) {
		remaining := int64(c.Total) - claimedSum
		if remaining > 0 {
			if err := adjustBalanceTx(tx, c.CreatorID, remaining, c.CreatorID, "check_refund", "支票过期退回"); err != nil {
				return nil, err
			}
		}
		if _, err := tx.Exec(
			`UPDATE checks SET status = 'refunded', refunded_at = NOW() WHERE id = $1`, id,
		); err != nil {
			return nil, fmt.Errorf("failed to mark check refunded: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("failed to commit expiry refund: %w", err)
		}
		return nil, ErrCheckSettled
	}

	if c.Status != model.CheckStatusActive {
		return nil, ErrCheckSettled
	}
	if claimedCount >= c.Shares {
		return nil, ErrCheckSettled
	}

	var existing int64
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM check_claims WHERE check_id = $1 AND user_id = $2`,
		id, userID,
	).Scan(&existing); err != nil {
		return nil, fmt.Errorf("failed to check existing claim: %w", err)
	}
	if existing > 0 {
		return nil, ErrCheckAlreadyClaimed
	}

	remaining := int64(c.Total) - claimedSum
	sharesLeft := c.Shares - claimedCount
	amount := int64(0)
	switch c.Mode {
	case model.CheckModeAverage:
		amount = int64(c.Total) / c.Shares
		if sharesLeft == 1 {
			// Last share absorbs rounding leftovers.
			amount = remaining
		}
	default: // random
		if sharesLeft == 1 {
			amount = remaining
		} else {
			avg := remaining / sharesLeft
			// Bound: [1, 2*avg-1] keeps the pool solvent for the rest.
			upper := 2*avg - 1
			if upper < 1 {
				upper = 1
			}
			amount = rand.Int63n(upper) + 1
			maxForSolvent := remaining - (sharesLeft-1)*checkMinTotalCents
			if maxForSolvent < 1 {
				maxForSolvent = 1
			}
			if amount > maxForSolvent {
				amount = maxForSolvent
			}
		}
	}
	if amount < checkMinTotalCents || amount > remaining {
		return nil, ErrInvalidCheck
	}

	if err := adjustBalanceTx(tx, userID, amount, c.CreatorID, "check_receive", "领取支票"); err != nil {
		return nil, err
	}

	cl := &model.CheckClaim{}
	err = tx.QueryRow(
		`INSERT INTO check_claims (check_id, user_id, amount) VALUES ($1, $2, $3)
		 RETURNING id, check_id, user_id, amount, created_at`,
		id, userID, amount,
	).Scan(&cl.ID, &cl.CheckID, &cl.UserID, &cl.Amount, &cl.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to record claim: %w", err)
	}

	// All shares handed out: close the check.
	if claimedCount+1 >= c.Shares {
		if _, err := tx.Exec(
			`UPDATE checks SET status = 'settled' WHERE id = $1`, id,
		); err != nil {
			return nil, fmt.Errorf("failed to settle check: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit claim: %w", err)
	}
	return cl, nil
}

// RefundExpired settles every active check past its expiry by refunding the
// unclaimed remainder to the creator. Returns the number of checks refunded.
func (r *CheckRepository) RefundExpired(now time.Time) (int64, error) {
	idRows, err := database.DB.Query(
		`SELECT id FROM checks WHERE status = 'active' AND expires_at < $1`,
		now,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to query expired checks: %w", err)
	}
	ids := make([]int64, 0)
	for idRows.Next() {
		var id int64
		if err := idRows.Scan(&id); err != nil {
			idRows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	idRows.Close()
	if err := idRows.Err(); err != nil {
		return 0, err
	}

	var refunded int64
	for _, id := range ids {
		ok, err := r.refundIfExpired(id)
		if err != nil {
			continue
		}
		if ok {
			refunded++
		}
	}
	return refunded, nil
}

// refundIfExpired refunds an active expired check exactly once. Reports
// whether a refund happened. Safe against races: the row lock re-validates
// status and expiry before paying out.
func (r *CheckRepository) refundIfExpired(id int64) (bool, error) {
	tx, err := database.DB.Begin()
	if err != nil {
		return false, fmt.Errorf("failed to begin check refund: %w", err)
	}
	defer tx.Rollback()

	c := &model.Check{}
	err = tx.QueryRow(
		`SELECT id, creator_id, status, expires_at FROM checks WHERE id = $1 FOR UPDATE`,
		id,
	).Scan(&c.ID, &c.CreatorID, &c.Status, &c.ExpiresAt)
	if err == sql.ErrNoRows {
		return false, ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("failed to lock check: %w", err)
	}
	if c.Status != model.CheckStatusActive || time.Now().Before(c.ExpiresAt) {
		return false, nil
	}

	var claimedSum int64
	if err := tx.QueryRow(
		`SELECT COALESCE(SUM(amount), 0) FROM check_claims WHERE check_id = $1`, id,
	).Scan(&claimedSum); err != nil {
		return false, fmt.Errorf("failed to sum check claims: %w", err)
	}

	remaining := int64(c.Total) - claimedSum
	if remaining > 0 {
		if err := adjustBalanceTx(tx, c.CreatorID, remaining, c.CreatorID, "check_refund", "支票过期退回"); err != nil {
			return false, err
		}
	}
	if _, err := tx.Exec(
		`UPDATE checks SET status = 'refunded', refunded_at = NOW() WHERE id = $1`, id,
	); err != nil {
		return false, fmt.Errorf("failed to mark check refunded: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("failed to commit check refund: %w", err)
	}
	return true, nil
}

// populateChecks attaches the check object to posts that carry one. Claims are
// not loaded here — the detail endpoint returns them via GetByID.
func populateChecks(posts []model.Post, postIDs []int64, viewerID int64) error {
	if len(postIDs) == 0 {
		return nil
	}
	rows, err := database.DB.Query(
		`SELECT DISTINCT ON (post_id)
		        post_id, c.id, c.creator_id, c.total, c.shares, c.mode, c.status, c.expires_at,
		        COALESCE(NULLIF(u.nickname, ''), u.username), u.avatar_url,
		        (SELECT COALESCE(SUM(amount), 0) FROM check_claims cl WHERE cl.check_id = c.id),
		        EXISTS (SELECT 1 FROM check_claims mc WHERE mc.check_id = c.id AND mc.user_id = $2),
		        COALESCE((SELECT amount FROM check_claims mc WHERE mc.check_id = c.id AND mc.user_id = $2), 0)
		 FROM checks c
		 JOIN users u ON c.creator_id = u.id
		 WHERE post_id = ANY($1)`,
		pq.Array(postIDs), viewerID,
	)
	if err != nil {
		return fmt.Errorf("failed to query post checks: %w", err)
	}
	defer rows.Close()

	byPost := make(map[int64]*model.Check)
	for rows.Next() {
		var postID int64
		c := &model.Check{}
		var myAmount int64
		var claimedByMe bool
		if err := rows.Scan(&postID, &c.ID, &c.CreatorID, &c.Total, &c.Shares, &c.Mode, &c.Status, &c.ExpiresAt, &c.CreatorName, &c.CreatorAvatar, &c.ClaimedTotal, &claimedByMe, &myAmount); err != nil {
			return fmt.Errorf("failed to scan post check: %w", err)
		}
		c.ClaimedByMe = claimedByMe
		c.MyAmount = model.Cents(myAmount)
		byPost[postID] = c
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("rows error: %w", err)
	}
	for i := range posts {
		if c, ok := byPost[posts[i].ID]; ok {
			posts[i].Check = c
		}
	}
	return nil
}
