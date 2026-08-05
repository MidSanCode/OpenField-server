package repository

import (
	"database/sql"
	"fmt"

	"github.com/openfield/server/pkg/database"
	"github.com/openfield/server/pkg/model"
)

// ConsentRequestRepository handles consent request database operations.
type ConsentRequestRepository struct {
	convRepo *ConversationRepository
}

// NewConsentRequestRepository creates a new ConsentRequestRepository.
func NewConsentRequestRepository() *ConsentRequestRepository {
	return &ConsentRequestRepository{
		convRepo: NewConversationRepository(),
	}
}

// Create inserts a pending consent request.
func (r *ConsentRequestRepository) Create(reqType string, requesterID, targetUserID int64, conversationID *int64, message string) (*model.ConsentRequest, error) {
	req := &model.ConsentRequest{}
	err := database.DB.QueryRow(
		`INSERT INTO consent_requests (type, requester_id, target_user_id, conversation_id, message)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING id, type, requester_id, target_user_id, conversation_id, message, status, created_at, responded_at`,
		reqType, requesterID, targetUserID, conversationID, message,
	).Scan(&req.ID, &req.Type, &req.RequesterID, &req.TargetUserID, &req.ConversationID, &req.Message, &req.Status, &req.CreatedAt, &req.RespondedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to create consent request: %w", err)
	}
	return req, nil
}

// HasPendingBetween reports whether a pending private chat request already exists between two users.
func (r *ConsentRequestRepository) HasPendingBetween(userA, userB int64) (bool, error) {
	var count int
	err := database.DB.QueryRow(
		`SELECT COUNT(*) FROM consent_requests
		 WHERE status = 'pending'
		   AND type = 'private_chat'
		   AND ((requester_id = $1 AND target_user_id = $2) OR (requester_id = $2 AND target_user_id = $1))`,
		userA, userB,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("failed to check pending request: %w", err)
	}
	return count > 0, nil
}

// GetByID retrieves a consent request by ID.
func (r *ConsentRequestRepository) GetByID(id int64) (*model.ConsentRequest, error) {
	req := &model.ConsentRequest{}
	err := database.DB.QueryRow(
		`SELECT cr.id, cr.type, cr.requester_id, cr.target_user_id, cr.conversation_id, cr.message, cr.status, cr.created_at, cr.responded_at,
		        ru.username, ru.avatar_url,
		        COALESCE(c.title, '')
		 FROM consent_requests cr
		 JOIN users ru ON cr.requester_id = ru.id
		 LEFT JOIN conversations c ON c.id = cr.conversation_id
		 WHERE cr.id = $1`,
		id,
	).Scan(&req.ID, &req.Type, &req.RequesterID, &req.TargetUserID, &req.ConversationID, &req.Message, &req.Status, &req.CreatedAt, &req.RespondedAt, &req.RequesterName, &req.RequesterAvatar, &req.GroupTitle)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return req, nil
}

// ListPendingForUser lists pending requests targeting a user.
func (r *ConsentRequestRepository) ListPendingForUser(userID int64) ([]model.ConsentRequest, error) {
	rows, err := database.DB.Query(
		`SELECT cr.id, cr.type, cr.requester_id, cr.target_user_id, cr.conversation_id, cr.message, cr.status, cr.created_at, cr.responded_at,
		        ru.username, ru.avatar_url,
		        COALESCE(c.title, '')
		 FROM consent_requests cr
		 JOIN users ru ON cr.requester_id = ru.id
		 LEFT JOIN conversations c ON c.id = cr.conversation_id
		 WHERE cr.target_user_id = $1 AND cr.status = 'pending'
		 ORDER BY cr.created_at DESC`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list consent requests: %w", err)
	}
	defer rows.Close()

	reqs := make([]model.ConsentRequest, 0)
	for rows.Next() {
		var req model.ConsentRequest
		if err := rows.Scan(&req.ID, &req.Type, &req.RequesterID, &req.TargetUserID, &req.ConversationID, &req.Message, &req.Status, &req.CreatedAt, &req.RespondedAt, &req.RequesterName, &req.RequesterAvatar, &req.GroupTitle); err != nil {
			return nil, fmt.Errorf("failed to scan consent request: %w", err)
		}
		reqs = append(reqs, req)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return reqs, nil
}

// Accept accepts a consent request:
// - private_chat: creates the private conversation and activates both members.
// - group_invite: activates the invited member in the existing conversation.
func (r *ConsentRequestRepository) Accept(reqID int64) (*model.Conversation, error) {
	req, err := r.GetByID(reqID)
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, sql.ErrNoRows
	}
	if req.Status != "pending" {
		return nil, ErrAlreadyHandled
	}

	if req.Type == "private_chat" {
		conv, err := r.convRepo.CreatePrivate(req.RequesterID, req.TargetUserID)
		if err != nil {
			return nil, err
		}
		_, err = database.DB.Exec(
			"UPDATE consent_requests SET status = 'accepted', responded_at = NOW() WHERE id = $1",
			reqID,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to update request: %w", err)
		}
		return conv, nil
	}

	if req.ConversationID == nil {
		return nil, ErrNotFound
	}
	// group_invite
	if err := r.convRepo.AddMember(*req.ConversationID, req.TargetUserID, req.RequesterID, "member", "active"); err != nil {
		return nil, err
	}
	_, err = database.DB.Exec(
		"UPDATE consent_requests SET status = 'accepted', responded_at = NOW() WHERE id = $1",
		reqID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to update request: %w", err)
	}
	return r.convRepo.GetByID(*req.ConversationID)
}

// Decline rejects a consent request.
func (r *ConsentRequestRepository) Decline(reqID int64) error {
	result, err := database.DB.Exec(
		"UPDATE consent_requests SET status = 'declined', responded_at = NOW() WHERE id = $1 AND status = 'pending'",
		reqID,
	)
	if err != nil {
		return fmt.Errorf("failed to decline request: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return ErrAlreadyHandled
	}
	return nil
}
