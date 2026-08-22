package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/logger"
	"github.com/openfield/server/pkg/middleware"
	"github.com/openfield/server/pkg/model"
	"github.com/openfield/server/pkg/repository"
	"github.com/openfield/server/pkg/security"
)

// TransferHandler handles user-to-user currency transfers.
type TransferHandler struct {
	transferRepo *repository.TransferRepository
	userRepo     *repository.UserRepository
}

// NewTransferHandler creates a new TransferHandler.
func NewTransferHandler() *TransferHandler {
	return &TransferHandler{
		transferRepo: repository.NewTransferRepository(),
		userRepo:     repository.NewUserRepository(),
	}
}

// CreateTransfer sends currency to another (any valid) user. The amount is held
// from the sender and only credited to the recipient on acceptance.
func (h *TransferHandler) CreateTransfer(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req struct {
		Recipient int64       `json:"recipient_id" binding:"required"`
		Amount    model.Cents `json:"amount" binding:"required"`
		Note      string      `json:"note"`
		Pin       string      `json:"pin"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if req.Amount <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "amount must be positive"})
		return
	}

	// Payment PIN authorizes outgoing transfers. A user without a PIN must set
	// one first (the client shows the set-up prompt on the first payment).
	// Wrong entries share the per-user PIN failure budget with /pin/verify.
	pinKey := pinAttemptKey(userID)
	if retry := pinLimiter.RetryAfter(pinKey); retry > 0 {
		lockedResponse(c, retry)
		return
	}
	pinHash, err := h.userRepo.GetPinHash(userID)
	if err != nil {
		logger.Log.Error("failed to load sender pin", "error", err, "user_id", userID)
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

	recipient, err := h.userRepo.GetByID(req.Recipient)
	if err != nil {
		logger.Log.Error("failed to load recipient", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load recipient"})
		return
	}
	if recipient == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "recipient not found"})
		return
	}
	if recipient.ID == userID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot transfer to yourself"})
		return
	}

	transfer, err := h.transferRepo.Create(userID, recipient.ID, req.Amount.Int64(), req.Note)
	if err != nil {
		if errors.Is(err, repository.ErrInsufficientBalance) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "insufficient balance"})
			return
		}
		logger.Log.Error("failed to create transfer", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create transfer"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"transfer": transfer})
}

// AcceptTransfer credits the recipient when a pending transfer is accepted.
func (h *TransferHandler) AcceptTransfer(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid transfer id"})
		return
	}
	transfer, err := h.transferRepo.Accept(id, userID)
	if err != nil {
		h.respondTransferError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"transfer": transfer})
}

// DeclineTransfer refunds the sender when a pending transfer is declined.
func (h *TransferHandler) DeclineTransfer(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid transfer id"})
		return
	}
	transfer, err := h.transferRepo.Decline(id, userID)
	if err != nil {
		h.respondTransferError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"transfer": transfer})
}

// ListTransfers returns the user's incoming or outgoing transfers.
func (h *TransferHandler) ListTransfers(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	direction := c.DefaultQuery("direction", "incoming")

	var (
		transfers []model.Transfer
		err       error
	)
	if direction == "outgoing" {
		transfers, err = h.transferRepo.ListOutgoing(userID, page, limit)
	} else {
		transfers, err = h.transferRepo.ListIncoming(userID, page, limit)
	}
	if err != nil {
		logger.Log.Error("failed to list transfers", "error", err, "user_id", userID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list transfers"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"transfers": transfers, "page": page, "limit": limit, "direction": direction})
}

func (h *TransferHandler) respondTransferError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, repository.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "transfer not found"})
	case errors.Is(err, repository.ErrTransferUnauthorized):
		c.JSON(http.StatusForbidden, gin.H{"error": "not the transfer recipient"})
	case errors.Is(err, repository.ErrTransferNotPending):
		c.JSON(http.StatusConflict, gin.H{"error": "transfer already settled"})
	default:
		logger.Log.Error("transfer operation failed", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "transfer operation failed"})
	}
}
