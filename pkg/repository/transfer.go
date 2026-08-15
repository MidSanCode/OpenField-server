package repository

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/openfield/server/pkg/database"
	"github.com/openfield/server/pkg/model"
)

// TransferStatus errors.
var (
	// ErrInvalidTransferAmount reports an amount that is not positive.
	ErrInvalidTransferAmount = errors.New("invalid transfer amount")
	// ErrSelfTransfer reports a transfer to the sender's own account.
	ErrSelfTransfer = errors.New("cannot transfer to yourself")
	// ErrTransferNotPending reports acting on a transfer that already settled.
	ErrTransferNotPending = errors.New("transfer already settled")
	// ErrTransferUnauthorized reports a non-recipient trying to accept/decline.
	ErrTransferUnauthorized = errors.New("not the transfer recipient")
)

// transferSettleTTL is how long a pending transfer may remain unanswered before
// it is automatically refunded to the sender.
const transferSettleTTL = 24 * time.Hour

// TransferRepository handles user-to-user currency transfers.
type TransferRepository struct{}

// NewTransferRepository creates a new TransferRepository.
func NewTransferRepository() *TransferRepository {
	return &TransferRepository{}
}

// Create drafts a pending transfer: the amount is locked (deducted) from the
// sender's wallet immediately and credited to the recipient only when they
// accept. If the recipient declines — or 24 hours pass unanswered — the amount
// is refunded to the sender.
func (r *TransferRepository) Create(senderID, recipientID, amount int64, note string) (*model.Transfer, error) {
	if amount <= 0 {
		return nil, ErrInvalidTransferAmount
	}
	if senderID == recipientID {
		return nil, ErrSelfTransfer
	}

	tx, err := database.DB.Begin()
	if err != nil {
		return nil, fmt.Errorf("failed to begin transfer: %w", err)
	}
	defer tx.Rollback()

	// Hold the amount from the sender.
	if err := adjustBalanceTx(tx, senderID, -amount, senderID, "transfer_send", "转账冻结"); err != nil {
		if errors.Is(err, ErrInsufficientBalance) {
			return nil, ErrInsufficientBalance
		}
		return nil, err
	}

	var transferID int64
	err = tx.QueryRow(
		`INSERT INTO transfers (sender_id, recipient_id, amount, status, note)
		 VALUES ($1, $2, $3, 'pending', $4) RETURNING id`,
		senderID, recipientID, amount, note,
	).Scan(&transferID)
	if err != nil {
		return nil, fmt.Errorf("failed to create transfer: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit transfer: %w", err)
	}

	return &model.Transfer{
		ID:          transferID,
		SenderID:    senderID,
		RecipientID: recipientID,
		Amount:      model.NewCents(amount),
		Status:      model.TransferPending,
		Note:        note,
		CreatedAt:   time.Now(),
	}, nil
}

// Accept settles a pending transfer in favor of the recipient: the locked
// amount is credited to the recipient's wallet.
func (r *TransferRepository) Accept(transferID, recipientID int64) (*model.Transfer, error) {
	tx, err := database.DB.Begin()
	if err != nil {
		return nil, fmt.Errorf("failed to begin transfer accept: %w", err)
	}
	defer tx.Rollback()

	transfer, err := lockTransferTx(tx, transferID)
	if err != nil {
		return nil, err
	}
	if transfer.RecipientID != recipientID {
		return nil, ErrTransferUnauthorized
	}
	if transfer.Status != model.TransferPending {
		return nil, ErrTransferNotPending
	}

	if err := adjustBalanceTx(tx, recipientID, int64(transfer.Amount), transfer.SenderID, "transfer_receive", "收到转账"); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(
		`UPDATE transfers SET status = 'accepted', decided_at = NOW() WHERE id = $1`,
		transferID,
	); err != nil {
		return nil, fmt.Errorf("failed to update transfer: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit accept: %w", err)
	}
	transfer.Status = model.TransferAccepted
	return transfer, nil
}

// Decline settles a pending transfer by refunding the amount to the sender.
func (r *TransferRepository) Decline(transferID, recipientID int64) (*model.Transfer, error) {
	tx, err := database.DB.Begin()
	if err != nil {
		return nil, fmt.Errorf("failed to begin transfer decline: %w", err)
	}
	defer tx.Rollback()

	transfer, err := lockTransferTx(tx, transferID)
	if err != nil {
		return nil, err
	}
	if transfer.RecipientID != recipientID {
		return nil, ErrTransferUnauthorized
	}
	if transfer.Status != model.TransferPending {
		return nil, ErrTransferNotPending
	}

	if err := adjustBalanceTx(tx, transfer.SenderID, int64(transfer.Amount), transfer.SenderID, "transfer_refund", "转账退回"); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(
		`UPDATE transfers SET status = 'declined', decided_at = NOW() WHERE id = $1`,
		transferID,
	); err != nil {
		return nil, fmt.Errorf("failed to update transfer: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit decline: %w", err)
	}
	transfer.Status = model.TransferDeclined
	return transfer, nil
}

// RefundExpired settles every pending transfer older than 24h by refunding the
// sender. Returns the number of transfers refunded.
func (r *TransferRepository) RefundExpired(now time.Time) (int64, error) {
	// Collect candidate ids first (outside a transaction lock) then settle each
	// one; the per-transfer lock guards against concurrent accept/decline.
	idRows, err := database.DB.Query(
		`SELECT id FROM transfers WHERE status = 'pending' AND created_at < $1`,
		now.Add(-transferSettleTTL),
	)
	if err != nil {
		return 0, fmt.Errorf("failed to query expired transfers: %w", err)
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
		tx, err := database.DB.Begin()
		if err != nil {
			return refunded, err
		}
		transfer, err := lockTransferTx(tx, id)
		if err != nil {
			tx.Rollback()
			continue
		}
		if transfer.Status != model.TransferPending {
			tx.Commit()
			continue
		}
		if err := adjustBalanceTx(tx, transfer.SenderID, int64(transfer.Amount), transfer.SenderID, "transfer_refund", "转账超时退回"); err != nil {
			tx.Rollback()
			continue
		}
		if _, err := tx.Exec(
			`UPDATE transfers SET status = 'refunded', decided_at = NOW(), refunded_at = NOW() WHERE id = $1`,
			id,
		); err != nil {
			tx.Rollback()
			continue
		}
		if err := tx.Commit(); err != nil {
			continue
		}
		refunded++
	}
	return refunded, nil
}

// ListIncoming returns transfers addressed to the user with the latest first.
func (r *TransferRepository) ListIncoming(userID int64, page, limit int) ([]model.Transfer, error) {
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	if page < 1 {
		page = 1
	}
	offset := (page - 1) * limit

	rows, err := database.DB.Query(
		`SELECT id, sender_id, recipient_id, amount, status, note, created_at, decided_at, refunded_at
		 FROM transfers WHERE recipient_id = $1
		 ORDER BY id DESC LIMIT $2 OFFSET $3`,
		userID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list incoming transfers: %w", err)
	}
	defer rows.Close()

	transfers, err := scanTransfers(rows)
	if err != nil {
		return nil, err
	}
	return joinTransferUsers(transfers), nil
}

// ListOutgoing returns transfers the user sent, latest first.
func (r *TransferRepository) ListOutgoing(userID int64, page, limit int) ([]model.Transfer, error) {
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	if page < 1 {
		page = 1
	}
	offset := (page - 1) * limit

	rows, err := database.DB.Query(
		`SELECT id, sender_id, recipient_id, amount, status, note, created_at, decided_at, refunded_at
		 FROM transfers WHERE sender_id = $1
		 ORDER BY id DESC LIMIT $2 OFFSET $3`,
		userID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list outgoing transfers: %w", err)
	}
	defer rows.Close()

	transfers, err := scanTransfers(rows)
	if err != nil {
		return nil, err
	}
	return joinTransferUsers(transfers), nil
}

// PendingForRecipient returns whether the user has at least one unanswered
// pending transfer.
func (r *TransferRepository) PendingForRecipient(userID int64) (bool, error) {
	var exists bool
	if err := database.DB.QueryRow(
		`SELECT EXISTS (SELECT 1 FROM transfers WHERE recipient_id = $1 AND status = 'pending')`,
		userID,
	).Scan(&exists); err != nil {
		return false, fmt.Errorf("failed to check pending transfers: %w", err)
	}
	return exists, nil
}

func lockTransferTx(tx interface {
	QueryRow(query string, args ...interface{}) *sql.Row
}, transferID int64) (*model.Transfer, error) {
	t := &model.Transfer{}
	err := tx.QueryRow(
		`SELECT id, sender_id, recipient_id, amount, status, note, created_at, decided_at, refunded_at
		 FROM transfers WHERE id = $1 FOR UPDATE`,
		transferID,
	).Scan(&t.ID, &t.SenderID, &t.RecipientID, &t.Amount, &t.Status, &t.Note, &t.CreatedAt, &t.DecidedAt, &t.RefundedAt)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to lock transfer: %w", err)
	}
	return t, nil
}

func scanTransfers(rows *sql.Rows) ([]model.Transfer, error) {
	transfers := make([]model.Transfer, 0)
	for rows.Next() {
		var t model.Transfer
		if err := rows.Scan(&t.ID, &t.SenderID, &t.RecipientID, &t.Amount, &t.Status, &t.Note, &t.CreatedAt, &t.DecidedAt, &t.RefundedAt); err != nil {
			return nil, fmt.Errorf("failed to scan transfer: %w", err)
		}
		transfers = append(transfers, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return transfers, nil
}

// joinTransferUsers enriches transfers with the counterparty's display name and
// avatar. The counterparty is the sender for incoming transfers and the
// recipient for outgoing ones.
func joinTransferUsers(transfers []model.Transfer) []model.Transfer {
	need := make([]int64, 0, len(transfers))
	seen := map[int64]bool{}
	for _, t := range transfers {
		if t.SenderID != 0 && !seen[t.SenderID] {
			seen[t.SenderID] = true
			need = append(need, t.SenderID)
		}
		if t.RecipientID != 0 && !seen[t.RecipientID] {
			seen[t.RecipientID] = true
			need = append(need, t.RecipientID)
		}
	}
	users, err := NewUserRepository().GetUsersByIDs(need)
	if err != nil {
		return transfers
	}
	for i := range transfers {
		if s, ok := users[transfers[i].SenderID]; ok {
			transfers[i].SenderName = displayName(s)
			transfers[i].SenderAvatar = s.AvatarURL
		}
		if r, ok := users[transfers[i].RecipientID]; ok {
			transfers[i].RecipientName = displayName(r)
			transfers[i].RecipientAvatar = r.AvatarURL
		}
	}
	return transfers
}

func displayName(u *model.User) string {
	if u.Nickname != "" {
		return u.Nickname
	}
	return u.Username
}
