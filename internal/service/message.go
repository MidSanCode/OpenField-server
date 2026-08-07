package service

import (
	"errors"
	"fmt"

	"github.com/openfield/server/internal/model"
	"github.com/openfield/server/internal/repository"
)

// MessageService handles business logic for messages.
type MessageService struct {
	msgRepo *repository.MessageRepository
}

// NewMessageService creates a new MessageService.
func NewMessageService() *MessageService {
	return &MessageService{
		msgRepo: repository.NewMessageRepository(),
	}
}

// SendMessage sends a message from one user to another.
func (s *MessageService) SendMessage(senderID, receiverID int64, content string) (*model.Message, error) {
	if content == "" {
		return nil, errors.New("message content cannot be empty")
	}
	if len(content) > 2000 {
		return nil, errors.New("message exceeds maximum length of 2000")
	}

	msg, err := s.msgRepo.Create(senderID, receiverID, content)
	if err != nil {
		return nil, fmt.Errorf("failed to send message: %w", err)
	}

	return msg, nil
}

// GetConversation retrieves the conversation history between two users.
func (s *MessageService) GetConversation(userA, userB int64, limit int) ([]model.Message, error) {
	messages, err := s.msgRepo.GetConversation(userA, userB, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get conversation: %w", err)
	}

	return messages, nil
}
