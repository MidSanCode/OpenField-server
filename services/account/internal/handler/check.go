package handler

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/logger"
	"github.com/openfield/server/pkg/middleware"
	"github.com/openfield/server/pkg/model"
	"github.com/openfield/server/pkg/repository"
	"github.com/openfield/server/pkg/security"
)

// CheckHandler handles check (red packet) endpoints.
type CheckHandler struct {
	checkRepo *repository.CheckRepository
	userRepo  *repository.UserRepository
}

// NewCheckHandler creates a new CheckHandler.
func NewCheckHandler() *CheckHandler {
	return &CheckHandler{
		checkRepo: repository.NewCheckRepository(),
		userRepo:  repository.NewUserRepository(),
	}
}

// Create escrows money into a new check. The payment PIN authorizes the
// charge, mirroring transfers and tips.
func (h *CheckHandler) Create(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req struct {
		Amount         model.Cents `json:"amount" binding:"required"`
		Shares         int64       `json:"shares" binding:"required"`
		Mode           string      `json:"mode"`
		ExpiresInHours int         `json:"expires_in_hours"`
		Pin            string      `json:"pin"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	mode := req.Mode
	if mode == "" {
		mode = model.CheckModeRandom
	}
	ttl := time.Duration(req.ExpiresInHours) * time.Hour
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}

	pinKey := pinAttemptKey(userID)
	if retry := pinLimiter.RetryAfter(pinKey); retry > 0 {
		lockedResponse(c, retry)
		return
	}
	pinHash, err := h.userRepo.GetPinHash(userID)
	if err != nil {
		logger.Log.Error("failed to load pin", "error", err, "user_id", userID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load payment pin"})
		return
	}
	if pinHash == "" {
		c.JSON(http.StatusForbidden, gin.H{"error": "payment pin not set"})
		return
	}
	if !security.VerifyPin(req.Pin, pinHash) {
		pinLimiter.Fail(pinKey)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid payment pin"})
		return
	}
	pinLimiter.Reset(pinKey)

	check, err := h.checkRepo.Create(userID, req.Amount.Int64(), req.Shares, mode, ttl)
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrInvalidCheck):
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid check parameters"})
		case errors.Is(err, repository.ErrInsufficientBalance):
			c.JSON(http.StatusBadRequest, gin.H{"error": "insufficient balance"})
		default:
			logger.Log.Error("failed to create check", "error", err, "user_id", userID)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create check"})
		}
		return
	}
	c.JSON(http.StatusCreated, gin.H{"check": check})
}

// Get returns one check with claims and the viewer's own state.
func (h *CheckHandler) Get(c *gin.Context) {
	viewerID, _ := middleware.GetUserID(c)
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid check id"})
		return
	}
	check, err := h.checkRepo.GetByID(id, viewerID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "check not found"})
			return
		}
		logger.Log.Error("failed to load check", "error", err, "check_id", id)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load check"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"check": check})
}

// Claim pays the caller one share of the check.
func (h *CheckHandler) Claim(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid check id"})
		return
	}
	claim, err := h.checkRepo.Claim(id, userID)
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "check not found"})
		case errors.Is(err, repository.ErrCheckSettled):
			c.JSON(http.StatusConflict, gin.H{"error": "check is no longer claimable"})
		case errors.Is(err, repository.ErrCheckAlreadyClaimed):
			c.JSON(http.StatusConflict, gin.H{"error": "you have already claimed this check"})
		case errors.Is(err, repository.ErrInsufficientBalance):
			// Cannot happen for payouts; treated as a server fault.
			fallthrough
		default:
			logger.Log.Error("failed to claim check", "error", err, "check_id", id, "user_id", userID)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to claim check"})
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"claim": claim})
}
