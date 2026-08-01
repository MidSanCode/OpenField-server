package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/internal/logger"
	"github.com/openfield/server/internal/middleware"
	"github.com/openfield/server/internal/service"
)

// MessageHandler handles message-related requests.
type MessageHandler struct {
	messageService *service.MessageService
}

// NewMessageHandler creates a new MessageHandler.
func NewMessageHandler() *MessageHandler {
	return &MessageHandler{
		messageService: service.NewMessageService(),
	}
}

// SendMessage sends a message to another user.
func (h *MessageHandler) SendMessage(c *gin.Context) {
	senderID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req struct {
		ReceiverID int64  `json:"receiver_id" binding:"required"`
		Content    string `json:"content" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	msg, err := h.messageService.SendMessage(senderID, req.ReceiverID, req.Content)
	if err != nil {
		logger.Log.Error("failed to send message", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to send message"})
		return
	}

	c.JSON(http.StatusCreated, msg)
}

// GetConversation retrieves the conversation history between two users.
func (h *MessageHandler) GetConversation(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	idStr := c.Param("user_id")
	otherUserID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user ID"})
		return
	}

	limitStr := c.DefaultQuery("limit", "50")
	limit, _ := strconv.Atoi(limitStr)

	messages, err := h.messageService.GetConversation(userID, otherUserID, limit)
	if err != nil {
		logger.Log.Error("failed to get conversation", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get conversation"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"messages": messages})
}
