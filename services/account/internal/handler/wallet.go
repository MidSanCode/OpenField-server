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
)

// WalletHandler handles wallet balance and transaction requests.
type WalletHandler struct {
	walletRepo *repository.WalletRepository
	userRepo   *repository.UserRepository
}

// NewWalletHandler creates a new WalletHandler.
func NewWalletHandler() *WalletHandler {
	return &WalletHandler{
		walletRepo: repository.NewWalletRepository(),
		userRepo:   repository.NewUserRepository(),
	}
}

// GetMyWallet returns the current user's balance and recent transactions.
func (h *WalletHandler) GetMyWallet(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))

	wallet, err := h.walletRepo.GetWallet(userID)
	if err != nil {
		logger.Log.Error("failed to get wallet", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get wallet"})
		return
	}

	txns, err := h.walletRepo.ListTransactions(userID, page, limit)
	if err != nil {
		logger.Log.Error("failed to list wallet transactions", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list wallet transactions"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"balance":      wallet.Balance,
		"transactions": txns,
		"page":         page,
		"limit":        limit,
	})
}

// AdjustWallet applies a recharge or deduction to a user's wallet. The caller
// must hold the wallet.manage permission (enforced by the gateway). amount is
// positive for recharges and negative for deductions.
func (h *WalletHandler) AdjustWallet(c *gin.Context) {
	operatorID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req struct {
		UserID      int64       `json:"user_id" binding:"required"`
		Amount      model.Cents `json:"amount" binding:"required"`
		Type        string      `json:"type"`
		Description string      `json:"description"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if req.Amount == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "amount must not be zero"})
		return
	}
	if req.UserID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user id"})
		return
	}

	target, err := h.userRepo.GetByID(req.UserID)
	if err != nil {
		logger.Log.Error("failed to get target user", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get user"})
		return
	}
	if target == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}

	if req.Type == "" {
		if req.Amount > 0 {
			req.Type = "recharge"
		} else {
			req.Type = "deduct"
		}
	}

	wallet, txn, err := h.walletRepo.AdjustBalance(req.UserID, req.Amount.Int64(), operatorID, req.Type, req.Description)
	if err != nil {
		if errors.Is(err, repository.ErrInsufficientBalance) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "insufficient balance"})
			return
		}
		logger.Log.Error("failed to adjust balance", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to adjust balance"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"balance":     wallet.Balance,
		"transaction": txn,
	})
}
