package handler

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/events"
	"github.com/openfield/server/pkg/logger"
	"github.com/openfield/server/pkg/middleware"
	"github.com/openfield/server/pkg/model"
	"github.com/openfield/server/pkg/repository"
)

// ConversationHandler handles conversation-related requests.
type ConversationHandler struct {
	convRepo    *repository.ConversationRepository
	consentRepo *repository.ConsentRequestRepository
	msgRepo     *repository.MessageRepository
}

// NewConversationHandler creates a new ConversationHandler.
func NewConversationHandler() *ConversationHandler {
	return &ConversationHandler{
		convRepo:    repository.NewConversationRepository(),
		consentRepo: repository.NewConsentRequestRepository(),
		msgRepo:     repository.NewMessageRepository(),
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

	// Non-members may only view public groups (searchable & viewable).
	if !isMember {
		if conv.Type != "group" || !conv.IsPublic {
			c.JSON(http.StatusForbidden, gin.H{"error": "you are not a member of this conversation"})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"conversation":  conv,
			"members":       []model.ConversationMember{},
			"my_membership": nil,
		})
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
		"conversation":  conv,
		"members":       members,
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

	// Creation quota: only groups the user CREATED count (joined groups do
	// not); active membership adds 25% per level on top of the 1000 base.
	userRepo := repository.NewUserRepository()
	user, err := userRepo.GetByID(userID)
	if err != nil || user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	owned, err := repository.CountOwnedGroups(userID)
	if err != nil {
		logger.Log.Error("failed to count owned groups", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create group"})
		return
	}
	quota := repository.GroupCreateQuotaFor(user.MemberLevel, user.MemberExpiresAt)
	if owned >= quota {
		c.JSON(http.StatusForbidden, gin.H{
			"error":   "group quota exceeded",
			"limit":   quota,
			"created": owned,
		})
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

	_, err = h.consentRepo.Create("group_invite", userID, req.UserID, &convID, req.Message, false)
	if err != nil {
		logger.Log.Error("failed to create invite", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to send invite"})
		return
	}

	// Push a realtime notification so the invitee's badge updates immediately.
	events.Publish(c.Request.Context(), events.ConsentRequested, []int64{req.UserID}, gin.H{
		"conversation_id": convID,
		"user_id":         userID,
	})

	c.JSON(http.StatusCreated, gin.H{"message": "invite sent"})
}

// StartPrivateChat sends a private chat consent request to another user. When
// [req.Encrypted] is true the resulting conversation will be created with
// end-to-end encryption enabled as soon as the recipient accepts.
func (h *ConversationHandler) StartPrivateChat(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req struct {
		UserID    int64  `json:"user_id" binding:"required"`
		Message   string `json:"message"`
		Encrypted bool   `json:"encrypted"`
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

	_, err = h.consentRepo.Create("private_chat", userID, req.UserID, nil, req.Message, req.Encrypted)
	if err != nil {
		logger.Log.Error("failed to create chat request", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start chat"})
		return
	}

	// Push a realtime notification so the recipient's badge updates immediately.
	events.Publish(c.Request.Context(), events.ConsentRequested, []int64{req.UserID}, gin.H{
		"user_id":   userID,
		"encrypted": req.Encrypted,
	})

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

// UpdateNotifyLevel sets the member's chat-notification preference for a
// conversation (all | mentions | none).
func (h *ConversationHandler) UpdateNotifyLevel(c *gin.Context) {
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
		Level string `json:"level" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	switch req.Level {
	case "all", "mentions", "none":
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid notify level (expected all|mentions|none)"})
		return
	}

	isMember, err := h.convRepo.IsMember(convID, userID)
	if err != nil || !isMember {
		c.JSON(http.StatusForbidden, gin.H{"error": "you are not a member of this conversation"})
		return
	}

	if err := h.convRepo.UpdateNotifyLevel(convID, userID, req.Level); err != nil {
		logger.Log.Error("failed to update notify level", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update notify level"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "notify level updated"})
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

	// Announce the departure to the remaining members with a system message.
	sys, err := h.msgRepo.CreateSystem(convID, userID, "system.leave")
	if err != nil {
		logger.Log.Warn("failed to create leave system message", "error", err, "conversation_id", convID)
	}
	h.publishConversationEvent(c.Request.Context(), convID)
	if sys != nil {
		h.publishMessageEvent(c.Request.Context(), events.ChatMessageCreated, convID, sys)
	}

	c.Status(http.StatusNoContent)
}

// Delete permanently removes a conversation and all its messages. For groups
// only the owner can delete; either participant can delete a private chat.
func (h *ConversationHandler) Delete(c *gin.Context) {
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete conversation"})
		return
	}
	if conv == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "conversation not found"})
		return
	}

	member, err := h.convRepo.GetMember(convID, userID)
	if err != nil || member == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "you are not a member of this conversation"})
		return
	}

	if conv.Type == "group" && member.Role != "owner" {
		c.JSON(http.StatusForbidden, gin.H{"error": "only the owner can delete the group"})
		return
	}

	if err := h.convRepo.Delete(convID); err != nil {
		logger.Log.Error("failed to delete conversation", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete conversation"})
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

// ListPublicGroups lists public groups that can be searched and joined.
func (h *ConversationHandler) ListPublicGroups(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	groups, err := h.convRepo.ListPublicGroups(userID, c.Query("q"), limit)
	if err != nil {
		logger.Log.Error("failed to list public groups", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list public groups"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"groups": groups})
}

// JoinGroup lets a user join a public group that allows self-join.
func (h *ConversationHandler) JoinGroup(c *gin.Context) {
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to join group"})
		return
	}
	if conv == nil || conv.Type != "group" {
		c.JSON(http.StatusNotFound, gin.H{"error": "group not found"})
		return
	}
	if !conv.IsPublic || !conv.AllowJoin {
		c.JSON(http.StatusForbidden, gin.H{"error": "this group does not allow direct joining"})
		return
	}

	isMember, err := h.convRepo.IsMember(convID, userID)
	if err != nil {
		logger.Log.Error("failed to check membership", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to join group"})
		return
	}
	if isMember {
		c.JSON(http.StatusConflict, gin.H{"error": "you are already a member of this group"})
		return
	}

	if err := h.convRepo.AddMember(convID, userID, userID, "member", "active"); err != nil {
		logger.Log.Error("failed to add member", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to join group"})
		return
	}

	// Announce the arrival to the whole group with a system message.
	sys, err := h.msgRepo.CreateSystem(convID, userID, "system.join")
	if err != nil {
		logger.Log.Warn("failed to create join system message", "error", err, "conversation_id", convID)
	}
	h.publishConversationEvent(c.Request.Context(), convID)
	if sys != nil {
		h.publishMessageEvent(c.Request.Context(), events.ChatMessageCreated, convID, sys)
	}

	c.JSON(http.StatusOK, conv)
}

// UpdateSettings updates a group's visibility and self-join policy (owner only).
func (h *ConversationHandler) UpdateSettings(c *gin.Context) {
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
		IsPublic  bool `json:"is_public"`
		AllowJoin bool `json:"allow_join"`
		Encrypted bool `json:"encrypted"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	conv, err := h.convRepo.GetByID(convID)
	if err != nil {
		logger.Log.Error("failed to get conversation", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update group settings"})
		return
	}
	if conv == nil || conv.Type != "group" {
		c.JSON(http.StatusNotFound, gin.H{"error": "group not found"})
		return
	}

	member, err := h.convRepo.GetMember(convID, userID)
	if err != nil || member == nil || member.Role != "owner" {
		c.JSON(http.StatusForbidden, gin.H{"error": "only the group owner can edit settings"})
		return
	}

	if err := h.convRepo.UpdateGroupSettings(convID, req.IsPublic, req.AllowJoin, req.Encrypted); err != nil {
		logger.Log.Error("failed to update group settings", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update group settings"})
		return
	}

	h.publishConversationEvent(c.Request.Context(), convID)
	c.JSON(http.StatusOK, gin.H{"message": "group settings updated"})
}

// UpdateTitle renames a group conversation (owner only).
func (h *ConversationHandler) UpdateTitle(c *gin.Context) {
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
		Title string `json:"title" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "group title cannot be empty"})
		return
	}
	if utf8.RuneCountInString(title) > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "group title too long (max 100)"})
		return
	}

	conv, err := h.convRepo.GetByID(convID)
	if err != nil {
		logger.Log.Error("failed to get conversation", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update group title"})
		return
	}
	if conv == nil || conv.Type != "group" {
		c.JSON(http.StatusNotFound, gin.H{"error": "group not found"})
		return
	}

	member, err := h.convRepo.GetMember(convID, userID)
	if err != nil || member == nil || member.Role != "owner" {
		c.JSON(http.StatusForbidden, gin.H{"error": "only the group owner can edit the title"})
		return
	}

	if err := h.convRepo.UpdateGroupTitle(convID, title); err != nil {
		logger.Log.Error("failed to update group title", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update group title"})
		return
	}

	h.publishConversationEvent(c.Request.Context(), convID)
	c.JSON(http.StatusOK, gin.H{"message": "group title updated"})
}

// UpdateAvatar sets a group's avatar image URL (owner only).
func (h *ConversationHandler) UpdateAvatar(c *gin.Context) {
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
		AvatarURL string `json:"avatar_url" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	avatarURL := strings.TrimSpace(req.AvatarURL)
	if avatarURL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "avatar URL cannot be empty"})
		return
	}

	conv, err := h.convRepo.GetByID(convID)
	if err != nil {
		logger.Log.Error("failed to get conversation", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update group avatar"})
		return
	}
	if conv == nil || conv.Type != "group" {
		c.JSON(http.StatusNotFound, gin.H{"error": "group not found"})
		return
	}

	member, err := h.convRepo.GetMember(convID, userID)
	if err != nil || member == nil || member.Role != "owner" {
		c.JSON(http.StatusForbidden, gin.H{"error": "only the group owner can edit the avatar"})
		return
	}

	if err := h.convRepo.UpdateGroupAvatar(convID, avatarURL); err != nil {
		logger.Log.Error("failed to update group avatar", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update group avatar"})
		return
	}

	h.publishConversationEvent(c.Request.Context(), convID)
	c.JSON(http.StatusOK, gin.H{"message": "group avatar updated"})
}

// SetMemberRole promotes or demotes a member (owner only).
func (h *ConversationHandler) SetMemberRole(c *gin.Context) {
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

	var req struct {
		Role string `json:"role" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if req.Role != "admin" && req.Role != "member" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "role must be admin or member"})
		return
	}

	actor, err := h.convRepo.GetMember(convID, userID)
	if err != nil || actor == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "you are not a member of this conversation"})
		return
	}
	if actor.Role != "owner" {
		c.JSON(http.StatusForbidden, gin.H{"error": "only the group owner can change roles"})
		return
	}

	target, err := h.convRepo.GetMember(convID, targetID)
	if err != nil || target == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "member not found"})
		return
	}
	if target.Role == "owner" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot change the group owner's role"})
		return
	}

	if err := h.convRepo.SetMemberRole(convID, targetID, req.Role); err != nil {
		logger.Log.Error("failed to set member role", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to set member role"})
		return
	}

	h.publishConversationEvent(c.Request.Context(), convID)
	c.JSON(http.StatusOK, gin.H{"message": "role updated"})
}

// SetMemberTitle sets the custom title shown next to a member's nickname in
// the chat (owner/admin only). Titles are an owner-controlled label and must
// not be confused with the self-set group_nickname.
func (h *ConversationHandler) SetMemberTitle(c *gin.Context) {
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

	var req struct {
		Title string `json:"title"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	// Cap to a reasonable size so a malicious admin cannot flood the UI.
	if len([]rune(req.Title)) > 24 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "title too long (max 24 chars)"})
		return
	}

	actor, err := h.convRepo.GetMember(convID, userID)
	if err != nil || actor == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "you are not a member of this conversation"})
		return
	}
	if actor.Role != "owner" && actor.Role != "admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "only group owner or admin can set titles"})
		return
	}

	target, err := h.convRepo.GetMember(convID, targetID)
	if err != nil || target == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "member not found"})
		return
	}

	if err := h.convRepo.UpdateMemberTitle(convID, targetID, req.Title); err != nil {
		logger.Log.Error("failed to set member title", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to set member title"})
		return
	}

	h.publishConversationEvent(c.Request.Context(), convID)
	c.JSON(http.StatusOK, gin.H{"message": "title updated"})
}

// maxMuteMinutes is the longest mute allowed (10 years).
const maxMuteMinutes = 5256000

// MuteMember mutes a member for a set duration (owner/admin only).
func (h *ConversationHandler) MuteMember(c *gin.Context) {
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

	var req struct {
		DurationMinutes int64 `json:"duration_minutes"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if req.DurationMinutes <= 0 || req.DurationMinutes > maxMuteMinutes {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid mute duration"})
		return
	}

	actor, err := h.convRepo.GetMember(convID, userID)
	if err != nil || actor == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "you are not a member of this conversation"})
		return
	}
	if actor.Role != "owner" && actor.Role != "admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "only the owner or admins can mute members"})
		return
	}

	target, err := h.convRepo.GetMember(convID, targetID)
	if err != nil || target == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "member not found"})
		return
	}
	if target.Role == "owner" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot mute the group owner"})
		return
	}
	if actor.Role == "admin" && target.Role == "admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "admins cannot mute other admins"})
		return
	}

	until := time.Now().Add(time.Duration(req.DurationMinutes) * time.Minute)
	if err := h.convRepo.SetMemberMute(convID, targetID, &until); err != nil {
		logger.Log.Error("failed to mute member", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to mute member"})
		return
	}

	sys, err := h.msgRepo.CreateSystem(convID, targetID, "system.mute")
	if err != nil {
		logger.Log.Warn("failed to create mute system message", "error", err, "conversation_id", convID)
	}
	h.publishConversationEvent(c.Request.Context(), convID)
	if sys != nil {
		h.publishMessageEvent(c.Request.Context(), events.ChatMessageCreated, convID, sys)
	}

	c.JSON(http.StatusOK, gin.H{"message": "member muted", "muted_until": until})
}

// UnmuteMember removes an individual mute (owner/admin only).
func (h *ConversationHandler) UnmuteMember(c *gin.Context) {
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
		c.JSON(http.StatusForbidden, gin.H{"error": "only the owner or admins can unmute members"})
		return
	}

	target, err := h.convRepo.GetMember(convID, targetID)
	if err != nil || target == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "member not found"})
		return
	}
	if target.Role == "owner" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot unmute the group owner"})
		return
	}
	if actor.Role == "admin" && target.Role == "admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "admins cannot unmute other admins"})
		return
	}

	if err := h.convRepo.SetMemberMute(convID, targetID, nil); err != nil {
		logger.Log.Error("failed to unmute member", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to unmute member"})
		return
	}

	sys, err := h.msgRepo.CreateSystem(convID, targetID, "system.unmute")
	if err != nil {
		logger.Log.Warn("failed to create unmute system message", "error", err, "conversation_id", convID)
	}
	h.publishConversationEvent(c.Request.Context(), convID)
	if sys != nil {
		h.publishMessageEvent(c.Request.Context(), events.ChatMessageCreated, convID, sys)
	}

	c.JSON(http.StatusOK, gin.H{"message": "member unmuted"})
}

// MuteAll mutes every non-staff member for a duration (owner/admin only).
func (h *ConversationHandler) MuteAll(c *gin.Context) {
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
		DurationMinutes int64 `json:"duration_minutes"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if req.DurationMinutes <= 0 || req.DurationMinutes > maxMuteMinutes {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid mute duration"})
		return
	}

	actor, err := h.convRepo.GetMember(convID, userID)
	if err != nil || actor == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "you are not a member of this conversation"})
		return
	}
	if actor.Role != "owner" && actor.Role != "admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "only the owner or admins can mute the group"})
		return
	}

	until := time.Now().Add(time.Duration(req.DurationMinutes) * time.Minute)
	if err := h.convRepo.SetGroupMuteAll(convID, &until); err != nil {
		logger.Log.Error("failed to mute group", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to mute group"})
		return
	}

	sys, err := h.msgRepo.CreateSystem(convID, userID, "system.mute.all")
	if err != nil {
		logger.Log.Warn("failed to create mute-all system message", "error", err, "conversation_id", convID)
	}
	h.publishConversationEvent(c.Request.Context(), convID)
	if sys != nil {
		h.publishMessageEvent(c.Request.Context(), events.ChatMessageCreated, convID, sys)
	}

	c.JSON(http.StatusOK, gin.H{"message": "group muted", "mute_all_until": until})
}

// UnmuteAll clears the group-wide mute (owner/admin only).
func (h *ConversationHandler) UnmuteAll(c *gin.Context) {
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

	actor, err := h.convRepo.GetMember(convID, userID)
	if err != nil || actor == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "you are not a member of this conversation"})
		return
	}
	if actor.Role != "owner" && actor.Role != "admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "only the owner or admins can unmute the group"})
		return
	}

	if err := h.convRepo.SetGroupMuteAll(convID, nil); err != nil {
		logger.Log.Error("failed to unmute group", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to unmute group"})
		return
	}

	sys, err := h.msgRepo.CreateSystem(convID, userID, "system.unmute.all")
	if err != nil {
		logger.Log.Warn("failed to create unmute-all system message", "error", err, "conversation_id", convID)
	}
	h.publishConversationEvent(c.Request.Context(), convID)
	if sys != nil {
		h.publishMessageEvent(c.Request.Context(), events.ChatMessageCreated, convID, sys)
	}

	c.JSON(http.StatusOK, gin.H{"message": "group unmuted"})
}

// GetE2EEKeys returns the current group-key envelopes for every member of an
// encrypted conversation, including the requesting member's own envelope.
func (h *ConversationHandler) GetE2EEKeys(c *gin.Context) {
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get e2ee keys"})
		return
	}
	if !isMember {
		c.JSON(http.StatusForbidden, gin.H{"error": "you are not a member of this conversation"})
		return
	}

	envelopes, err := h.convRepo.ListE2EEKeyEnvelopes(convID)
	if err != nil {
		logger.Log.Error("failed to list e2ee envelopes", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get e2ee keys"})
		return
	}

	myEnvelope, err := h.convRepo.GetE2EEEnvelopeFor(convID, userID)
	if err != nil {
		logger.Log.Error("failed to read my e2ee envelope", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get e2ee keys"})
		return
	}

	version, err := h.convRepo.CurrentE2EEVersion(convID)
	if err != nil {
		logger.Log.Error("failed to read e2ee version", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get e2ee keys"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"version":         version,
		"envelopes":       envelopes,
		"my_envelope":     myEnvelope,
		"conversation_id": convID,
	})
}

// PutE2EEKeys stores a batch of group-key envelopes after a key rotation
// (enabling encryption, or adding a member). The caller must be an active
// member; the server never inspects the ciphertext.
func (h *ConversationHandler) PutE2EEKeys(c *gin.Context) {
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to store e2ee keys"})
		return
	}
	if !isMember {
		c.JSON(http.StatusForbidden, gin.H{"error": "you are not a member of this conversation"})
		return
	}

	var req struct {
		Envelopes map[int64]string `json:"envelopes"` // target_user_id -> base64 ciphertext
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if len(req.Envelopes) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no envelopes provided"})
		return
	}

	version, err := h.convRepo.PutE2EEKeys(convID, req.Envelopes)
	if err != nil {
		logger.Log.Error("failed to store e2ee keys", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to store e2ee keys"})
		return
	}

	h.publishMessageEvent(c.Request.Context(), events.ChatE2EEKeysUpdated, convID, gin.H{
		"conversation_id": convID,
		"version":         version,
	})

	c.JSON(http.StatusOK, gin.H{"message": "e2ee keys stored", "version": version})
}

// publishConversationEvent notifies the group's active members that the
// conversation changed (membership, roles, settings, mutes).
func (h *ConversationHandler) publishConversationEvent(ctx context.Context, convID int64) {
	members, err := h.convRepo.ListMembers(convID)
	if err != nil {
		logger.Log.Warn("failed to list members for conversation event", "error", err, "conversation_id", convID)
		return
	}
	recipients := make([]int64, 0, len(members))
	for _, m := range members {
		if m.Status == "active" {
			recipients = append(recipients, m.UserID)
		}
	}
	events.Publish(ctx, events.ConversationUpdated, recipients, gin.H{"conversation_id": convID})
}

// publishMessageEvent notifies all active members about a message change.
func (h *ConversationHandler) publishMessageEvent(ctx context.Context, typ string, convID int64, msg interface{}) {
	members, err := h.convRepo.ListMembers(convID)
	if err != nil {
		logger.Log.Warn("failed to list members for push", "error", err, "conversation_id", convID)
		return
	}
	recipients := make([]int64, 0, len(members))
	for _, m := range members {
		if m.Status == "active" {
			recipients = append(recipients, m.UserID)
		}
	}
	events.Publish(ctx, typ, recipients, msg)
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
