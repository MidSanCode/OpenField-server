package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/events"
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
		// Non-members may read messages of public groups (public = viewable).
		conv, convErr := h.convRepo.GetByID(convID)
		if convErr != nil || conv == nil || conv.Type != "group" || !conv.IsPublic {
			c.JSON(http.StatusForbidden, gin.H{"error": "you are not a member of this conversation"})
			return
		}
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

// Search looks up chat history in a conversation by content keyword, sender,
// time range and/or attachments (presence or file name). All criteria are
// optional and combined with AND; an empty filter returns the newest messages.
func (h *MessageHandler) Search(c *gin.Context) {
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to search messages"})
		return
	}
	if !isMember {
		// Same visibility rule as List: public groups are readable by anyone.
		conv, convErr := h.convRepo.GetByID(convID)
		if convErr != nil || conv == nil || conv.Type != "group" || !conv.IsPublic {
			c.JSON(http.StatusForbidden, gin.H{"error": "you are not a member of this conversation"})
			return
		}
	}

	filter := repository.MessageSearchFilter{
		Content:  c.Query("q"),
		FileName: c.Query("file_name"),
	}
	filter.Content = strings.TrimSpace(filter.Content)
	filter.FileName = strings.TrimSpace(filter.FileName)

	if v := c.Query("sender_id"); v != "" {
		senderID, err := strconv.ParseInt(v, 10, 64)
		if err != nil || senderID <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid sender ID"})
			return
		}
		filter.SenderID = senderID
	}
	parseTime := func(name string) (*time.Time, error) {
		v := c.Query(name)
		if v == "" {
			return nil, nil
		}
		if ts, err := strconv.ParseInt(v, 10, 64); err == nil {
			t := time.Unix(ts, 0).UTC()
			return &t, nil
		}
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return &t, nil
		}
		return nil, fmt.Errorf("invalid %s (use RFC3339 or unix seconds)", name)
	}
	from, err := parseTime("from")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	to, err := parseTime("to")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	filter.From = from
	filter.To = to

	switch c.DefaultQuery("has_attachment", "") {
	case "true", "1":
		filter.HasAttachments = true
	case "false", "0", "":
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid has_attachment value"})
		return
	}

	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))

	msgs, err := h.msgRepo.Search(convID, filter, limit)
	if err != nil {
		logger.Log.Error("failed to search messages", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to search messages"})
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
		Content       string  `json:"content"`
		ReplyToID     *int64  `json:"reply_to_id"`
		AttachmentIDs []int64 `json:"attachment_ids"`
		Mentions      []int64 `json:"mentions"`
		CheckID       int64   `json:"check_id"`
		BurnSeconds   int     `json:"burn_seconds"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if !burnSecondsAllowed(req.BurnSeconds) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid burn_seconds (allowed: 0, or 5..86400)"})
		return
	}
	if req.Content == "" && len(req.AttachmentIDs) == 0 && req.CheckID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "message cannot be empty"})
		return
	}
	if len(req.Content) > 5000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "message too long (max 5000)"})
		return
	}
	if len(req.AttachmentIDs) > 9 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "too many attachments (max 9)"})
		return
	}

	// A check message carries no text of its own; validate the check up front
	// so a typo'd id fails loudly instead of rendering an empty bubble.
	checkRepo := repository.NewCheckRepository()
	if req.CheckID > 0 {
		if err := checkRepo.ValidateOwnedActive(req.CheckID, userID); err != nil {
			switch {
			case errors.Is(err, repository.ErrNotFound):
				c.JSON(http.StatusNotFound, gin.H{"error": "check not found"})
			case errors.Is(err, repository.ErrInvalidCheck):
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid or expired check"})
			default:
				logger.Log.Error("failed to validate check", "error", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to send message"})
			}
			return
		}
	}

	// @everyone is restricted to group owners and admins. Drop the sentinel
	// from non-privileged senders and let the client fail loudly.
	if hasEveryone := containsEveryone(req.Mentions); hasEveryone {
		actor, err := h.convRepo.GetMember(convID, userID)
		if err != nil || actor == nil {
			c.JSON(http.StatusForbidden, gin.H{"error": "you are not a member of this conversation"})
			return
		}
		if actor.Role != "owner" && actor.Role != "admin" {
			req.Mentions = removeEveryone(req.Mentions)
		}
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

	muted, err := h.convRepo.IsUserMuted(convID, userID)
	if err != nil {
		logger.Log.Error("failed to check mute status", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to send message"})
		return
	}
	if muted {
		c.JSON(http.StatusForbidden, gin.H{"error": "you are muted and cannot send messages in this conversation"})
		return
	}

	msg, err := h.msgRepo.Create(convID, userID, req.Content, req.ReplyToID, req.AttachmentIDs, req.Mentions, req.CheckID, req.BurnSeconds)
	if err != nil {
		logger.Log.Error("failed to send message", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to send message"})
		return
	}

	h.publishMessageEvent(c.Request.Context(), events.ChatMessageCreated, convID, msg)

	c.JSON(http.StatusCreated, msg)
}

// Forward copies messages from one conversation into one or more other
// conversations the sender belongs to. The copies are new messages authored
// by the forwarding user: content and attachments are re-linked as-is, while
// replies, checks, burn timers and mentions are stripped. E2EE-encrypted
// conversations never reach this endpoint because their clients do not
// expose the entry point (the ciphertext is bound to the conversation key).
func (h *MessageHandler) Forward(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	sourceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid conversation ID"})
		return
	}

	var req struct {
		MessageIDs            []int64 `json:"message_ids"`
		TargetConversationIDs []int64 `json:"target_conversation_ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if len(req.MessageIDs) == 0 || len(req.MessageIDs) > 20 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "forward between 1 and 20 messages"})
		return
	}
	if len(req.TargetConversationIDs) == 0 || len(req.TargetConversationIDs) > 10 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "forward to between 1 and 10 conversations"})
		return
	}

	// The sender must be able to read the source conversation.
	isMember, err := h.convRepo.IsMember(sourceID, userID)
	if err != nil {
		logger.Log.Error("failed to check source membership", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to forward messages"})
		return
	}
	if !isMember {
		conv, convErr := h.convRepo.GetByID(sourceID)
		if convErr != nil || conv == nil || conv.Type != "group" || !conv.IsPublic {
			c.JSON(http.StatusForbidden, gin.H{"error": "you cannot read this conversation"})
			return
		}
	}

	// Validate every source message up front so the loop below either
	// forwards a clean batch or fails before creating anything.
	type forwardItem struct {
		content       string
		attachmentIDs []int64
	}
	items := make([]forwardItem, 0, len(req.MessageIDs))
	for _, msgID := range req.MessageIDs {
		msg, err := h.msgRepo.GetByID(msgID)
		if err != nil {
			logger.Log.Error("failed to load message for forward", "error", err, "message_id", msgID)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to forward messages"})
			return
		}
		if msg == nil || msg.ConversationID != sourceID || msg.DeletedAt != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "message not found in this conversation"})
			return
		}
		if msg.Kind != "text" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "only text messages can be forwarded"})
			return
		}
		if msg.BurnSeconds > 0 || msg.BurnAt != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "burn-after-read messages cannot be forwarded"})
			return
		}
		if msg.CheckID > 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "check messages cannot be forwarded"})
			return
		}
		atts, err := h.msgRepo.AttachmentsForMessage(msgID)
		if err != nil {
			logger.Log.Error("failed to load message attachments for forward", "error", err, "message_id", msgID)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to forward messages"})
			return
		}
		attIDs := make([]int64, 0, len(atts))
		for _, a := range atts {
			attIDs = append(attIDs, a.ID)
		}
		items = append(items, forwardItem{content: msg.Content, attachmentIDs: attIDs})
	}

	forwarded := 0
	for _, targetID := range req.TargetConversationIDs {
		isMember, err := h.convRepo.IsMember(targetID, userID)
		if err != nil {
			logger.Log.Error("failed to check target membership", "error", err, "conversation_id", targetID)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to forward messages"})
			return
		}
		if !isMember {
			c.JSON(http.StatusForbidden, gin.H{"error": "you are not a member of a target conversation"})
			return
		}
		muted, err := h.convRepo.IsUserMuted(targetID, userID)
		if err != nil {
			logger.Log.Error("failed to check target mute status", "error", err, "conversation_id", targetID)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to forward messages"})
			return
		}
		if muted {
			c.JSON(http.StatusForbidden, gin.H{"error": "you are muted in a target conversation"})
			return
		}
		for _, item := range items {
			// Mentions do not survive a forward: the @-ed users belong to the
			// source conversation, and re-resolving them in the target would
			// surprise-notify strangers.
			msg, err := h.msgRepo.Create(targetID, userID, item.content, nil, item.attachmentIDs, nil, 0, 0)
			if err != nil {
				logger.Log.Error("failed to create forwarded message", "error", err, "conversation_id", targetID)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to forward messages"})
				return
			}
			h.publishMessageEvent(c.Request.Context(), events.ChatMessageCreated, targetID, msg)
			forwarded++
		}
	}

	c.JSON(http.StatusOK, gin.H{"forwarded": forwarded})
}

// burnSecondsAllowed reports whether a burn-after-read countdown (in seconds)
// may be requested. 0 disables burning; anything else is a custom countdown.
// The bounds stop clients from arming absurd timers (e.g. 1s flash-burns
// nobody can read, or week-long pseudo-persistence) while still allowing the
// picker's arbitrary custom values, not just the old fixed presets.
func burnSecondsAllowed(v int) bool {
	if v == 0 {
		return true
	}
	return v >= 5 && v <= 86400
}

// MarkBurnRead arms the burn-after-read countdown of a single message on the
// first read by a conversation member other than the sender. Idempotent:
// re-reads return the current state without re-arming. The freshly stamped
// burn_at is pushed to every member so senders see the countdown start.
func (h *MessageHandler) MarkBurnRead(c *gin.Context) {
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
	msgID, err := strconv.ParseInt(c.Param("message_id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid message ID"})
		return
	}

	isMember, err := h.convRepo.IsMember(convID, userID)
	if err != nil {
		logger.Log.Error("failed to check membership", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to mark message read"})
		return
	}
	if !isMember {
		c.JSON(http.StatusForbidden, gin.H{"error": "you are not a member of this conversation"})
		return
	}

	msg, stamped, err := h.msgRepo.MarkBurnRead(msgID, userID)
	if err != nil {
		logger.Log.Error("failed to mark message read", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to mark message read"})
		return
	}
	if msg == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "message not found"})
		return
	}
	if stamped {
		h.publishMessageEvent(c.Request.Context(), events.ChatMessageUpdated, msg.ConversationID, msg)
	}

	c.JSON(http.StatusOK, msg)
}

// StartBurnSweeper runs a background loop that soft-deletes burn-after-read
// messages whose countdown has expired and pushes deletion events so every
// member's client drops the bubble immediately — including the sender's,
// which would otherwise keep a copy forever.
func (h *MessageHandler) StartBurnSweeper(interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			swept, err := h.msgRepo.SweepBurned()
			if err != nil {
				logger.Log.Error("burn sweeper failed", "error", err)
				continue
			}
			for i := range swept {
				h.publishMessageEvent(context.Background(), events.ChatMessageDeleted, swept[i].ConversationID, swept[i])
			}
		}
	}()
}

// everyoneSentinel is the user id stored in [model.Message.Mentions] to mark
// an @everyone mention. We use -1 because real user ids are positive.
const everyoneSentinel = int64(-1)

func containsEveryone(m []int64) bool {
	for _, id := range m {
		if id == everyoneSentinel {
			return true
		}
	}
	return false
}

func removeEveryone(m []int64) []int64 {
	out := make([]int64, 0, len(m))
	for _, id := range m {
		if id != everyoneSentinel {
			out = append(out, id)
		}
	}
	return out
}

// publishMessageEvent notifies all conversation members (via the push service)
// about a message change.
func (h *MessageHandler) publishMessageEvent(ctx context.Context, typ string, convID int64, msg interface{}) {
	members, err := h.convRepo.ListMembers(convID)
	if err != nil {
		logger.Log.Warn("failed to list conversation members for push", "error", err, "conversation_id", convID)
		return
	}
	recipients := make([]int64, 0, len(members))
	for _, m := range members {
		if m.Status != "active" {
			continue
		}
		recipients = append(recipients, m.UserID)
	}
	events.Publish(ctx, typ, recipients, msg)
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

	h.publishMessageEvent(c.Request.Context(), events.ChatMessageUpdated, msg.ConversationID, msg)

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

	msg, err := h.msgRepo.GetByID(msgID)
	if err != nil {
		logger.Log.Error("failed to get message for delete event", "error", err)
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

	if msg != nil {
		now := time.Now()
		msg.DeletedAt = &now
		h.publishMessageEvent(c.Request.Context(), events.ChatMessageDeleted, msg.ConversationID, msg)
	}

	c.Status(http.StatusNoContent)
}
