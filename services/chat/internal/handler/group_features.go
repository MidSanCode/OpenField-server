package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/logger"
	"github.com/openfield/server/pkg/middleware"
	"github.com/openfield/server/pkg/repository"
)

// GroupExtrasHandler handles per-conversation announcements, todos and the
// shared attachment (file) list.
type GroupExtrasHandler struct {
	convRepo  *repository.ConversationRepository
	annRepo   *repository.GroupAnnouncementRepository
	todoRepo  *repository.GroupTodoRepository
}

// NewGroupExtrasHandler creates a new GroupExtrasHandler.
func NewGroupExtrasHandler() *GroupExtrasHandler {
	return &GroupExtrasHandler{
		convRepo: repository.NewConversationRepository(),
		annRepo:  repository.NewGroupAnnouncementRepository(),
		todoRepo: repository.NewGroupTodoRepository(),
	}
}

// requireMember resolves the caller and enforces membership; managers passes
// true additionally require chat.group.manage.
func (h *GroupExtrasHandler) requireMember(c *gin.Context, convID int64, manager bool) (int64, bool) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return 0, false
	}
	isMember, err := h.convRepo.IsMember(convID, userID)
	if err != nil {
		logger.Log.Error("failed to check membership", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to check membership"})
		return 0, false
	}
	if !isMember {
		c.JSON(http.StatusForbidden, gin.H{"error": "you are not a member of this conversation"})
		return 0, false
	}
	if manager {
		member, err := h.convRepo.GetMember(convID, userID)
		if err != nil {
			logger.Log.Error("failed to load membership", "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to check role"})
			return 0, false
		}
		isManager := member != nil && (member.Role == "owner" || member.Role == "admin")
		conv, err := h.convRepo.GetByID(convID)
		if err == nil && conv != nil && conv.OwnerID == userID {
			isManager = true
		}
		if !isManager {
			c.JSON(http.StatusForbidden, gin.H{"error": "group manager role required"})
			return 0, false
		}
	}
	return userID, true
}

// ---- announcements ----

// ListAnnouncements returns the conversation's announcements.
func (h *GroupExtrasHandler) ListAnnouncements(c *gin.Context) {
	convID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid conversation ID"})
		return
	}
	if _, ok := h.requireMember(c, convID, false); !ok {
		return
	}
	list, err := h.annRepo.ListByConversation(convID, 50)
	if err != nil {
		logger.Log.Error("failed to list announcements", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list announcements"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"announcements": list})
}

// CreateAnnouncement publishes an announcement (managers only).
func (h *GroupExtrasHandler) CreateAnnouncement(c *gin.Context) {
	convID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid conversation ID"})
		return
	}
	userID, ok := h.requireMember(c, convID, true)
	if !ok {
		return
	}
	var req struct {
		Title   string `json:"title"`
		Content string `json:"content" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if len(req.Content) > 5000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "content too long (max 5000)"})
		return
	}
	if len(req.Title) > 200 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "title too long (max 200)"})
		return
	}
	ann, err := h.annRepo.Create(convID, userID, req.Title, req.Content)
	if err != nil {
		logger.Log.Error("failed to create announcement", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create announcement"})
		return
	}
	c.JSON(http.StatusCreated, ann)
}

// DeleteAnnouncement removes one announcement (managers only).
func (h *GroupExtrasHandler) DeleteAnnouncement(c *gin.Context) {
	convID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid conversation ID"})
		return
	}
	if _, ok := h.requireMember(c, convID, true); !ok {
		return
	}
	annID, err := strconv.ParseInt(c.Param("announcement_id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid announcement ID"})
		return
	}
	if err := h.annRepo.Delete(annID, convID); err != nil {
		if err == repository.ErrNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "announcement not found"})
			return
		}
		logger.Log.Error("failed to delete announcement", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete announcement"})
		return
	}
	c.Status(http.StatusNoContent)
}

// ---- todos ----

// ListTodos returns the conversation's checklist.
func (h *GroupExtrasHandler) ListTodos(c *gin.Context) {
	convID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid conversation ID"})
		return
	}
	if _, ok := h.requireMember(c, convID, false); !ok {
		return
	}
	list, err := h.todoRepo.ListByConversation(convID, 100)
	if err != nil {
		logger.Log.Error("failed to list todos", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list todos"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"todos": list})
}

// CreateTodo adds a checklist entry (any member).
func (h *GroupExtrasHandler) CreateTodo(c *gin.Context) {
	convID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid conversation ID"})
		return
	}
	userID, ok := h.requireMember(c, convID, false)
	if !ok {
		return
	}
	var req struct {
		Title string `json:"title" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	req.Title = trim(req.Title)
	if req.Title == "" || len([]rune(req.Title)) > 200 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "todo title must be 1-200 characters"})
		return
	}
	todo, err := h.todoRepo.Create(convID, userID, req.Title)
	if err != nil {
		logger.Log.Error("failed to create todo", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create todo"})
		return
	}
	c.JSON(http.StatusCreated, todo)
}

// UpdateTodo checks/unchecks an entry (any member).
func (h *GroupExtrasHandler) UpdateTodo(c *gin.Context) {
	convID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid conversation ID"})
		return
	}
	userID, ok := h.requireMember(c, convID, false)
	if !ok {
		return
	}
	todoID, err := strconv.ParseInt(c.Param("todo_id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid todo ID"})
		return
	}
	var req struct {
		Done *bool `json:"done"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Done == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "done boolean required"})
		return
	}
	if err := h.todoRepo.SetDone(todoID, convID, userID, *req.Done); err != nil {
		if err == repository.ErrNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "todo not found"})
			return
		}
		logger.Log.Error("failed to update todo", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update todo"})
		return
	}
	c.Status(http.StatusNoContent)
}

// DeleteTodo removes one entry (creator or managers).
func (h *GroupExtrasHandler) DeleteTodo(c *gin.Context) {
	convID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid conversation ID"})
		return
	}
	userID, ok := h.requireMember(c, convID, false)
	if !ok {
		return
	}
	_ = userID
	todoID, err := strconv.ParseInt(c.Param("todo_id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid todo ID"})
		return
	}
	if err := h.todoRepo.Delete(todoID, convID); err != nil {
		if err == repository.ErrNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "todo not found"})
			return
		}
		logger.Log.Error("failed to delete todo", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete todo"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

// ---- shared files ----

// ListFiles surfaces the attachments shared into the conversation, newest
// first, with cursor pagination on message id.
func (h *GroupExtrasHandler) ListFiles(c *gin.Context) {
	convID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid conversation ID"})
		return
	}
	if _, ok := h.requireMember(c, convID, false); !ok {
		return
	}
	beforeID, _ := strconv.ParseInt(c.DefaultQuery("before", "0"), 10, 64)
	files, err := repository.ListConversationFiles(convID, beforeID, 100)
	if err != nil {
		logger.Log.Error("failed to list conversation files", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list files"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"files": files})
}

func trim(s string) string {
	out := []rune(s)
	start, end := 0, len(out)-1
	for start <= end && (out[start] == ' ' || out[start] == '\t' || out[start] == '\n' || out[start] == '\r') {
		start++
	}
	for end >= start && (out[end] == ' ' || out[end] == '\t' || out[end] == '\n' || out[end] == '\r') {
		end--
	}
	if start > end {
		return ""
	}
	return string(out[start : end+1])
}
