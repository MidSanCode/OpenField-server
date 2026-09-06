package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/logger"
	"github.com/openfield/server/pkg/middleware"
	"github.com/openfield/server/pkg/repository"
)

// AppAnnouncementHandler manages server-wide announcements: anyone may read
// active ones (startup banner, history), admin permission holders publish
// and retire them from the app or the admin backend.
type AppAnnouncementHandler struct{}

// NewAppAnnouncementHandler creates a new handler.
func NewAppAnnouncementHandler() *AppAnnouncementHandler {
	return &AppAnnouncementHandler{}
}

// List returns announcements. Default: active only (what clients show on
// startup). ?all=1 returns the full history including retired entries.
func (h *AppAnnouncementHandler) List(c *gin.Context) {
	rows, err := repository.ListAppAnnouncements(c.Query("all") == "1")
	if err != nil {
		logger.Log.Error("failed to list app announcements", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list announcements"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"announcements": rows})
}

// Create publishes a new announcement (admin).
func (h *AppAnnouncementHandler) Create(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var req struct {
		Title   string `json:"title" binding:"required"`
		Content string `json:"content" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if len(req.Title) > 200 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "title too long (max 200)"})
		return
	}
	if len(req.Content) > 10000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "content too long (max 10000)"})
		return
	}
	ann, err := repository.CreateAppAnnouncement(req.Title, req.Content, userID)
	if err != nil {
		logger.Log.Error("failed to create app announcement", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create announcement"})
		return
	}
	c.JSON(http.StatusCreated, ann)
}

// Update toggles active state (admin). Body: {"active": false} retires the
// announcement without deleting the history entry.
func (h *AppAnnouncementHandler) Update(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid announcement ID"})
		return
	}
	var req struct {
		Active *bool `json:"active"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Active == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "active boolean required"})
		return
	}
	if err := repository.SetAppAnnouncementActive(id, *req.Active); err != nil {
		if err == repository.ErrNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "announcement not found"})
			return
		}
		logger.Log.Error("failed to update app announcement", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update announcement"})
		return
	}
	c.Status(http.StatusNoContent)
}
