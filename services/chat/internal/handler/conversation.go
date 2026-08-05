package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/events"
	"github.com/openfield/server/pkg/logger"
	"github.com/openfield/server/pkg/middleware"
	"github.com/openfield/server/pkg/repository"
)

// ConversationHandler handles conversation-related requests.
type ConversationHandler struct {
	convRepo    *repository.ConversationRepository
	consentRepo *repository.ConsentRequestRepository
}

// NewConversationHandler creates a new ConversationHandler.
func NewConversationHandler() *ConversationHandler {
	return &ConversationHandler{
		convRepo:    repository.NewConversationRepository(),
		consentRepo: repository.NewConsentRequestRepository(),
	}
}

// List lists the current user's conversations.
func (h *ConversationHandler) List(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	convs, err := h.convRepo.ListForUser(userID)
	if err != nil {
		logger.Log.Error("failed to list conversations", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list conversations"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"conversations": convs})
}

// Get retrieves a single conversation with its members.
func (h *ConversationHandler) Get(c *gin.Context) {
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get conversation"})
		return
	}
	if !isMember {
		c.JSON(http.StatusForbidden, gin.H{"error": "you are not a member of this conversation"})
		return
	}

	conv, err := h.convRepo.GetByID(convID)
	if err != nil {
		logger.Log.Error("failed to get conversation", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get conversation"})
		return
	}
	if conv == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "conversation not found"})
		return
	}

	members, err := h.convRepo.ListMembers(convID)
	if err != nil {
		logger.Log.Error("failed to list members", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get members"})
		return
	}

	myMember, err := h.convRepo.GetMember(convID, userID)
	if err != nil {
		logger.Log.Error("failed to get my membership", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get membership"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"conversation": conv,
		"members":      members,
		"my_membership": myMember,
	})
}

// CreateGroup creates a new group conversation.
func (h *ConversationHandler) CreateGroup(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req struct {
		Title string `json:"title" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if req.Title == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "group title cannot be empty"})
		return
	}
	if len(req.Title) > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "group title too long (max 100)"})
		return
	}

	conv, err := h.convRepo.CreateGroup(userID, req.Title)
	if err != nil {
		logger.Log.Error("failed to create group", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create group"})
		return
	}

	c.JSON(http.StatusCreated, conv)
}

// InviteToGroup sends a group invite consent request to another user.
func (h *ConversationHandler) InviteToGroup(c *gin.Context) {
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
		UserID  int64  `json:"user_id" binding:"required"`
		Message string `json:"message"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if req.UserID == userID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot invite yourself"})
		return
	}

	conv, err := h.convRepo.GetByID(convID)
	if err != nil {
		logger.Log.Error("failed to get conversation", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to invite"})
		return
	}
	if conv == nil || conv.Type != "group" {
		c.JSON(http.StatusNotFound, gin.H{"error": "group not found"})
		return
	}

	isMember, err := h.convRepo.IsMember(convID, userID)
	if err != nil || !isMember {
		c.JSON(http.StatusForbidden, gin.H{"error": "you are not a member of this group"})
		return
	}

	targetIsMember, err := h.convRepo.IsMember(convID, req.UserID)
	if err != nil {
		logger.Log.Error("failed to check membership", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to invite"})
		return
	}
	if targetIsMember {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user is already a member of this group"})
		return
	}

	_, err = h.consentRepo.Create("group_invite", userID, req.UserID, &convID, req.Message)
	if err != nil {
		logger.Log.Error("failed to create invite", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to send invite"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"message": "invite sent"})
}

// StartPrivateChat sends a private chat consent request to another user.
func (h *ConversationHandler) StartPrivateChat(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req struct {
		UserID  int64  `json:"user_id" binding:"required"`
		Message string `json:"message"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if req.UserID == userID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot start a chat with yourself"})
		return
	}

	hasPending, err := h.consentRepo.HasPendingBetween(userID, req.UserID)
	if err != nil {
		logger.Log.Error("failed to check pending request", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start chat"})
		return
	}
	if hasPending {
		c.JSON(http.StatusConflict, gin.H{"error": "a chat request is already pending"})
		return
	}

	_, err = h.consentRepo.Create("private_chat", userID, req.UserID, nil, req.Message)
	if err != nil {
		logger.Log.Error("failed to create chat request", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start chat"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"message": "chat request sent"})
}

// UpdateNote sets the user's note/remark about a private conversation's other participant.
func (h *ConversationHandler) UpdateNote(c *gin.Context) {
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
		Note string `json:"note"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	isMember, err := h.convRepo.IsMember(convID, userID)
	if err != nil || !isMember {
		c.JSON(http.StatusForbidden, gin.H{"error": "you are not a member of this conversation"})
		return
	}

	if err := h.convRepo.UpdateNote(convID, userID, req.Note); err != nil {
		logger.Log.Error("failed to update note", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update note"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "note updated"})
}

// UpdateGroupNickname sets the user's display nickname in a group.
func (h *ConversationHandler) UpdateGroupNickname(c *gin.Context) {
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
		Nickname string `json:"group_nickname"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	isMember, err := h.convRepo.IsMember(convID, userID)
	if err != nil || !isMember {
		c.JSON(http.StatusForbidden, gin.H{"error": "you are not a member of this conversation"})
		return
	}

	if err := h.convRepo.UpdateGroupNickname(convID, userID, req.Nickname); err != nil {
		logger.Log.Error("failed to update group nickname", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update group nickname"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "group nickname updated"})
}

// MarkRead marks the conversation as read up to a message ID.
func (h *ConversationHandler) MarkRead(c *gin.Context) {
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
		LastMessageID int64 `json:"last_message_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	isMember, err := h.convRepo.IsMember(convID, userID)
	if err != nil || !isMember {
		c.JSON(http.StatusForbidden, gin.H{"error": "you are not a member of this conversation"})
		return
	}

	if err := h.convRepo.UpdateLastReadMessage(convID, userID, req.LastMessageID); err != nil {
		logger.Log.Error("failed to mark read", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to mark read"})
		return
	}

	c.Status(http.StatusNoContent)
}

// Leave removes the current user from a group conversation.
func (h *ConversationHandler) Leave(c *gin.Context) {
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

	conv, err := h.convRepo.GetByID(convID)
	if err != nil {
		logger.Log.Error("failed to get conversation", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to leave"})
		return
	}
	if conv == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "conversation not found"})
		return
	}

	if conv.Type == "private" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot leave a private conversation"})
		return
	}

	member, err := h.convRepo.GetMember(convID, userID)
	if err != nil || member == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "you are not a member of this conversation"})
		return
	}
	if member.Role == "owner" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the owner cannot leave; transfer ownership or delete the group"})
		return
	}

	if err := h.convRepo.RemoveMember(convID, userID); err != nil {
		logger.Log.Error("failed to leave group", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to leave group"})
		return
	}

	c.Status(http.StatusNoContent)
}

// RemoveMember removes another member from a group (owner/admin only).
func (h *ConversationHandler) RemoveMember(c *gin.Context) {
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
	targetID, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user ID"})
		return
	}

	actor, err := h.convRepo.GetMember(convID, userID)
	if err != nil || actor == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "you are not a member of this conversation"})
		return
	}
	if actor.Role != "owner" && actor.Role != "admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "only the owner or admins can remove members"})
		return
	}

	if err := h.convRepo.RemoveMember(convID, targetID); err != nil {
		logger.Log.Error("failed to remove member", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to remove member"})
		return
	}

	c.Status(http.StatusNoContent)
}

// Typing broadcasts a "typing" indicator to the other members of the
// conversation. It is fire-and-forget; clients throttle their own requests.
func (h *ConversationHandler) Typing(c *gin.Context) {
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to publish typing indicator"})
		return
	}
	if !isMember {
		c.JSON(http.StatusForbidden, gin.H{"error": "you are not a member of this conversation"})
		return
	}

	members, err := h.convRepo.ListMembers(convID)
	if err != nil {
		logger.Log.Warn("failed to list conversation members for typing", "error", err)
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
		return
	}
	recipients := make([]int64, 0, len(members))
	for _, m := range members {
		if m.Status != "active" || m.UserID == userID {
			continue
		}
		recipients = append(recipients, m.UserID)
	}
	events.Publish(c.Request.Context(), events.ChatTyping, recipients, gin.H{
		"conversation_id": convID,
		"user_id":         userID,
	})

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
