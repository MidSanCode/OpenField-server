package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/events"
	"github.com/openfield/server/pkg/logger"
	"github.com/openfield/server/pkg/middleware"
	"github.com/openfield/server/pkg/model"
	"github.com/openfield/server/pkg/repository"
)

// allowedReactions is the fixed set of post reactions clients may use.
var allowedReactions = map[string]bool{
	"like": true, "dislike": true, "love": true,
	"haha": true, "wow": true, "sad": true, "angry": true,
}

// Post visibility values.
const (
	visibilityPublic  = "public"
	visibilityLogin   = "login"
	visibilityFriends = "friends"
	visibilityPrivate = "private"
)

// allowedVisibilities is the fixed set of post visibility values.
var allowedVisibilities = map[string]bool{
	visibilityPublic:  true,
	visibilityLogin:   true,
	visibilityFriends: true,
	visibilityPrivate: true,
}

// canViewPost reports whether the viewer may see a post based on its
// visibility: public for everyone, login for any authenticated user, friends
// for mutual follows, private for the author only.
func canViewPost(post *model.Post, viewerID int64) bool {
	switch post.Visibility {
	case visibilityPrivate:
		return viewerID > 0 && viewerID == post.UserID
	case visibilityFriends:
		if viewerID <= 0 {
			return false
		}
		if viewerID == post.UserID {
			return true
		}
		mutual, err := repository.NewFollowRepository().AreMutual(viewerID, post.UserID)
		return err == nil && mutual
	case visibilityLogin:
		return viewerID > 0
	default:
		return true
	}
}

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
		Visibility    string  `json:"visibility"`
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
	if req.Visibility == "" {
		req.Visibility = visibilityPublic
	}
	if !allowedVisibilities[req.Visibility] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown visibility"})
		return
	}

	post, err := h.postRepo.Create(userID, req.Content, req.Visibility, req.AttachmentIDs)
	if err != nil {
		logger.Log.Error("failed to create post", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create post"})
		return
	}

	// New posts are broadcast to all connected clients (public feed).
	events.Publish(c.Request.Context(), events.PostCreated, nil, post)

	c.JSON(http.StatusCreated, post)
}

// GetPost retrieves a single post by ID, recording a view.
func (h *PostHandler) GetPost(c *gin.Context) {
	postID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid post ID"})
		return
	}

	viewer := requesterID(c)
	post, err := h.postRepo.GetByIDWithViewer(postID, viewer)
	if err != nil {
		logger.Log.Error("failed to get post", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get post"})
		return
	}
	if post == nil || !canViewPost(post, viewer) {
		c.JSON(http.StatusNotFound, gin.H{"error": "post not found"})
		return
	}

	// Fire-and-forget view tracking: the read succeeds regardless.
	if err := h.postRepo.RecordView(postID, viewerKey(c)); err != nil {
		logger.Log.Warn("failed to record post view", "error", err, "post_id", postID)
	}

	c.JSON(http.StatusOK, post)
}

// requesterID returns the authenticated user id forwarded by the gateway, or 0
// for anonymous reads.
func requesterID(c *gin.Context) int64 {
	if id, ok := middleware.GetUserID(c); ok {
		return id
	}
	if v := c.GetHeader(middleware.UserIDHeader); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			return id
		}
	}
	return 0
}

// viewerKey builds a stable per-viewer key: "u:<id>" for authenticated users,
// otherwise "ip:<client ip>" for anonymous visitors.
func viewerKey(c *gin.Context) string {
	if id := requesterID(c); id > 0 {
		return "u:" + strconv.FormatInt(id, 10)
	}
	return "ip:" + c.ClientIP()
}

// ListPosts retrieves paginated posts, optionally filtered by a search query.
func (h *PostHandler) ListPosts(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	query := strings.TrimSpace(c.Query("q"))
	viewer := requesterID(c)

	var (
		posts []model.Post
		err   error
	)
	if query != "" {
		posts, err = h.postRepo.Search(query, page, limit, viewer)
	} else {
		posts, err = h.postRepo.List(page, limit, viewer)
	}
	if err != nil {
		logger.Log.Error("failed to list posts", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list posts"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"posts": posts,
		"page":  page,
		"limit": limit,
		"query": query,
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

	posts, err := h.postRepo.ListByUser(userID, page, limit, requesterID(c))
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

// ListFavoritePosts retrieves the current user's favorited posts.
func (h *PostHandler) ListFavoritePosts(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	targetID, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user ID"})
		return
	}
	if targetID != userID {
		c.JSON(http.StatusForbidden, gin.H{"error": "cannot view another user's favorites"})
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))

	posts, err := h.postRepo.ListFavoritePosts(userID, page, limit)
	if err != nil {
		logger.Log.Error("failed to list favorite posts", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list favorite posts"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"posts": posts,
		"page":  page,
		"limit": limit,
	})
}

// ListFavoriteReplies retrieves the current user's favorited replies.
func (h *PostHandler) ListFavoriteReplies(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	targetID, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user ID"})
		return
	}
	if targetID != userID {
		c.JSON(http.StatusForbidden, gin.H{"error": "cannot view another user's favorites"})
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))

	replies, err := h.replyRepo.ListFavoriteReplies(userID, page, limit)
	if err != nil {
		logger.Log.Error("failed to list favorite replies", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list favorite replies"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"replies": replies,
		"page":    page,
		"limit":   limit,
	})
}

// FavoritePost marks the current user's favorite on a post.
func (h *PostHandler) FavoritePost(c *gin.Context) {
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

	post, err := h.postRepo.GetByIDWithViewer(postID, userID)
	if err != nil {
		logger.Log.Error("failed to get post", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to favorite post"})
		return
	}
	if post == nil || !canViewPost(post, userID) {
		c.JSON(http.StatusNotFound, gin.H{"error": "post not found"})
		return
	}

	if err := h.postRepo.FavoritePost(postID, userID); err != nil {
		logger.Log.Error("failed to favorite post", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to favorite post"})
		return
	}

	post, err = h.postRepo.GetByIDWithViewer(postID, userID)
	if err != nil || post == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load post"})
		return
	}
	c.JSON(http.StatusOK, post)
}

// UnfavoritePost removes the current user's favorite from a post.
func (h *PostHandler) UnfavoritePost(c *gin.Context) {
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

	if err := h.postRepo.UnfavoritePost(postID, userID); err != nil {
		logger.Log.Error("failed to unfavorite post", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to unfavorite post"})
		return
	}

	post, err := h.postRepo.GetByIDWithViewer(postID, userID)
	if err != nil || post == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load post"})
		return
	}
	c.JSON(http.StatusOK, post)
}

// FavoriteReply marks the current user's favorite on a reply.
func (h *PostHandler) FavoriteReply(c *gin.Context) {
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
	replyID, err := strconv.ParseInt(c.Param("reply_id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid reply ID"})
		return
	}

	reply, err := h.replyRepo.GetByID(replyID)
	if err != nil {
		logger.Log.Error("failed to get reply", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to favorite reply"})
		return
	}
	if reply == nil || reply.PostID != postID {
		c.JSON(http.StatusNotFound, gin.H{"error": "reply not found"})
		return
	}

	if err := h.replyRepo.FavoriteReply(replyID, userID); err != nil {
		logger.Log.Error("failed to favorite reply", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to favorite reply"})
		return
	}

	reply, err = h.replyRepo.GetByIDWithViewer(replyID, userID)
	if err != nil || reply == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load reply"})
		return
	}
	c.JSON(http.StatusOK, reply)
}

// UnfavoriteReply removes the current user's favorite from a reply.
func (h *PostHandler) UnfavoriteReply(c *gin.Context) {
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
	replyID, err := strconv.ParseInt(c.Param("reply_id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid reply ID"})
		return
	}

	reply, err := h.replyRepo.GetByID(replyID)
	if err != nil {
		logger.Log.Error("failed to get reply", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to unfavorite reply"})
		return
	}
	if reply == nil || reply.PostID != postID {
		c.JSON(http.StatusNotFound, gin.H{"error": "reply not found"})
		return
	}

	if err := h.replyRepo.UnfavoriteReply(replyID, userID); err != nil {
		logger.Log.Error("failed to unfavorite reply", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to unfavorite reply"})
		return
	}

	reply, err = h.replyRepo.GetByIDWithViewer(replyID, userID)
	if err != nil || reply == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load reply"})
		return
	}
	c.JSON(http.StatusOK, reply)
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
		Visibility    string  `json:"visibility"`
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
	if req.Visibility == "" {
		req.Visibility = visibilityPublic
	}
	if !allowedVisibilities[req.Visibility] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown visibility"})
		return
	}

	post, err := h.postRepo.Update(postID, userID, req.Content, req.Visibility, req.AttachmentIDs)
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
	if post == nil || !canViewPost(post, userID) {
		c.JSON(http.StatusNotFound, gin.H{"error": "post not found"})
		return
	}

	var req struct {
		Content       string  `json:"content" binding:"required"`
		ParentID      *int64  `json:"parent_id"`
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

	reply, err := h.replyRepo.Create(postID, userID, req.Content, req.ParentID, req.AttachmentIDs)
	if err != nil {
		logger.Log.Error("failed to create reply", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create reply"})
		return
	}

	h.notifyReplyCreated(c.Request.Context(), reply, post.UserID)

	c.JSON(http.StatusCreated, reply)
}

// notifyReplyCreated pushes a new-reply event to the post author and, for
// nested replies, the author of the parent reply (excluding the replier).
func (h *PostHandler) notifyReplyCreated(ctx context.Context, reply interface{}, postAuthorID int64) {
	recipients := []int64{postAuthorID}
	if r, ok := reply.(*model.PostReply); ok {
		if r.ParentID != nil {
			parent, err := h.replyRepo.GetByID(*r.ParentID)
			if err == nil && parent != nil && parent.UserID != postAuthorID {
				recipients = append(recipients, parent.UserID)
			}
		}
	}
	events.Publish(ctx, events.ReplyCreated, recipients, reply)
}

// ListReplies retrieves replies for a post.
func (h *PostHandler) ListReplies(c *gin.Context) {
	postID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid post ID"})
		return
	}

	viewer := requesterID(c)
	post, err := h.postRepo.GetByID(postID)
	if err != nil {
		logger.Log.Error("failed to get post", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list replies"})
		return
	}
	if post == nil || !canViewPost(post, viewer) {
		c.JSON(http.StatusNotFound, gin.H{"error": "post not found"})
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))

	replies, err := h.replyRepo.ListByPost(postID, page, limit, viewer)
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

	reply, err := h.replyRepo.Update(replyID, userID, req.Content, req.AttachmentIDs)
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

// SetPostReaction upserts the current user's reaction on a post.
func (h *PostHandler) SetPostReaction(c *gin.Context) {
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
		Reaction string `json:"reaction" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if !allowedReactions[req.Reaction] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown reaction"})
		return
	}

	if err := h.postRepo.SetReaction(postID, userID, req.Reaction); err != nil {
		logger.Log.Error("failed to set reaction", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to set reaction"})
		return
	}

	post, err := h.postRepo.GetByIDWithViewer(postID, userID)
	if err != nil || post == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load post"})
		return
	}
	c.JSON(http.StatusOK, post)
}

// RemovePostReaction clears the current user's reaction on a post.
func (h *PostHandler) RemovePostReaction(c *gin.Context) {
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

	if err := h.postRepo.RemoveReaction(postID, userID); err != nil {
		logger.Log.Error("failed to remove reaction", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to remove reaction"})
		return
	}

	post, err := h.postRepo.GetByIDWithViewer(postID, userID)
	if err != nil || post == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load post"})
		return
	}
	c.JSON(http.StatusOK, post)
}
