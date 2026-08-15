package handler

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/logger"
	"github.com/openfield/server/pkg/middleware"
	"github.com/openfield/server/pkg/repository"
	"github.com/openfield/server/pkg/security"
)

// MembershipHandler handles the membership catalog, wallet purchases and admin
// grants.
type MembershipHandler struct {
	membershipRepo *repository.MembershipRepository
	userRepo       *repository.UserRepository
}

// NewMembershipHandler creates a new MembershipHandler.
func NewMembershipHandler() *MembershipHandler {
	return &MembershipHandler{
		membershipRepo: repository.NewMembershipRepository(),
		userRepo:       repository.NewUserRepository(),
	}
}

// GetMembership returns the authenticated user's membership state plus the
// purchaseable catalog.
func (h *MembershipHandler) GetMembership(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	status, err := h.membershipRepo.GetForUser(userID, time.Now())
	if err != nil {
		logger.Log.Error("failed to load membership", "error", err, "user_id", userID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load membership"})
		return
	}
	c.JSON(http.StatusOK, status)
}

// Purchase buys a membership tier using wallet coins, authorized by the payment
// PIN (set-up enforced client-side via the same flow as transfers).
func (h *MembershipHandler) Purchase(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var req struct {
		Level int64  `json:"level" binding:"required"`
		Pin   string `json:"pin" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "level and pin are required"})
		return
	}
	if req.Level < 1 || req.Level > 4 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid membership level"})
		return
	}

	pinHash, err := h.userRepo.GetPinHash(userID)
	if err != nil {
		logger.Log.Error("failed to load payment pin", "error", err, "user_id", userID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load payment pin"})
		return
	}
	if pinHash == "" {
		c.JSON(http.StatusForbidden, gin.H{"error": "payment pin not set"})
		return
	}
	if !security.VerifyPin(req.Pin, pinHash) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid payment pin"})
		return
	}

	status, err := h.membershipRepo.Purchase(userID, req.Level, time.Now())
	if err != nil {
		if errors.Is(err, repository.ErrInsufficientBalance) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "insufficient balance"})
			return
		}
		if errors.Is(err, repository.ErrInvalidMemberLevel) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid membership level"})
			return
		}
		logger.Log.Error("failed to purchase membership", "error", err, "user_id", userID, "level", req.Level)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to purchase membership"})
		return
	}
	c.JSON(http.StatusOK, status)
}

// Grant applies a membership level directly to a user, skipping the wallet. An
// admin-only endpoint: the gateway enforces the user.membership.grant
// permission. Level 0 clears the membership; a positive level expires after
// `days` (defaults to the standard membership duration).
func (h *MembershipHandler) Grant(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user ID"})
		return
	}
	var req struct {
		Level int64 `json:"level" binding:"required"`
		Days  int64 `json:"days"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if err := h.membershipRepo.Grant(userID, req.Level, req.Days, time.Now()); err != nil {
		if errors.Is(err, repository.ErrInvalidMemberLevel) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid membership level"})
			return
		}
		logger.Log.Error("failed to grant membership", "error", err, "user_id", userID, "level", req.Level)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to grant membership"})
		return
	}
	status, err := h.membershipRepo.GetForUser(userID, time.Now())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load membership"})
		return
	}
	c.JSON(http.StatusOK, status)
}
