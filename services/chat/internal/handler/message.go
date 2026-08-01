package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/logger"
	"github.com/openfield/server/pkg/middleware"
	"github.com/openfield/server/pkg/repository"
)

// MessageHandler handles chat message endpoints.
type MessageHandler struct {
	msgRepo  *repository.MessageRepository
	convRepo *repository.ConversationRepository
}

// NewMessageHandler creates a new MessageHandler.
func NewMessageHandler() *MessageHandler {
	return &MessageHandler{
		msgRepo:  repository.NewMessageRepository(),
		convRepo: repository.NewConversationRepository(),
	}
}

// List lists messages in a conversation (oldest first, newest last).
func (h *MessageHandler) List(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	convID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid conversation ID"})
		return
	}

	isMember, err := h.convRepo.IsMember(convID, userID)
	if err != nil {
		logger.Log.Error("failed to check membership", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list messages"})
		return
	}
	if !isMember {
		c.JSON(http.StatusForbidden, gin.H{"error": "you are not a member of this conversation"})
		return
	}

	beforeID, _ := strconv.ParseInt(c.DefaultQuery("before", "0"), 10, 64)
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))

	msgs, err := h.msgRepo.ListByConversation(convID, beforeID, limit)
	if err != nil {
		logger.Log.Error("failed to list messages", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list messages"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"messages": msgs})
}

// Send sends a message to a conversation.
func (h *MessageHandler) Send(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	convID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid conversation ID"})
		return
	}

	var req struct {
		Content   string `json:"content" binding:"required"`
		ReplyToID *int64 `json:"reply_to_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if req.Content == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "message cannot be empty"})
		return
	}
	if len(req.Content) > 5000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "message too long (max 5000)"})
		return
	}

	isMember, err := h.convRepo.IsMember(convID, userID)
	if err != nil {
		logger.Log.Error("failed to check membership", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to send message"})
		return
	}
	if !isMember {
		c.JSON(http.StatusForbidden, gin.H{"error": "you are not a member of this conversation"})
		return
	}

	msg, err := h.msgRepo.Create(convID, userID, req.Content, req.ReplyToID)
	if err != nil {
		logger.Log.Error("failed to send message", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to send message"})
		return
	}

	c.JSON(http.StatusCreated, msg)
}

// Update edits a message's content (owner only).
func (h *MessageHandler) Update(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	msgID, err := strconv.ParseInt(c.Param("message_id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid message ID"})
		return
	}

	var req struct {
		Content string `json:"content" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if req.Content == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "message cannot be empty"})
		return
	}
	if len(req.Content) > 5000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "message too long (max 5000)"})
		return
	}

	msg, err := h.msgRepo.Update(msgID, userID, req.Content)
	if err != nil {
		if errors.Is(err, repository.ErrDeletedMessage) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "message already deleted"})
			return
		}
		if errors.Is(err, repository.ErrNoSuchRow) {
			c.JSON(http.StatusForbidden, gin.H{"error": "message not found or you cannot edit it"})
			return
		}
		logger.Log.Error("failed to update message", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update message"})
		return
	}

	c.JSON(http.StatusOK, msg)
}

// Delete soft-deletes a message (owner only).
func (h *MessageHandler) Delete(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	msgID, err := strconv.ParseInt(c.Param("message_id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid message ID"})
		return
	}

	err = h.msgRepo.Delete(msgID, userID)
	if err != nil {
		if errors.Is(err, repository.ErrDeletedMessage) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "message already deleted"})
			return
		}
		if errors.Is(err, repository.ErrNoSuchRow) {
			c.JSON(http.StatusForbidden, gin.H{"error": "message not found or you cannot delete it"})
			return
		}
		logger.Log.Error("failed to delete message", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete message"})
		return
	}

	c.Status(http.StatusNoContent)
}
