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

// PostHandler handles post and reply requests.
type PostHandler struct {
	postRepo  *repository.PostRepository
	replyRepo *repository.PostReplyRepository
}

// NewPostHandler creates a new PostHandler.
func NewPostHandler() *PostHandler {
	return &PostHandler{
		postRepo:  repository.NewPostRepository(),
		replyRepo: repository.NewPostReplyRepository(),
	}
}

// CreatePost creates a new post.
func (h *PostHandler) CreatePost(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req struct {
		Content       string  `json:"content" binding:"required"`
		AttachmentIDs []int64 `json:"attachment_ids"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	if req.Content == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "content cannot be empty"})
		return
	}
	if len(req.Content) > 5000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "content exceeds maximum length of 5000"})
		return
	}
	if len(req.AttachmentIDs) > 9 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "too many attachments (max 9)"})
		return
	}

	post, err := h.postRepo.Create(userID, req.Content, req.AttachmentIDs)
	if err != nil {
		logger.Log.Error("failed to create post", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create post"})
		return
	}

	c.JSON(http.StatusCreated, post)
}

// GetPost retrieves a single post by ID.
func (h *PostHandler) GetPost(c *gin.Context) {
	postID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid post ID"})
		return
	}

	post, err := h.postRepo.GetByID(postID)
	if err != nil {
		logger.Log.Error("failed to get post", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get post"})
		return
	}
	if post == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "post not found"})
		return
	}

	c.JSON(http.StatusOK, post)
}

// ListPosts retrieves paginated posts.
func (h *PostHandler) ListPosts(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))

	posts, err := h.postRepo.List(page, limit)
	if err != nil {
		logger.Log.Error("failed to list posts", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list posts"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"posts": posts,
		"page":  page,
		"limit": limit,
	})
}

// ListPostsByUser retrieves a specific user's posts.
func (h *PostHandler) ListPostsByUser(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user ID"})
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))

	posts, err := h.postRepo.ListByUser(userID, page, limit)
	if err != nil {
		logger.Log.Error("failed to list user posts", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list posts"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"posts": posts,
		"page":  page,
		"limit": limit,
	})
}

// DeletePost deletes a post by ID (owner only).
func (h *PostHandler) DeletePost(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	postID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid post ID"})
		return
	}

	err = h.postRepo.Delete(postID, userID)
	if err != nil {
		if errors.Is(err, repository.ErrNoSuchRow) {
			c.JSON(http.StatusForbidden, gin.H{"error": "post not found or you don't have permission to delete it"})
			return
		}
		logger.Log.Error("failed to delete post", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete post"})
		return
	}

	c.Status(http.StatusNoContent)
}

// UpdatePost updates a post's content and attachments (owner only).
func (h *PostHandler) UpdatePost(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	postID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid post ID"})
		return
	}

	var req struct {
		Content       string  `json:"content" binding:"required"`
		AttachmentIDs []int64 `json:"attachment_ids"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	if req.Content == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "content cannot be empty"})
		return
	}
	if len(req.Content) > 5000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "content exceeds maximum length of 5000"})
		return
	}
	if len(req.AttachmentIDs) > 9 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "too many attachments (max 9)"})
		return
	}

	post, err := h.postRepo.Update(postID, userID, req.Content, req.AttachmentIDs)
	if err != nil {
		if errors.Is(err, repository.ErrNoSuchRow) {
			c.JSON(http.StatusForbidden, gin.H{"error": "post not found or you don't have permission to edit it"})
			return
		}
		logger.Log.Error("failed to update post", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update post"})
		return
	}

	c.JSON(http.StatusOK, post)
}

// CreateReply creates a reply on a post.
func (h *PostHandler) CreateReply(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	postID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid post ID"})
		return
	}

	post, err := h.postRepo.GetByID(postID)
	if err != nil {
		logger.Log.Error("failed to get post", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create reply"})
		return
	}
	if post == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "post not found"})
		return
	}

	var req struct {
		Content  string `json:"content" binding:"required"`
		ParentID *int64 `json:"parent_id"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if req.Content == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "content cannot be empty"})
		return
	}
	if len(req.Content) > 5000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "content exceeds maximum length of 5000"})
		return
	}

	reply, err := h.replyRepo.Create(postID, userID, req.Content, req.ParentID)
	if err != nil {
		logger.Log.Error("failed to create reply", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create reply"})
		return
	}

	c.JSON(http.StatusCreated, reply)
}

// ListReplies retrieves replies for a post.
func (h *PostHandler) ListReplies(c *gin.Context) {
	postID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid post ID"})
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))

	replies, err := h.replyRepo.ListByPost(postID, page, limit)
	if err != nil {
		logger.Log.Error("failed to list replies", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list replies"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"replies": replies,
		"page":    page,
		"limit":   limit,
	})
}

// UpdateReply updates a reply's content (owner only).
func (h *PostHandler) UpdateReply(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	replyID, err := strconv.ParseInt(c.Param("reply_id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid reply ID"})
		return
	}

	var req struct {
		Content string `json:"content" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if req.Content == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "content cannot be empty"})
		return
	}

	reply, err := h.replyRepo.Update(replyID, userID, req.Content)
	if err != nil {
		if errors.Is(err, repository.ErrNoSuchRow) {
			c.JSON(http.StatusForbidden, gin.H{"error": "reply not found or you don't have permission to edit it"})
			return
		}
		logger.Log.Error("failed to update reply", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update reply"})
		return
	}

	c.JSON(http.StatusOK, reply)
}

// DeleteReply soft-deletes a reply (owner only).
func (h *PostHandler) DeleteReply(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	replyID, err := strconv.ParseInt(c.Param("reply_id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid reply ID"})
		return
	}

	err = h.replyRepo.Delete(replyID, userID)
	if err != nil {
		if errors.Is(err, repository.ErrNoSuchRow) {
			c.JSON(http.StatusForbidden, gin.H{"error": "reply not found or you don't have permission to delete it"})
			return
		}
		logger.Log.Error("failed to delete reply", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete reply"})
		return
	}

	c.Status(http.StatusNoContent)
}
