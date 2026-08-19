package repository

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/openfield/server/pkg/database"
	"github.com/openfield/server/pkg/model"
)

// tipPlatformFeePercent is the share of each tip kept by the platform; the
// remainder (95%) goes to the post author and is refunded on post deletion.
const tipPlatformFeePercent = 5

// netTipAmount returns the author-facing amount (cents) for a tip: 95% of the
// paid cents, rounded down.
func netTipAmount(cents int64) int64 {
	return cents * (100 - tipPlatformFeePercent) / 100
}

// ErrSelfTip reports tipping one's own post.
var ErrSelfTip = errors.New("cannot tip your own post")

// TipRepository handles post coin tips and their refunds.
type TipRepository struct{}

// NewTipRepository creates a new TipRepository.
func NewTipRepository() *TipRepository {
	return &TipRepository{}
}

// Tip charges [amountCoins] coins to the tipper and credits 95% (net) to the
// post author's wallet in one transaction. The remaining 5% is the platform
// fee. The post row is locked so a concurrent delete cannot race the tip (the
// delete flow refunds tips, so the tipper still ends up even).
func (r *TipRepository) Tip(postID, tipperID int64, amountCoins int64) (*model.PostTip, error) {
	if amountCoins < 1 {
		return nil, ErrInvalidAmount
	}
	amount := amountCoins * model.MoneyScale
	net := netTipAmount(amount)

	tx, err := database.DB.Begin()
	if err != nil {
		return nil, fmt.Errorf("failed to begin tip: %w", err)
	}
	defer tx.Rollback()

	var authorID int64
	err = tx.QueryRow("SELECT user_id FROM posts WHERE id = $1 FOR UPDATE", postID).Scan(&authorID)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to lock post for tip: %w", err)
	}
	if authorID == tipperID {
		return nil, ErrSelfTip
	}

	if err := adjustBalanceTx(tx, tipperID, -amount, tipperID, "post_tip", "打赏帖子"); err != nil {
		return nil, err
	}
	if err := adjustBalanceTx(tx, authorID, net, tipperID, "post_tip_income", "帖子打赏收入"); err != nil {
		return nil, err
	}

	tip := &model.PostTip{}
	if err := tx.QueryRow(
		`INSERT INTO post_tips (post_id, user_id, amount, net_amount)
		 VALUES ($1, $2, $3, $4) RETURNING id, post_id, user_id, amount, net_amount, created_at`,
		postID, tipperID, amount, net,
	).Scan(&tip.ID, &tip.PostID, &tip.UserID, &tip.Amount, &tip.NetAmount, &tip.CreatedAt); err != nil {
		return nil, fmt.Errorf("failed to record tip: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit tip: %w", err)
	}
	return tip, nil
}

// walletBalanceTx returns the current balance of a user's wallet inside the
// given transaction (0 when no wallet row exists yet).
func walletBalanceTx(tx *sql.Tx, userID int64) (int64, error) {
	var balance int64
	err := tx.QueryRow("SELECT balance FROM wallets WHERE user_id = $1", userID).Scan(&balance)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("failed to read wallet balance: %w", err)
	}
	return balance, nil
}

// RefundForPostTx refunds every non-refunded tip on a post to its tipper,
// debiting the author's wallet by the same net amount so the payout is undone
// without minting coins. Returns the number of refunded tips. The author
// wallet is never driven negative: a tip whose refund exceeds the author's
// current balance is only refunded up to that balance (the rest is forfeited
// with the platform fee). Call this inside the caller's transaction, before an
// ON DELETE CASCADE removes the tip rows.
func (r *TipRepository) RefundForPostTx(tx *sql.Tx, postID, authorID int64) (int, error) {
	rows, err := tx.Query(
		"SELECT id, user_id, net_amount FROM post_tips WHERE post_id = $1 AND refunded_at IS NULL FOR UPDATE",
		postID,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to list tips for refund: %w", err)
	}
	type pending struct {
		id       int64
		giverID  int64
		netCents int64
	}
	var list []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.giverID, &p.netCents); err != nil {
			rows.Close()
			return 0, fmt.Errorf("failed to scan tip for refund: %w", err)
		}
		list = append(list, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("rows error: %w", err)
	}

	refunded := 0
	for _, p := range list {
		refund := p.netCents
		if refund <= 0 {
			continue
		}
		balance, err := walletBalanceTx(tx, authorID)
		if err != nil {
			return 0, err
		}
		if balance < refund {
			refund = balance
		}
		if refund <= 0 {
			continue
		}
		if err := adjustBalanceTx(tx, authorID, -refund, authorID, "post_tip_refund", "删除帖子退还打赏"); err != nil {
			return 0, err
		}
		if err := adjustBalanceTx(tx, p.giverID, refund, authorID, "post_tip_refund", "删除帖子退还打赏"); err != nil {
			return 0, err
		}
		if _, err := tx.Exec("UPDATE post_tips SET refunded_at = NOW() WHERE id = $1", p.id); err != nil {
			return 0, fmt.Errorf("failed to mark tip refunded: %w", err)
		}
		refunded++
	}
	return refunded, nil
}