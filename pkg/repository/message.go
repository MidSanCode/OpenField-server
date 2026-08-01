package repository

import (
	"database/sql"
	"fmt"

	"github.com/openfield/server/pkg/database"
	"github.com/openfield/server/pkg/model"
)

// MessageRepository handles chat message database operations.
type MessageRepository struct{}

// NewMessageRepository creates a new MessageRepository.
func NewMessageRepository() *MessageRepository {
	return &MessageRepository{}
}

// Create inserts a new message and returns it with sender info.
func (r *MessageRepository) Create(conversationID, senderID int64, content string, replyToID *int64) (*model.Message, error) {
	msg := &model.Message{}
	err := database.DB.QueryRow(
		`INSERT INTO messages (conversation_id, sender_id, content, reply_to_id)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id, conversation_id, sender_id, content, reply_to_id, edited_at, deleted_at, created_at`,
		conversationID, senderID, content, replyToID,
	).Scan(&msg.ID, &msg.ConversationID, &msg.SenderID, &msg.Content, &msg.ReplyToID, &msg.EditedAt, &msg.DeletedAt, &msg.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to create message: %w", err)
	}

	if _, err := database.DB.Exec(
		"UPDATE conversations SET updated_at = NOW() WHERE id = $1",
		conversationID,
	); err != nil {
		return nil, fmt.Errorf("failed to touch conversation: %w", err)
	}

	msg, err = r.getWithSender(msg.ID)
	if err != nil {
		return nil, err
	}
	return msg, nil
}

// ListByConversation retrieves messages for a conversation (newest last).
func (r *MessageRepository) ListByConversation(conversationID int64, beforeID int64, limit int) ([]model.Message, error) {
	if limit < 1 || limit > 100 {
		limit = 50
	}
	query := `SELECT m.id, m.conversation_id, m.sender_id, m.content, m.reply_to_id, m.edited_at, m.deleted_at, m.created_at,
	                 u.username, u.nickname, u.avatar_url
	          FROM messages m
	          JOIN users u ON m.sender_id = u.id
	          WHERE m.conversation_id = $1 AND m.deleted_at IS NULL`
	args := []interface{}{conversationID}
	if beforeID > 0 {
		query += " AND m.id < $2"
		args = append(args, beforeID)
	}
	query += " ORDER BY m.created_at DESC, m.id DESC LIMIT " + fmt.Sprintf("%d", limit)

	rows, err := database.DB.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list messages: %w", err)
	}
	defer rows.Close()

	msgs := make([]model.Message, 0)
	for rows.Next() {
		var m model.Message
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.SenderID, &m.Content, &m.ReplyToID, &m.EditedAt, &m.DeletedAt, &m.CreatedAt, &m.SenderName, &m.SenderAvatar); err != nil {
			return nil, fmt.Errorf("failed to scan message: %w", err)
		}
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}

	// reverse to chronological order
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, nil
}

func (r *MessageRepository) getWithSender(id int64) (*model.Message, error) {
	msg := &model.Message{}
	err := database.DB.QueryRow(
		`SELECT m.id, m.conversation_id, m.sender_id, m.content, m.reply_to_id, m.edited_at, m.deleted_at, m.created_at,
		        u.username, u.nickname, u.avatar_url
		 FROM messages m
		 JOIN users u ON m.sender_id = u.id
		 WHERE m.id = $1`,
		id,
	).Scan(&msg.ID, &msg.ConversationID, &msg.SenderID, &msg.Content, &msg.ReplyToID, &msg.EditedAt, &msg.DeletedAt, &msg.CreatedAt, &msg.SenderName, &msg.SenderAvatar)
	if err != nil {
		return nil, fmt.Errorf("failed to get message: %w", err)
	}
	return msg, nil
}

// GetByID retrieves a message by ID (including soft-deleted ones, to allow owner checks).
func (r *MessageRepository) GetByID(id int64) (*model.Message, error) {
	msg := &model.Message{}
	err := database.DB.QueryRow(
		`SELECT id, conversation_id, sender_id, content, reply_to_id, edited_at, deleted_at, created_at
		 FROM messages WHERE id = $1`,
		id,
	).Scan(&msg.ID, &msg.ConversationID, &msg.SenderID, &msg.Content, &msg.ReplyToID, &msg.EditedAt, &msg.DeletedAt, &msg.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return msg, nil
}

// Update edits a message's content (owner only) and stamps edited_at.
func (r *MessageRepository) Update(id, userID int64, content string) (*model.Message, error) {
	msg, err := r.GetByID(id)
	if err != nil {
		return nil, err
	}
	if msg == nil || msg.SenderID != userID {
		return nil, sql.ErrNoRows
	}
	if msg.DeletedAt != nil {
		return nil, ErrDeletedMessage
	}

	result, err := database.DB.Exec(
		"UPDATE messages SET content = $3, edited_at = NOW() WHERE id = $1 AND sender_id = $2 AND deleted_at IS NULL",
		id, userID, content,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to update message: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return nil, sql.ErrNoRows
	}
	return r.getWithSender(id)
}

// Delete soft-deletes a message (owner only).
func (r *MessageRepository) Delete(id, userID int64) error {
	msg, err := r.GetByID(id)
	if err != nil {
		return err
	}
	if msg == nil || msg.SenderID != userID {
		return sql.ErrNoRows
	}
	if msg.DeletedAt != nil {
		return ErrDeletedMessage
	}

	result, err := database.DB.Exec(
		"UPDATE messages SET deleted_at = NOW() WHERE id = $1 AND sender_id = $2 AND deleted_at IS NULL",
		id, userID,
	)
	if err != nil {
		return fmt.Errorf("failed to delete message: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}
