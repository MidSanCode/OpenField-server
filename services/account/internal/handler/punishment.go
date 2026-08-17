package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/logger"
	"github.com/openfield/server/pkg/middleware"
	"github.com/openfield/server/pkg/model"
	"github.com/openfield/server/pkg/repository"
)

// PunishmentHandler handles moderation actions (警告/记过/剥夺权限/封禁/解封).
type PunishmentHandler struct {
	userRepo       *repository.UserRepository
	punishRepo     *repository.PunishmentRepository
	permRepo       *repository.PermissionRepository
}

// NewPunishmentHandler creates a new PunishmentHandler.
func NewPunishmentHandler() *PunishmentHandler {
	return &PunishmentHandler{
		userRepo:   repository.NewUserRepository(),
		punishRepo: repository.NewPunishmentRepository(),
		permRepo:   repository.NewPermissionRepository(),
	}
}

// punishRequest is the JSON body for issuing a punishment.
type punishRequest struct {
	Type          string `json:"type" binding:"required"`
	Reason        string `json:"reason"`
	PermissionKey string `json:"permission_key"`
	// DurationMinutes applies to temp_ban and must be positive for that type.
	DurationMinutes int64 `json:"duration_minutes"`
}

// Punish records a moderation action on a user and applies its side effects.
// The gateway enforces the user.punish permission on this route.
func (h *PunishmentHandler) Punish(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil || userID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user id"})
		return
	}
	operatorID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req punishRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	target, err := h.userRepo.GetByID(userID)
	if err != nil {
		logger.Log.Error("failed to get target user", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to punish user"})
		return
	}
	if target == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}

	ptype := model.PunishmentType(req.Type)
	switch ptype {
	case model.PunishWarning, model.PunishDemerit:
		// history only
	case model.PunishRevoke, model.PunishRestore:
		if req.PermissionKey == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "permission_key is required"})
			return
		}
	case model.PunishTempBan:
		if req.DurationMinutes <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "duration_minutes is required for temp_ban"})
			return
		}
	case model.PunishBan, model.PunishUnban:
		// no extra params
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown punishment type"})
		return
	}

	var expiresAt *time.Time
	if ptype == model.PunishTempBan {
		t := time.Now().Add(time.Duration(req.DurationMinutes) * time.Minute)
		expiresAt = &t
	}

	id, err := h.punishRepo.Record(userID, operatorID, ptype, req.PermissionKey, req.Reason, expiresAt)
	if err != nil {
		logger.Log.Error("failed to record punishment", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to punish user"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"punishment_id": id,
		"type":          string(ptype),
		"expires_at":    expiresAt,
	})
}

// ListPunishments returns a user's moderation history.
func (h *PunishmentHandler) ListPunishments(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil || userID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user id"})
		return
	}

	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	punishments, err := h.punishRepo.ListByUser(userID, limit)
	if err != nil {
		logger.Log.Error("failed to list punishments", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list punishments"})
		return
	}
	bans, err := h.punishRepo.ListPermissionBans(userID)
	if err != nil {
		logger.Log.Error("failed to list permission bans", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list punishments"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"punishments": punishments, "permission_bans": bans})
}