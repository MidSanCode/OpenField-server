package handler

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/minio/minio-go/v7"
	"github.com/openfield/server/pkg/config"
	"github.com/openfield/server/pkg/imaging"
	"github.com/openfield/server/pkg/logger"
	"github.com/openfield/server/pkg/middleware"
	"github.com/openfield/server/pkg/model"
	"github.com/openfield/server/pkg/repository"
	"github.com/openfield/server/pkg/storage"
)

// sha256Hex returns the hex-encoded SHA-256 of the given bytes.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

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

	// Quota is enforced only for genuinely new uploads: reusing a blob the
	// cloud already has (dedupe) must still work when the quota is exhausted,
	// so the check runs after the hash lookup below.
	quotaOK, quotaErr := h.checkQuota(c, user, header.Size)
	if quotaErr != nil {
		logger.Log.Error("failed to check storage quota", "error", quotaErr)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to check storage quota"})
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
	// Denylist check: active document / script formats are rejected so the
	// bucket can never serve executable content; every inert type is accepted.
	if !storage.MimeAllowed(contentType) {
		c.JSON(http.StatusUnsupportedMediaType, gin.H{"error": "unsupported file type"})
		return
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

	// Content-hash deduplication: when the cloud already has an attachment with
	// the exact same stored bytes, reuse the existing link instead of storing a
	// copy. The hash is computed on the final (sanitized) bytes so GPS stripping
	// of images does not defeat the match.
	hash := sha256Hex(data)
	if existing, derr := h.attRepo.GetByHash(hash, userID); derr != nil {
		logger.Log.Error("failed to check attachment hash", "error", derr)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to check attachment hash"})
		return
	} else if existing != nil {
		logger.Log.Info("reused existing attachment by content hash", "hash", hash, "attachment_id", existing.ID)
		c.JSON(http.StatusOK, existing)
		return
	}

	if !quotaOK {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "storage quota exceeded"})
		return
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

	att, err := h.attRepo.Create(userID, objectKey, header.Filename, contentType, int64(len(data)), url, thumbURL, visibility, user.StorageBucket, hash)
	if err != nil {
		logger.Log.Error("failed to save attachment", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save attachment"})
		return
	}

	c.JSON(http.StatusCreated, att)
}

// Get retrieves an attachment by ID.
func (h *AttachmentHandler) Get(c *gin.Context) {
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get attachment"})
		return
	}
	if att == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "attachment not found"})
		return
	}
	// Metadata is visible to the owner, or to any authenticated user when the
	// attachment is public (same rule as by-hash reuse). A 404 keeps foreign
	// private attachments indistinguishable from nonexistent ones.
	if att.UserID != userID && att.Visibility != "public" {
		c.JSON(http.StatusNotFound, gin.H{"error": "attachment not found"})
		return
	}

	c.JSON(http.StatusOK, att)
}

// Reuse looks up an attachment whose content hash matches, so clients can
// skip uploading a file the cloud already has and use the existing link.
func (h *AttachmentHandler) Reuse(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	hash := strings.ToLower(strings.TrimSpace(c.Param("hash")))
	att, err := h.attRepo.GetByHash(hash, userID)
	if err != nil {
		logger.Log.Error("failed to look up attachment by hash", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to look up attachment"})
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

	// Without this guard h.store.For below yields a nil store whenever object
	// storage is not configured, panicking on store.Delete.
	if !h.storageAvailable(c) {
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

// Usage reports the current user's storage statistics: total count and size
// plus a per-bucket breakdown, alongside the effective quota (base quota plus
// the membership bonus on the default bucket).
func (h *AttachmentHandler) Usage(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	total, buckets, err := h.attRepo.UsageByUser(userID)
	if err != nil {
		logger.Log.Error("failed to aggregate usage", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to aggregate usage"})
		return
	}

	resp := gin.H{
		"total_bytes": total.SizeBytes,
		"total_count": total.Count,
		"buckets":     buckets,
	}

	user, uerr := h.userRepo.GetByID(userID)
	if uerr == nil && user != nil {
		now := time.Now()
		base := user.StorageQuota
		bonus := int64(0)
		effective := base
		if bucket, ok := h.cfg.BucketByName(user.StorageBucket); ok && bucket.IsDefault {
			bonus = model.MemberStorageBonusAt(user.MemberLevel, user.MemberExpiresAt, now)
			effective += bonus
		}
		resp["quota"] = gin.H{
			"base_bytes":      base,
			"bonus_bytes":     bonus,
			"effective_bytes": effective,
			"used_bytes":      total.SizeBytes,
		}
	}

	c.JSON(http.StatusOK, resp)
}

// parseByteRange parses a single-range Range header ("bytes=a-b", "bytes=a-",
// "bytes=-suffix") against the object size. Returns (start, end, true) for an
// applicable range or (0, 0, false) when absent/unparseable/unsatisfiable.
func parseByteRange(header string, size int64) (int64, int64, bool) {
	const prefix = "bytes="
	if size <= 0 || !strings.HasPrefix(header, prefix) {
		return 0, 0, false
	}
	spec := strings.TrimSpace(strings.TrimPrefix(header, prefix))
	if strings.ContainsAny(spec, ",/") { // multi-range & unsatisfied forms: serve full
		return 0, 0, false
	}
	dash := strings.Index(spec, "-")
	if dash < 0 {
		return 0, 0, false
	}
	startStr, endStr := spec[:dash], spec[dash+1:]
	var start, end int64
	switch {
	case startStr == "" && endStr == "":
		return 0, 0, false
	case startStr == "": // bytes=-N : final N bytes
		n, err := strconv.ParseInt(endStr, 10, 64)
		if err != nil || n <= 0 {
			return 0, 0, false
		}
		if n > size {
			n = size
		}
		start, end = size-n, size-1
	default:
		s, err := strconv.ParseInt(startStr, 10, 64)
		if err != nil || s < 0 || s >= size {
			return 0, 0, false
		}
		start = s
		if endStr == "" {
			end = size - 1
		} else {
			e, err := strconv.ParseInt(endStr, 10, 64)
			if err != nil {
				return 0, 0, false
			}
			end = e
			if end >= size {
				end = size - 1
			}
		}
	}
	if end < start {
		return 0, 0, false
	}
	return start, end, true
}

// ServeFile streams an object from the bucket through the API (internal proxy
// mode). Path form: GET /api/v1/files/<physical-bucket>/<object-key>. Public:
// read access matches what direct bucket URLs offered before; chunk uploads
// are never exposed. Supports single Range requests so media players can seek.
func (h *AttachmentHandler) ServeFile(c *gin.Context) {
	if !h.storageAvailable(c) {
		return
	}

	parts := strings.SplitN(strings.Trim(c.Param("path"), "/"), "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	bucketName, key := parts[0], parts[1]
	if strings.Contains(bucketName, "..") || strings.Contains(key, "..") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid path"})
		return
	}
	// In-progress upload chunks are internal state, not user-facing files.
	if strings.HasPrefix(key, "chunks/") {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	store := h.store.ForPhysical(bucketName)
	if store == nil || !store.Enabled() {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}

	ctx := c.Request.Context()
	obj, err := store.Open(ctx, key)
	if err != nil {
		logger.Log.Error("failed to open proxied object", "error", err, "bucket", bucketName)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read file"})
		return
	}
	defer obj.Close()
	stat, err := obj.Stat()
	if err != nil {
		// minio resolves lazily: missing objects surface here.
		logger.Log.Warn("proxied object stat failed", "error", err, "bucket", bucketName, "key", key)
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}

	// Object keys embed a fresh UUID, so successful responses are immutable.
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	c.Header("Accept-Ranges", "bytes")
	c.Header("X-Content-Type-Options", "nosniff")
	contentType := stat.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	c.Header("Content-Type", contentType)
	// Types outside the inline-safe whitelist always download instead of
	// rendering: inert binaries (archives, office documents, encrypted
	// containers) must never be interpreted by the browser.
	if !storage.InlineSafeMime(contentType) {
		c.Header("Content-Disposition",
			fmt.Sprintf("attachment; filename*=UTF-8''%s", url.PathEscape(key[strings.LastIndexByte(key, '/')+1:])))
	}

	// Single-range requests (media seeking): re-open with the parsed range.
	// Any failure falls back to serving the full object.
	if start, end, ok := parseByteRange(c.GetHeader("Range"), stat.Size); ok {
		opts := minio.GetObjectOptions{}
		if serr := opts.SetRange(start, end); serr == nil {
			rangedObj, rerr := store.OpenOpts(ctx, key, opts)
			if rerr == nil {
				defer rangedObj.Close()
				c.Header("Content-Range",
					fmt.Sprintf("bytes %d-%d/%d", start, end, stat.Size))
				c.Header("Content-Length", strconv.FormatInt(end-start+1, 10))
				c.Status(http.StatusPartialContent)
				_, _ = io.Copy(c.Writer, rangedObj)
				return
			}
			logger.Log.Warn("range request failed; serving full object",
				"bucket", bucketName, "key", key, "error", rerr)
		}
	}

	c.Header("Content-Length", strconv.FormatInt(stat.Size, 10))
	c.Status(http.StatusOK)
	_, _ = io.Copy(c.Writer, obj)
}
