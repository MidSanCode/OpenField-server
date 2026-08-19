package handler

import (
	"bytes"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/config"
	"github.com/openfield/server/pkg/imaging"
	"github.com/openfield/server/pkg/logger"
	"github.com/openfield/server/pkg/middleware"
	"github.com/openfield/server/pkg/model"
	"github.com/openfield/server/pkg/repository"
	"github.com/openfield/server/pkg/storage"
)

// maxThumbReadBytes caps the size of an image that gets a generated thumbnail.
const maxThumbReadBytes = 40 * 1024 * 1024

// AttachmentHandler handles file upload/download.
type AttachmentHandler struct {
	store    *storage.Manager
	attRepo  *repository.AttachmentRepository
	userRepo *repository.UserRepository
	cfg      config.StorageConfig
}

// NewAttachmentHandler creates a new AttachmentHandler.
func NewAttachmentHandler(store *storage.Manager, cfg config.StorageConfig) *AttachmentHandler {
	return &AttachmentHandler{
		store:    store,
		attRepo:  repository.NewAttachmentRepository(),
		userRepo: repository.NewUserRepository(),
		cfg:      cfg,
	}
}

// checkQuota returns true when the current user may upload size bytes more.
// Members receive a storage bonus while their membership is active, but only
// while the user is on the default bucket; on non-default buckets they get
// their base quota only. Once a membership expires users revert to their base
// quota, so uploads are denied whenever the effective quota has been exceeded.
func (h *AttachmentHandler) checkQuota(c *gin.Context, user *model.User, size int64) (bool, error) {
	if user == nil {
		return true, nil
	}
	now := time.Now()
	effectiveQuota := user.StorageQuota
	if bucket, ok := h.cfg.BucketByName(user.StorageBucket); ok && bucket.IsDefault {
		effectiveQuota += model.MemberStorageBonusAt(user.MemberLevel, user.MemberExpiresAt, now)
	}
	if effectiveQuota <= 0 {
		return true, nil
	}
	used, err := h.attRepo.SumSizeByUser(user.ID)
	if err != nil {
		return false, err
	}
	return used+size <= effectiveQuota, nil
}

// storageAvailable writes a 503 response when object storage is not configured
// and reports whether uploads are allowed.
func (h *AttachmentHandler) storageAvailable(c *gin.Context) bool {
	if h.store == nil || !h.store.Enabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "storage not configured"})
		return false
	}
	return true
}

// bucketAllowed reports whether the user may upload into the bucket their
// account is currently on. Buckets gated behind a minimum membership level
// reject users whose active membership is below it (e.g. a user downgraded or
// their membership expired after switching).
func (h *AttachmentHandler) bucketAllowed(b config.StorageBucketConfig, user *model.User, now time.Time) bool {
	if b.MinMemberLevel <= 0 {
		return true
	}
	if user == nil {
		return false
	}
	return model.MembershipActive(user.MemberLevel, user.MemberExpiresAt, now) && user.MemberLevel >= b.MinMemberLevel
}

// Upload accepts a multipart file upload, stores it in RustFS, and returns attachment metadata.
func (h *AttachmentHandler) Upload(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	if !h.storageAvailable(c) {
		return
	}

	user, err := h.userRepo.GetByID(userID)
	if err != nil {
		logger.Log.Error("failed to load user", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load user"})
		return
	}

	if bucket, ok := h.cfg.BucketByName(user.StorageBucket); !ok || !h.bucketAllowed(bucket, user, time.Now()) {
		c.JSON(http.StatusForbidden, gin.H{"error": "storage bucket requires higher membership level"})
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

	allowed, err := h.checkQuota(c, user, header.Size)
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
	if contentType == "" || strings.HasPrefix(contentType, "application/octet-stream") {
		if guessed := mime.TypeByExtension(filepath.Ext(header.Filename)); guessed != "" {
			contentType = guessed
		} else if contentType == "" {
			contentType = "application/octet-stream"
		}
	}

	visibility := c.PostForm("visibility")
	if visibility == "" {
		visibility = "public"
	}

	data, err := io.ReadAll(file)
	if err != nil {
		logger.Log.Error("failed to read upload", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read upload"})
		return
	}

	// Strip GPS / location metadata from images before storing them. The other
	// metadata (orientation, camera model, ...) is preserved. Fail-open: when
	// sanitization cannot run the file is stored as-is.
	if len(data) <= maxStripReadBytes {
		if clean := imaging.StripImageLocation(data, contentType); !bytes.Equal(clean, data) {
			data = clean
			logger.Log.Info("stripped location metadata from uploaded image", "filename", header.Filename)
		}
	}

	store := h.store.For(user.StorageBucket)
	objectKey, url, err := store.Upload(c.Request.Context(), bytes.NewReader(data), int64(len(data)), contentType, header.Filename)
	if err != nil {
		logger.Log.Error("failed to upload file", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to upload file"})
		return
	}

	// Generate a compressed thumbnail for images so feeds never download the full
	// original. Failures are non-fatal: the attachment still works without one.
	thumbURL := ""
	if isImageMime(contentType) && len(data) <= maxThumbReadBytes {
		if thumb, terr := generateThumbnail(data, contentType); terr == nil && len(thumb) > 0 {
			thumbURL, err = store.UploadThumb(c.Request.Context(), objectKey, bytes.NewReader(thumb), int64(len(thumb)))
			if err != nil {
				logger.Log.Warn("failed to upload thumbnail", "error", err)
			}
		} else if terr != nil {
			logger.Log.Debug("skipped thumbnail generation", "error", terr)
		}
	}

	att, err := h.attRepo.Create(userID, objectKey, header.Filename, contentType, int64(len(data)), url, thumbURL, visibility, user.StorageBucket)
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

	store := h.store.For(att.Bucket)
	if err := store.Delete(c.Request.Context(), att.ObjectKey); err != nil {
		logger.Log.Error("failed to delete storage object", "error", err)
	}
	if err := store.DeleteThumb(c.Request.Context(), att.ObjectKey); err != nil {
		logger.Log.Error("failed to delete storage thumbnail", "error", err)
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
