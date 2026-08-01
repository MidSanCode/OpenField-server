package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/logger"
	"github.com/openfield/server/pkg/middleware"
	"github.com/openfield/server/pkg/repository"
)

// ConsentHandler handles consent request endpoints.
type ConsentHandler struct {
	consentRepo *repository.ConsentRequestRepository
}

// NewConsentHandler creates a new ConsentHandler.
func NewConsentHandler() *ConsentHandler {
	return &ConsentHandler{
		consentRepo: repository.NewConsentRequestRepository(),
	}
}

// ListPending lists consent requests targeting the current user.
func (h *ConsentHandler) ListPending(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	reqs, err := h.consentRepo.ListPendingForUser(userID)
	if err != nil {
		logger.Log.Error("failed to list consent requests", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list requests"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"requests": reqs})
}

// Accept accepts a consent request.
func (h *ConsentHandler) Accept(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	reqID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request ID"})
		return
	}

	req, err := h.consentRepo.GetByID(reqID)
	if err != nil {
		logger.Log.Error("failed to get request", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to accept request"})
		return
	}
	if req == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "request not found"})
		return
	}
	if req.TargetUserID != userID {
		c.JSON(http.StatusForbidden, gin.H{"error": "you cannot accept this request"})
		return
	}

	conv, err := h.consentRepo.Accept(reqID)
	if err != nil {
		if errors.Is(err, repository.ErrAlreadyHandled) {
			c.JSON(http.StatusConflict, gin.H{"error": "request already handled"})
			return
		}
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "request not found"})
			return
		}
		logger.Log.Error("failed to accept request", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to accept request"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "request accepted", "conversation": conv})
}

// Decline declines a consent request.
func (h *ConsentHandler) Decline(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	reqID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request ID"})
		return
	}

	req, err := h.consentRepo.GetByID(reqID)
	if err != nil {
		logger.Log.Error("failed to get request", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to decline request"})
		return
	}
	if req == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "request not found"})
		return
	}
	if req.TargetUserID != userID {
		c.JSON(http.StatusForbidden, gin.H{"error": "you cannot decline this request"})
		return
	}

	err = h.consentRepo.Decline(reqID)
	if err != nil {
		if errors.Is(err, repository.ErrAlreadyHandled) {
			c.JSON(http.StatusConflict, gin.H{"error": "request already handled"})
			return
		}
		logger.Log.Error("failed to decline request", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to decline request"})
		return
	}

	c.Status(http.StatusNoContent)
}
