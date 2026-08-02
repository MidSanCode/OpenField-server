package handler

import (
	"bytes"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/openfield/server/pkg/logger"
	"github.com/openfield/server/pkg/middleware"
)

// maxChunkBytes is the size limit for a single uploaded chunk.
const maxChunkBytes = 8 * 1024 * 1024

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

	var req chunkInitRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	if req.Size > h.cfg.MaxUploadBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "file too large"})
		return
	}

	allowed, err := h.checkQuota(c, userID, req.Size)
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
	c.JSON(http.StatusCreated, gin.H{
		"upload_id":    uploadID,
		"total_chunks": req.TotalChunks,
	})
}

// ChunkUpload stores a single chunk for the given upload session.
func (h *AttachmentHandler) ChunkUpload(c *gin.Context) {
	if _, ok := middleware.GetUserID(c); !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	uploadID := c.Param("upload_id")
	index, err := strconv.Atoi(c.Param("index"))
	if err != nil || index < 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid chunk index"})
		return
	}

	file, _, err := c.Request.FormFile("chunk")
	if err != nil {
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

	if err := h.store.UploadChunk(c.Request.Context(), uploadID, index, bytes.NewReader(data), int64(len(data))); err != nil {
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
func (h *AttachmentHandler) ChunkStatus(c *gin.Context) {
	if _, ok := middleware.GetUserID(c); !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	uploadID := c.Param("upload_id")
	existing, err := h.store.ListChunks(c.Request.Context(), uploadID)
	if err != nil {
		logger.Log.Error("failed to list chunks", "error", err)
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
func (h *AttachmentHandler) ChunkComplete(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	uploadID := c.Param("upload_id")
	var req chunkCompleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	existing, err := h.store.ListChunks(c.Request.Context(), uploadID)
	if err != nil {
		logger.Log.Error("failed to list chunks", "error", err)
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
	visibility := req.Visibility
	if visibility == "" {
		visibility = "public"
	}

	objectKey, url, err := h.store.AssembleChunks(c.Request.Context(), uploadID, req.TotalChunks, contentType, req.Filename)
	if err != nil {
		logger.Log.Error("failed to assemble chunks", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to assemble chunks"})
		return
	}

	thumbURL := ""
	if isImageMime(contentType) && req.Size <= maxThumbReadBytes {
		if data, gerr := h.store.GetBytes(c.Request.Context(), objectKey, maxThumbReadBytes); gerr == nil {
			if thumb, terr := generateThumbnail(data, contentType); terr == nil && len(thumb) > 0 {
				thumbURL, err = h.store.UploadThumb(c.Request.Context(), objectKey, bytes.NewReader(thumb), int64(len(thumb)))
				if err != nil {
					logger.Log.Warn("failed to upload thumbnail", "error", err)
				}
			} else if terr != nil {
				logger.Log.Debug("skipped thumbnail generation", "error", terr)
			}
		} else {
			logger.Log.Warn("failed to read assembled object for thumbnail", "error", gerr)
		}
	}

	att, err := h.attRepo.Create(userID, objectKey, req.Filename, contentType, req.Size, url, thumbURL, visibility)
	if err != nil {
		logger.Log.Error("failed to save attachment", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save attachment"})
		return
	}

	if err := h.store.DeleteChunks(c.Request.Context(), uploadID); err != nil {
		logger.Log.Warn("failed to delete temp chunks", "error", err)
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
