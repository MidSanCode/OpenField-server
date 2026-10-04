package repository

import (
	"fmt"

	"github.com/openfield/server/pkg/database"
)

// Membership caps.
//
// Group chats and camps had no member ceiling anywhere in the codebase: the
// join and invite paths wrote rows with INSERT ... ON CONFLICT DO NOTHING and
// never counted. A single group could therefore grow without bound, bloating
// conversation_members / camp_members and slowing every member count, member
// list and join used by other services on the same database. Creation quotas
// already existed (GroupCreateQuotaFor / CampCreateQuotaFor); this is the
// matching limit for growth.
const (
	// MaxGroupMembers is the ceiling for a group conversation.
	MaxGroupMembers = 2000
	// MaxCampMembers is the ceiling for a camp (board/forum).
	MaxCampMembers = 50000
)

// addMemberLimited inserts a conversation member only while the group is under
// its cap, doing the count and the insert in one transaction under a per-group
// lock. Returns ErrMemberLimitReached when the group is full.
func (r *ConversationRepository) addMemberLimited(conversationID, userID, addedBy int64, role, status string) error {
	tx, err := database.DB.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin add-member transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Serializes joins to this one conversation; other groups are unaffected.
	if _, err := tx.Exec("SELECT pg_advisory_xact_lock($1)", conversationID); err != nil {
		return fmt.Errorf("failed to lock conversation: %w", err)
	}

	var count int
	if err := tx.QueryRow(
		"SELECT COUNT(*) FROM conversation_members WHERE conversation_id = $1",
		conversationID,
	).Scan(&count); err != nil {
		return fmt.Errorf("failed to count members: %w", err)
	}

	// Re-adding an existing member (or re-activating one) does not grow the
	// group, so it is allowed even at the cap.
	var exists bool
	if err := tx.QueryRow(
		"SELECT EXISTS (SELECT 1 FROM conversation_members WHERE conversation_id = $1 AND user_id = $2)",
		conversationID, userID,
	).Scan(&exists); err != nil {
		return fmt.Errorf("failed to check existing member: %w", err)
	}
	if !exists && count >= MaxGroupMembers {
		return ErrMemberLimitReached
	}

	if _, err := tx.Exec(
		`INSERT INTO conversation_members (conversation_id, user_id, role, status, added_by)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (conversation_id, user_id) DO UPDATE SET status = $4, added_by = $5`,
		conversationID, userID, role, status, addedBy,
	); err != nil {
		return fmt.Errorf("failed to add member: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit add-member: %w", err)
	}
	return nil
}

// joinCampLimited adds a camp member only while the camp is under its cap,
// with the same transactional count-then-insert as the group path. Returns
// ErrMemberLimitReached when the camp is full.
func (r *CampRepository) joinCampLimited(campID, userID int64) error {
	tx, err := database.DB.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin join transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec("SELECT pg_advisory_xact_lock($1)", campID); err != nil {
		return fmt.Errorf("failed to lock camp: %w", err)
	}

	var exists bool
	if err := tx.QueryRow(
		"SELECT EXISTS (SELECT 1 FROM camp_members WHERE camp_id = $1 AND user_id = $2)",
		campID, userID,
	).Scan(&exists); err != nil {
		return fmt.Errorf("failed to check existing member: %w", err)
	}
	if !exists {
		var count int
		if err := tx.QueryRow(
			"SELECT COUNT(*) FROM camp_members WHERE camp_id = $1", campID,
		).Scan(&count); err != nil {
			return fmt.Errorf("failed to count members: %w", err)
		}
		if count >= MaxCampMembers {
			return ErrMemberLimitReached
		}
	}

	if _, err := tx.Exec(
		"INSERT INTO camp_members (camp_id, user_id, role) VALUES ($1, $2, 'member') ON CONFLICT (camp_id, user_id) DO NOTHING",
		campID, userID,
	); err != nil {
		return fmt.Errorf("failed to join camp: %w", err)
	}
	if _, err := tx.Exec("UPDATE camps SET updated_at = NOW() WHERE id = $1", campID); err != nil {
		return fmt.Errorf("failed to touch camp: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit join: %w", err)
	}
	return nil
}
