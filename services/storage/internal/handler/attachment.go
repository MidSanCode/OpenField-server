package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/config"
	"github.com/openfield/server/pkg/logger"
	"github.com/openfield/server/pkg/middleware"
	"github.com/openfield/server/pkg/repository"
	"github.com/openfield/server/pkg/storage"
)

// AttachmentHandler handles file upload/download.
type AttachmentHandler struct {
	store    *storage.Store
	attRepo  *repository.AttachmentRepository
	userRepo *repository.UserRepository
	cfg      config.StorageConfig
}

// NewAttachmentHandler creates a new AttachmentHandler.
func NewAttachmentHandler(store *storage.Store, cfg config.StorageConfig) *AttachmentHandler {
	return &AttachmentHandler{
		store:    store,
		attRepo:  repository.NewAttachmentRepository(),
		userRepo: repository.NewUserRepository(),
		cfg:      cfg,
	}
}

// checkQuota returns true when the current user may upload size bytes more.
func (h *AttachmentHandler) checkQuota(c *gin.Context, userID int64, size int64) (bool, error) {
	user, err := h.userRepo.GetByID(userID)
	if err != nil {
		return false, err
	}
	if user == nil || user.StorageQuota <= 0 {
		return true, nil
	}
	used, err := h.attRepo.SumSizeByUser(userID)
	if err != nil {
		return false, err
	}
	return used+size <= user.StorageQuota, nil
}

// Upload accepts a multipart file upload, stores it in RustFS, and returns attachment metadata.
func (h *AttachmentHandler) Upload(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing file field"})
		return
	}
	defer file.Close()

	if header.Size > h.cfg.MaxUploadBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "file too large"})
		return
	}

	allowed, err := h.checkQuota(c, userID, header.Size)
	if err != nil {
		logger.Log.Error("failed to check storage quota", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to check storage quota"})
		return
	}
	if !allowed {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "storage quota exceeded"})
		return
	}

	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	visibility := c.PostForm("visibility")
	if visibility == "" {
		visibility = "public"
	}

	objectKey, url, err := h.store.Upload(c.Request.Context(), file, header.Size, contentType, header.Filename)
	if err != nil {
		logger.Log.Error("failed to upload file", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to upload file"})
		return
	}

	att, err := h.attRepo.Create(userID, objectKey, header.Filename, contentType, header.Size, url, visibility)
	if err != nil {
		logger.Log.Error("failed to save attachment", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save attachment"})
		return
	}

	c.JSON(http.StatusCreated, att)
}

// Get retrieves an attachment by ID.
func (h *AttachmentHandler) Get(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid attachment ID"})
		return
	}

	att, err := h.attRepo.GetByID(id)
	if err != nil {
		logger.Log.Error("failed to get attachment", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get attachment"})
		return
	}
	if att == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "attachment not found"})
		return
	}

	c.JSON(http.StatusOK, att)
}

// Delete deletes an attachment owned by the current user (also removes from storage).
func (h *AttachmentHandler) Delete(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid attachment ID"})
		return
	}

	att, err := h.attRepo.GetByID(id)
	if err != nil {
		logger.Log.Error("failed to get attachment", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete attachment"})
		return
	}
	if att == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "attachment not found"})
		return
	}
	if att.UserID != userID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	if err := h.store.Delete(c.Request.Context(), att.ObjectKey); err != nil {
		logger.Log.Error("failed to delete storage object", "error", err)
	}
	if err := h.attRepo.Delete(id); err != nil {
		logger.Log.Error("failed to delete attachment record", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete attachment"})
		return
	}

	c.Status(http.StatusNoContent)
}

// ListByUser lists the current user's attachments.
func (h *AttachmentHandler) ListByUser(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	atts, err := h.attRepo.ListByUser(userID, limit)
	if err != nil {
		logger.Log.Error("failed to list attachments", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list attachments"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"attachments": atts})
}
