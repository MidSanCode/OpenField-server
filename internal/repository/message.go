package repository

import (
	"fmt"

	"github.com/openfield/server/internal/database"
	"github.com/openfield/server/internal/model"
)

// MessageRepository handles message-related database operations.
type MessageRepository struct{}

// NewMessageRepository creates a new MessageRepository.
func NewMessageRepository() *MessageRepository {
	return &MessageRepository{}
}

// Create creates a new message.
func (r *MessageRepository) Create(senderID, receiverID int64, content string) (*model.Message, error) {
	msg := &model.Message{}
	err := database.DB.QueryRow(
		"INSERT INTO messages (sender_id, receiver_id, content) VALUES ($1, $2, $3) RETURNING id, sender_id, receiver_id, content, created_at",
		senderID, receiverID, content,
	).Scan(&msg.ID, &msg.SenderID, &msg.ReceiverID, &msg.Content, &msg.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to create message: %w", err)
	}
	return msg, nil
}

// GetConversation retrieves messages between two users.
func (r *MessageRepository) GetConversation(userA, userB int64, limit int) ([]model.Message, error) {
	if limit < 1 {
		limit = 50
	}

	rows, err := database.DB.Query(
		`SELECT id, sender_id, receiver_id, content, created_at
		 FROM messages
		 WHERE (sender_id = $1 AND receiver_id = $2) OR (sender_id = $2 AND receiver_id = $1)
		 ORDER BY created_at DESC
		 LIMIT $3`,
		userA, userB, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get conversation: %w", err)
	}
	defer rows.Close()

	messages := make([]model.Message, 0)
	for rows.Next() {
		var msg model.Message
		if err := rows.Scan(&msg.ID, &msg.SenderID, &msg.ReceiverID, &msg.Content, &msg.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan message: %w", err)
		}
		messages = append(messages, msg)
	}

	return messages, nil
}
