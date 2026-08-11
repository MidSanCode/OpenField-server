package repository

import (
	"database/sql"
	"fmt"

	"github.com/openfield/server/pkg/database"
	"github.com/openfield/server/pkg/model"
)

// ExpRepository handles experience history records.
type ExpRepository struct{}

// NewExpRepository creates a new ExpRepository.
func NewExpRepository() *ExpRepository {
	return &ExpRepository{}
}

// Add records a single experience award.
func (r *ExpRepository) Add(userID, amount int64, reason model.ExpReason, description string) error {
	if _, err := database.DB.Exec(
		`INSERT INTO exp_history (user_id, amount, reason, description) VALUES ($1, $2, $3, $4)`,
		userID, amount, string(reason), description,
	); err != nil {
		return fmt.Errorf("failed to record exp history: %w", err)
	}
	return nil
}

// List returns the most recent exp history for a user, newest first.
func (r *ExpRepository) List(userID int64, limit int) ([]model.ExpEntry, error) {
	if limit < 1 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := database.DB.Query(
		`SELECT id, user_id, amount, reason, description, created_at
		 FROM exp_history WHERE user_id = $1 ORDER BY id DESC LIMIT $2`,
		userID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query exp history: %w", err)
	}
	defer rows.Close()

	entries := make([]model.ExpEntry, 0)
	for rows.Next() {
		var e model.ExpEntry
		if err := rows.Scan(&e.ID, &e.UserID, &e.Amount, &e.Reason, &e.Description, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan exp entry: %w", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return entries, nil
}

// CountOfReason returns the number of recorded awards of a reason. Used to
// detect whether a one-time event (e.g. first post) already happened when only
// the exp award is recorded.
func (r *ExpRepository) CountOfReason(userID int64, reason model.ExpReason) (int, error) {
	var n int
	err := database.DB.QueryRow(
		`SELECT COUNT(*) FROM exp_history WHERE user_id = $1 AND reason = $2`,
		userID, string(reason),
	).Scan(&n)
	if err != nil && err != sql.ErrNoRows {
		return 0, fmt.Errorf("failed to count exp history: %w", err)
	}
	return n, nil
}
