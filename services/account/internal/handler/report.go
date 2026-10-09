package handler

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/logger"
	"github.com/openfield/server/pkg/middleware"
	"github.com/openfield/server/pkg/model"
	"github.com/openfield/server/pkg/repository"
)

// maxReportReasonRunes bounds the free-text reason. Long enough to explain
// the problem, short enough that the moderation queue stays readable.
const maxReportReasonRunes = 500

// ReportHandler serves the user-facing moderation report endpoints (举报).
// Reports are reviewed by the admin dashboard; these endpoints only file and
// list a caller's own reports.
type ReportHandler struct {
	repo *repository.ReportRepository
}

// NewReportHandler creates a new ReportHandler.
func NewReportHandler() *ReportHandler {
	return &ReportHandler{repo: repository.NewReportRepository()}
}

// Create files a report against a post, a chat message or a user.
// POST /api/v1/reports {target_type, target_id, reason}
func (h *ReportHandler) Create(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var req struct {
		TargetType string `json:"target_type" binding:"required"`
		TargetID   int64  `json:"target_id" binding:"required"`
		Reason     string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	req.TargetType = strings.TrimSpace(req.TargetType)
	switch req.TargetType {
	case model.ReportTargetPost, model.ReportTargetMessage, model.ReportTargetUser:
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "target_type must be one of post, message, user"})
		return
	}
	if req.TargetID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid target_id"})
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if utf8.RuneCountInString(reason) > maxReportReasonRunes {
		c.JSON(http.StatusBadRequest, gin.H{"error": "reason too long (max 500 characters)"})
		return
	}
	if reason == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "reason is required"})
		return
	}
	if req.TargetType == model.ReportTargetUser && req.TargetID == userID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "you cannot report yourself"})
		return
	}

	report, err := h.repo.Create(userID, req.TargetType, req.TargetID, reason)
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "the reported content does not exist"})
			return
		case errors.Is(err, repository.ErrAlreadyReported):
			c.JSON(http.StatusConflict, gin.H{"error": "you have already reported this"})
			return
		case errors.Is(err, repository.ErrReportRateLimit):
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "too many reports, please try again later"})
			return
		}
		logger.Log.Error("failed to create report", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to submit report"})
		return
	}
	c.JSON(http.StatusCreated, report)
}

// ListMine returns the caller's own reports, newest first.
// GET /api/v1/reports/mine
func (h *ReportHandler) ListMine(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	reports, err := h.repo.ListMine(userID, limit)
	if err != nil {
		logger.Log.Error("failed to list reports", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list reports"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"reports": reports})
}
