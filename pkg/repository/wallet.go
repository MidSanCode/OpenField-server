package repository

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/openfield/server/pkg/database"
	"github.com/openfield/server/pkg/model"
)

// ErrInsufficientBalance reports an adjustment that would drive a balance below
// zero. Zero balance is always allowed; negative balances are rejected.
var ErrInsufficientBalance = errors.New("insufficient balance")

// adjustBalanceTx applies a signed balance delta within an existing transaction,
// recording a wallet transaction row. It is shared by AdjustBalance and other
// flows (sign-in make-up, transfers) that need atomic wallet writes.
type txRunner interface {
	QueryRow(query string, args ...interface{}) *sql.Row
	Exec(query string, args ...interface{}) (sql.Result, error)
}

// adjustBalanceTx runs against the caller's transaction and uses the plain
// *sql.Tx-compatible interface.
func adjustBalanceTx(tx txRunner, userID, amount, operatorID int64, txType, description string) error {
	if _, err := tx.Exec(
		`INSERT INTO wallets (user_id, balance) VALUES ($1, 0) ON CONFLICT (user_id) DO NOTHING`,
		userID,
	); err != nil {
		return fmt.Errorf("failed to ensure wallet: %w", err)
	}

	var balance int64
	err := tx.QueryRow(
		"SELECT balance FROM wallets WHERE user_id = $1 FOR UPDATE",
		userID,
	).Scan(&balance)
	if err != nil {
		return fmt.Errorf("failed to lock wallet: %w", err)
	}
	newBalance := balance + amount
	if newBalance < 0 {
		return ErrInsufficientBalance
	}
	if _, err := tx.Exec(
		"UPDATE wallets SET balance = $2, updated_at = NOW() WHERE user_id = $1",
		userID, newBalance,
	); err != nil {
		return fmt.Errorf("failed to update balance: %w", err)
	}
	if _, err := tx.Exec(
		`INSERT INTO wallet_transactions (user_id, amount, balance_after, type, description, operator_id)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		userID, amount, newBalance, txType, description, operatorID,
	); err != nil {
		return fmt.Errorf("failed to insert wallet transaction: %w", err)
	}
	return nil
}

// WalletRepository handles user wallet balances and transactions.
type WalletRepository struct{}

// NewWalletRepository creates a new WalletRepository.
func NewWalletRepository() *WalletRepository {
	return &WalletRepository{}
}

// EnsureWallet creates a wallet row for the user if it does not exist yet.
func (r *WalletRepository) EnsureWallet(userID int64) error {
	_, err := database.DB.Exec(
		`INSERT INTO wallets (user_id, balance) VALUES ($1, 0) ON CONFLICT (user_id) DO NOTHING`,
		userID,
	)
	if err != nil {
		return fmt.Errorf("failed to ensure wallet: %w", err)
	}
	return nil
}

// GetBalance returns the user's current wallet balance.
func (r *WalletRepository) GetBalance(userID int64) (int64, error) {
	if err := r.EnsureWallet(userID); err != nil {
		return 0, err
	}
	var balance int64
	err := database.DB.QueryRow(
		"SELECT balance FROM wallets WHERE user_id = $1", userID,
	).Scan(&balance)
	if err != nil {
		return 0, fmt.Errorf("failed to get wallet balance: %w", err)
	}
	return balance, nil
}

// GetWallet returns the user's wallet row.
func (r *WalletRepository) GetWallet(userID int64) (*model.Wallet, error) {
	if err := r.EnsureWallet(userID); err != nil {
		return nil, err
	}
	wallet := &model.Wallet{}
	err := database.DB.QueryRow(
		"SELECT user_id, balance, created_at, updated_at FROM wallets WHERE user_id = $1",
		userID,
	).Scan(&wallet.UserID, &wallet.Balance, &wallet.CreatedAt, &wallet.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to get wallet: %w", err)
	}
	return wallet, nil
}

// AdjustBalance applies a signed delta to a user's balance, recording a
// transaction. Negative deltas are rejected when they would drive the balance
// below zero. The write is atomic: balance update and transaction insert happen
// in a single database transaction.
func (r *WalletRepository) AdjustBalance(userID, amount, operatorID int64, txType, description string) (*model.Wallet, *model.WalletTransaction, error) {
	if err := r.EnsureWallet(userID); err != nil {
		return nil, nil, err
	}

	tx, err := database.DB.Begin()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	var balance int64
	err = tx.QueryRow(
		"SELECT balance FROM wallets WHERE user_id = $1 FOR UPDATE",
		userID,
	).Scan(&balance)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to lock wallet: %w", err)
	}

	newBalance := balance + amount
	if newBalance < 0 {
		return nil, nil, ErrInsufficientBalance
	}

	if _, err := tx.Exec(
		"UPDATE wallets SET balance = $2, updated_at = NOW() WHERE user_id = $1",
		userID, newBalance,
	); err != nil {
		return nil, nil, fmt.Errorf("failed to update balance: %w", err)
	}

	var txnID int64
	err = tx.QueryRow(
		`INSERT INTO wallet_transactions (user_id, amount, balance_after, type, description, operator_id)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		userID, amount, newBalance, txType, description, operatorID,
	).Scan(&txnID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to insert wallet transaction: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	wallet := &model.Wallet{UserID: userID, Balance: model.NewCents(newBalance)}
	txn := &model.WalletTransaction{
		ID:           txnID,
		UserID:       userID,
		Amount:       model.NewCents(amount),
		BalanceAfter: model.NewCents(newBalance),
		Type:         txType,
		Description:  description,
		OperatorID:   operatorID,
	}
	return wallet, txn, nil
}

// ListTransactions returns a user's wallet transactions, newest first.
func (r *WalletRepository) ListTransactions(userID int64, page, limit int) ([]model.WalletTransaction, error) {
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
		`SELECT id, user_id, amount, balance_after, type, description, operator_id, created_at
		 FROM wallet_transactions
		 WHERE user_id = $1
		 ORDER BY id DESC
		 LIMIT $2 OFFSET $3`,
		userID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list wallet transactions: %w", err)
	}
	defer rows.Close()

	return scanWalletTransactions(rows)
}

func scanWalletTransactions(rows *sql.Rows) ([]model.WalletTransaction, error) {
	txns := make([]model.WalletTransaction, 0)
	for rows.Next() {
		var txn model.WalletTransaction
		var operatorID sql.NullInt64
		if err := rows.Scan(&txn.ID, &txn.UserID, &txn.Amount, &txn.BalanceAfter,
			&txn.Type, &txn.Description, &operatorID, &txn.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan wallet transaction: %w", err)
		}
		if operatorID.Valid {
			txn.OperatorID = operatorID.Int64
		}
		txns = append(txns, txn)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return txns, nil
}
