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
	"github.com/google/uuid"
	"github.com/openfield/server/pkg/imaging"
	"github.com/openfield/server/pkg/logger"
	"github.com/openfield/server/pkg/middleware"
	"github.com/openfield/server/pkg/repository"
	"github.com/openfield/server/pkg/storage"
)

// maxChunkBytes is the size limit for a single uploaded chunk.
const maxChunkBytes = 8 * 1024 * 1024

// maxTotalChunks bounds the per-session chunk count so a hostile init request
// cannot make completion iterate (or reserve) an unbounded number of objects.
const maxTotalChunks = 10000

// uploadSessionTTL is how long an unfinished session may linger before the
// sweeper deletes it.
const uploadSessionTTL = 24 * time.Hour

// loadOwnedUploadSession fetches a chunked-upload session and verifies it
// belongs to the requesting user. Returns nil when the caller already wrote a
// response.
func (h *AttachmentHandler) loadOwnedUploadSession(c *gin.Context, userID int64, uploadID string) *repository.UploadSession {
	if uploadID == "" || len(uploadID) > 64 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid upload id"})
		return nil
	}
	session, err := repository.GetUploadSession(uploadID)
	if err != nil {
		logger.Log.Error("failed to load upload session", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load upload session"})
		return nil
	}
	if session == nil || session.UserID != userID {
		// A missing session and someone else's session are indistinguishable
		// to the caller on purpose.
		c.JSON(http.StatusNotFound, gin.H{"error": "upload session not found"})
		return nil
	}
	return session
}

// chunkInitRequest starts a chunked upload session.
type chunkInitRequest struct {
	Filename    string `json:"filename" binding:"required"`
	Size        int64  `json:"size" binding:"required,gt=0"`
	TotalChunks int    `json:"total_chunks" binding:"required,gt=0"`
	MimeType    string `json:"mime_type"`
	Visibility  string `json:"visibility"`
}

// chunkCompleteRequest finishes a chunked upload session.
type chunkCompleteRequest struct {
	Filename    string `json:"filename" binding:"required"`
	Size        int64  `json:"size" binding:"required,gt=0"`
	TotalChunks int    `json:"total_chunks" binding:"required,gt=0"`
	MimeType    string `json:"mime_type"`
	Visibility  string `json:"visibility"`
}

// ChunkInit creates a chunked upload session and returns its upload ID.
func (h *AttachmentHandler) ChunkInit(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	if !h.storageAvailable(c) {
		return
	}

	var req chunkInitRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	if req.Size > h.cfg.MaxUploadBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "file too large"})
		return
	}
	if req.TotalChunks > maxTotalChunks {
		c.JSON(http.StatusBadRequest, gin.H{"error": "too many chunks"})
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

	allowed, err := h.checkQuota(c, user, req.Size)
	if err != nil {
		logger.Log.Error("failed to check storage quota", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to check storage quota"})
		return
	}
	if !allowed {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "storage quota exceeded"})
		return
	}

	uploadID := uuid.NewString()
	if err := repository.CreateUploadSession(&repository.UploadSession{
		UploadID:    uploadID,
		UserID:      userID,
		Bucket:      user.StorageBucket,
		TotalChunks: req.TotalChunks,
		SizeBytes:   req.Size,
	}); err != nil {
		logger.Log.Error("failed to create upload session", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start upload"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"upload_id":    uploadID,
		"total_chunks": req.TotalChunks,
	})
}

// ChunkUpload stores a single chunk for the given upload session. The session
// must exist and belong to the requesting user.
func (h *AttachmentHandler) ChunkUpload(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	if !h.storageAvailable(c) {
		return
	}

	uploadID := c.Param("upload_id")
	session := h.loadOwnedUploadSession(c, userID, uploadID)
	if session == nil {
		return
	}
	index, err := strconv.Atoi(c.Param("index"))
	if err != nil || index < 1 || index > session.TotalChunks {
		logger.Log.Warn(
			"chunked upload rejected: invalid chunk index",
			"upload_id", uploadID,
			"user_id", userID,
			"index_param", c.Param("index"),
			"index_parsed", index,
			"session_total_chunks", session.TotalChunks,
			"err", err,
		)
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid chunk index"})
		return
	}

	store := h.store.For(session.Bucket)

	file, _, err := c.Request.FormFile("chunk")
	if err != nil {
		logger.Log.Warn(
			"chunked upload rejected: missing chunk field",
			"upload_id", uploadID,
			"user_id", userID,
			"index", index,
			"content_length", c.Request.ContentLength,
			"content_type", c.Request.Header.Get("Content-Type"),
			"err", err,
		)
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing chunk field"})
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxChunkBytes+1))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read chunk"})
		return
	}
	if int64(len(data)) > maxChunkBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "chunk too large"})
		return
	}

	if err := store.UploadChunk(c.Request.Context(), uploadID, index, bytes.NewReader(data), int64(len(data))); err != nil {
		logger.Log.Error("failed to upload chunk", "error", err, "index", index)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to upload chunk"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"upload_id": uploadID,
		"index":     index,
		"size":      len(data),
	})
}

// ChunkStatus returns which chunks have already been uploaded (for resume).
// The session must belong to the requesting user.
func (h *AttachmentHandler) ChunkStatus(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	uploadID := c.Param("upload_id")
	session := h.loadOwnedUploadSession(c, userID, uploadID)
	if session == nil {
		return
	}
	store := h.store.For(session.Bucket)
	// Stat per expected key instead of listing the prefix: RustFS prefix
	// listings omit freshly written objects, which made resume status lie
	// about which chunks had landed.
	existing, err := store.StatChunks(c.Request.Context(), uploadID, session.TotalChunks)
	if err != nil {
		logger.Log.Error("failed to stat chunks", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list chunks"})
		return
	}

	uploaded := make([]int, 0, len(existing))
	for index := range existing {
		uploaded = append(uploaded, index)
	}
	sortInts(uploaded)

	c.JSON(http.StatusOK, gin.H{
		"upload_id": uploadID,
		"uploaded":  uploaded,
	})
}

// ChunkComplete verifies all chunks, assembles the object, generates a
// thumbnail, creates the attachment record and removes the temp chunks.
// The session must belong to the requesting user and match the declared
// layout; the session row is consumed once the upload finishes (or is found
// to be a duplicate).
func (h *AttachmentHandler) ChunkComplete(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	if !h.storageAvailable(c) {
		return
	}

	uploadID := c.Param("upload_id")
	session := h.loadOwnedUploadSession(c, userID, uploadID)
	if session == nil {
		return
	}
	var req chunkCompleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	store := h.store.For(session.Bucket)

	if req.TotalChunks != session.TotalChunks {
		c.JSON(http.StatusBadRequest, gin.H{"error": "chunk count mismatch"})
		return
	}

	// Verify chunk presence with per-key StatObject, not a prefix listing.
	// RustFS (the configured backend) does not return freshly PutObject-ed
	// objects from prefix listings, so the listing-based check reported all
	// chunks missing while the client had a 200 for every PUT — the root
	// cause of the recurring "100% uploaded but missing chunks" reports.
	// req.TotalChunks was already validated to equal session.TotalChunks.
	existing, err := store.StatChunks(c.Request.Context(), uploadID, req.TotalChunks)
	if err != nil {
		logger.Log.Error("failed to stat chunks", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list chunks"})
		return
	}
	missing := make([]int, 0, req.TotalChunks)
	for i := 1; i <= req.TotalChunks; i++ {
		if _, ok := existing[i]; !ok {
			missing = append(missing, i)
		}
	}
	if len(missing) > 0 {
		// Log which chunks did make it across alongside the missing ones, so
		// a recurring pattern (e.g. "always the first N") is visible in the
		// server logs without having to repro the failed upload on the
		// client. uploadedOnly is computed as existing \ missing but really
		// equals existing keys when the loop above is the only producer.
		uploaded := make([]int, 0, len(existing))
		for idx := range existing {
			uploaded = append(uploaded, idx)
		}
		logger.Log.Warn(
			"chunked upload complete rejected: missing chunks",
			"upload_id", uploadID,
			"user_id", userID,
			"bucket", session.Bucket,
			"total_chunks", req.TotalChunks,
			"uploaded", uploaded,
			"missing", missing,
		)
		c.JSON(http.StatusConflict, gin.H{"error": "missing chunks", "missing": missing})
		return
	}

	contentType := req.MimeType
	if contentType == "" || strings.HasPrefix(contentType, "application/octet-stream") {
		if guessed := mime.TypeByExtension(filepath.Ext(req.Filename)); guessed != "" {
			contentType = guessed
		} else if contentType == "" {
			contentType = "application/octet-stream"
		}
	}
	// Denylist check: active document / script formats are rejected so the
	// public bucket can never serve executable content.
	if !storage.MimeAllowed(contentType) {
		c.JSON(http.StatusUnsupportedMediaType, gin.H{"error": "unsupported file type"})
		return
	}
	visibility := req.Visibility
	if visibility == "" {
		visibility = "public"
	}

	objectKey, url, assembledHash, err := store.AssembleChunks(c.Request.Context(), uploadID, req.TotalChunks, contentType, req.Filename)
	if err != nil {
		logger.Log.Error("failed to assemble chunks", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to assemble chunks"})
		return
	}

	// Read the assembled image once to strip GPS/location metadata and to
	// generate the thumbnail. Replacement is fail-open: when the sanitized copy
	// cannot be stored the original object is kept.
	cleanData := []byte(nil)
	if isImageMime(contentType) && req.Size <= maxStripReadBytes {
		if data, gerr := store.GetBytes(c.Request.Context(), objectKey, maxStripReadBytes); gerr == nil {
			cleanData = imaging.StripImageLocation(data, contentType)
			if !bytes.Equal(cleanData, data) {
				newKey, newURL, uerr := store.Upload(c.Request.Context(), bytes.NewReader(cleanData), int64(len(cleanData)), contentType, req.Filename)
				if uerr == nil {
					_ = store.Delete(c.Request.Context(), objectKey)
					objectKey, url = newKey, newURL
					logger.Log.Info("stripped location metadata from uploaded image", "filename", req.Filename)
				} else {
					logger.Log.Warn("failed to store sanitized image, keeping original", "error", uerr)
					cleanData = data
				}
			}
		} else {
			logger.Log.Debug("skipped location stripping", "error", gerr)
		}
	}

	// Content-hash deduplication. The hash of the bytes actually stored wins:
	// after possible GPS stripping that is the in-memory cleaned copy, otherwise
	// it is the hash computed while assembling the stream.
	finalHash := assembledHash
	if len(cleanData) > 0 {
		finalHash = sha256Hex(cleanData)
	}
	if existing, derr := h.attRepo.GetByHash(finalHash, userID); derr != nil {
		logger.Log.Error("failed to check attachment hash", "error", derr)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to check attachment hash"})
		return
	} else if existing != nil {
		// The assembled bytes duplicate an existing blob: discard the fresh
		// object/thumbnail and reuse the existing link.
		_ = store.Delete(c.Request.Context(), objectKey)
		_ = store.DeleteThumb(c.Request.Context(), objectKey)
		_ = store.DeleteChunks(c.Request.Context(), uploadID)
		_ = repository.DeleteUploadSession(uploadID)
		logger.Log.Info("reused existing attachment by content hash", "hash", finalHash, "attachment_id", existing.ID)
		c.JSON(http.StatusOK, existing)
		return
	}

	thumbURL := ""
	if len(cleanData) > 0 && len(cleanData) <= maxThumbReadBytes {
		if thumb, terr := generateThumbnail(cleanData, contentType); terr == nil && len(thumb) > 0 {
			thumbURL, err = store.UploadThumb(c.Request.Context(), objectKey, bytes.NewReader(thumb), int64(len(thumb)))
			if err != nil {
				logger.Log.Warn("failed to upload thumbnail", "error", err)
			}
		} else if terr != nil {
			logger.Log.Debug("skipped thumbnail generation", "error", terr)
		}
	}

	attSize := req.Size
	if len(cleanData) > 0 {
		attSize = int64(len(cleanData))
	}

	att, err := h.attRepo.Create(userID, objectKey, req.Filename, contentType, attSize, url, thumbURL, visibility, session.Bucket, finalHash)
	if err != nil {
		logger.Log.Error("failed to save attachment", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save attachment"})
		return
	}

	if err := store.DeleteChunks(c.Request.Context(), uploadID); err != nil {
		logger.Log.Warn("failed to delete temp chunks", "error", err)
	}
	if err := repository.DeleteUploadSession(uploadID); err != nil {
		logger.Log.Warn("failed to delete upload session", "error", err)
	}

	c.JSON(http.StatusCreated, att)
}

func sortInts(s []int) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
