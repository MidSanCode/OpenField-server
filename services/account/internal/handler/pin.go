package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/logger"
	"github.com/openfield/server/pkg/middleware"
	"github.com/openfield/server/pkg/repository"
	"github.com/openfield/server/pkg/security"
)

// PinHandler handles setting and verifying the user's payment PIN.
type PinHandler struct {
	userRepo *repository.UserRepository
}

// NewPinHandler creates a new PinHandler.
func NewPinHandler() *PinHandler {
	return &PinHandler{userRepo: repository.NewUserRepository()}
}

// SetPin sets (or changes) the user's 6-digit payment PIN. The payment PIN is
// used only to authorize outgoing payments; it never replaces the login
// password.
func (h *PinHandler) SetPin(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req struct {
		Pin string `json:"pin" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "pin is required"})
		return
	}
	if !security.ValidPin(req.Pin) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "pin must be 6 digits"})
		return
	}

	hash, err := security.HashPin(req.Pin)
	if err != nil {
		logger.Log.Error("failed to hash pin", "error", err, "user_id", userID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to set pin"})
		return
	}
	if err := h.userRepo.SetPinHash(userID, hash); err != nil {
		logger.Log.Error("failed to save pin hash", "error", err, "user_id", userID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to set pin"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"set": true, "has_pin": true})
}

// ChangePin replaces an existing payment PIN. The current PIN must be
// presented (and matches against the same per-user failure budget as other
// PIN operations), so a stolen session alone cannot take over payments.
func (h *PinHandler) ChangePin(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req struct {
		OldPin string `json:"old_pin" binding:"required"`
		Pin    string `json:"pin" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "old_pin and pin are required"})
		return
	}
	if !security.ValidPin(req.Pin) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "pin must be 6 digits"})
		return
	}

	pinKey := pinAttemptKey(userID)
	if retry := pinLimiter.RetryAfter(pinKey); retry > 0 {
		lockedResponse(c, retry)
		return
	}
	currentHash, err := h.userRepo.GetPinHash(userID)
	if err != nil {
		logger.Log.Error("failed to load pin hash", "error", err, "user_id", userID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to change pin"})
		return
	}
	if currentHash == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "pin not set"})
		return
	}
	if !security.VerifyPin(req.OldPin, currentHash) {
		pinLimiter.Fail(pinKey)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid pin"})
		return
	}
	pinLimiter.Reset(pinKey)

	newHash, err := security.HashPin(req.Pin)
	if err != nil {
		logger.Log.Error("failed to hash pin", "error", err, "user_id", userID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to change pin"})
		return
	}
	if err := h.userRepo.SetPinHash(userID, newHash); err != nil {
		logger.Log.Error("failed to save pin hash", "error", err, "user_id", userID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to change pin"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"set": true, "has_pin": true})
}

// VerifyPin checks a candidate PIN against the stored hash. The client uses it
// to authorize a payment without guessing server-side state.
func (h *PinHandler) VerifyPin(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	// The 6-digit PIN space is small: lock the account out after repeated
	// wrong entries instead of letting attackers grind through it.
	pinKey := pinAttemptKey(userID)
	if retry := pinLimiter.RetryAfter(pinKey); retry > 0 {
		lockedResponse(c, retry)
		return
	}

	var req struct {
		Pin string `json:"pin" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "pin is required"})
		return
	}

	hash, err := h.userRepo.GetPinHash(userID)
	if err != nil {
		logger.Log.Error("failed to load pin hash", "error", err, "user_id", userID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to verify pin"})
		return
	}
	if hash == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "pin not set"})
		return
	}
	valid := security.VerifyPin(req.Pin, hash)
	if !valid {
		pinLimiter.Fail(pinKey)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid pin"})
		return
	}
	pinLimiter.Reset(pinKey)
	c.JSON(http.StatusOK, gin.H{"valid": true})
}
