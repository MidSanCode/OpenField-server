package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/events"
	"github.com/openfield/server/pkg/logger"
	"github.com/openfield/server/pkg/middleware"
	"github.com/openfield/server/pkg/model"
	"github.com/openfield/server/pkg/repository"
	"github.com/openfield/server/pkg/security"
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

// postRecipients computes the realtime push audience for a newly created post
// based on its visibility. A nil slice means "broadcast to every connected
// client" (the WebSocket hub only serves authenticated users).
func postRecipients(authorID int64, visibility string) []int64 {
	switch visibility {
	case visibilityPrivate:
		return []int64{authorID}
	case visibilityFriends:
		friendIDs, err := repository.NewFollowRepository().MutualFollowerIDs(authorID)
		if err != nil {
			logger.Log.Error("failed to list mutual followers for push", "error", err, "user_id", authorID)
			// Fail closed: never widen the audience on error.
			return []int64{authorID}
		}
		return append(friendIDs, authorID)
	default:
		// public and login posts may reach every connected client.
		return nil
	}
}

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
		Content       string   `json:"content"`
		Visibility    string   `json:"visibility"`
		AttachmentIDs []int64  `json:"attachment_ids"`
		CheckID       int64    `json:"check_id"`
		Tags          []string `json:"tags"`
		QuotedPostID  int64    `json:"quoted_post_id"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	if req.Content == "" && req.QuotedPostID <= 0 {
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

	// Quote/repost: the referenced post must exist and be visible to the
	// author. Self-quotes are allowed; the referenced post's own visibility
	// is re-checked per viewer at read time.
	if req.QuotedPostID > 0 {
		quoted, err := h.postRepo.GetByID(req.QuotedPostID)
		if err != nil {
			logger.Log.Error("failed to load quoted post", "error", err, "post_id", req.QuotedPostID)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to verify quoted post"})
			return
		}
		if quoted == nil || !canViewPost(quoted, userID) {
			c.JSON(http.StatusNotFound, gin.H{"error": "quoted post not found"})
			return
		}
	}

	// Posts cannot carry encrypted attachments: E2EE messages flow through
	// the chat service, posts are public. Reject any attachment that was
	// uploaded with private visibility or marked as E2EE ciphertext so a
	// compromised client cannot smuggle an encrypted blob into a public
	// post and have it be silently readable only by them.
	if len(req.AttachmentIDs) > 0 {
		attRepo := repository.NewAttachmentRepository()
		atts, err := attRepo.GetByIDsOwnedBy(userID, req.AttachmentIDs)
		if err != nil {
			logger.Log.Error("failed to verify post attachments", "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to verify attachments"})
			return
		}
		if len(atts) != len(req.AttachmentIDs) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "attachment not found or not owned by you"})
			return
		}
		for _, a := range atts {
			if a.Visibility != visibilityPublic {
				c.JSON(http.StatusBadRequest, gin.H{"error": "encrypted or non-public attachments cannot be used in posts"})
				return
			}
			if isEncryptedMime(a.MimeType) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "encrypted attachments cannot be used in posts"})
				return
			}
		}
	}

	post, err := h.postRepo.Create(userID, req.Content, req.Visibility, req.AttachmentIDs, req.CheckID, req.QuotedPostID)
	if err != nil {
		logger.Log.Error("failed to create post", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create post"})
		return
	}

	if len(req.Tags) > 0 {
		if err := repository.SetPostTags(post.ID, req.Tags); err != nil {
			logger.Log.Warn("failed to set post tags", "error", err, "post_id", post.ID)
		} else {
			post.Tags = req.Tags
		}
	}

	// Realtime push respects the post's visibility: public/login posts go to
	// every connected (authenticated) socket, friends-only posts only to
	// mutual followers, private posts only to the author. Broadcasting the
	// content unfiltered would leak restricted posts to everyone online.
	events.Publish(c.Request.Context(), events.PostCreated, postRecipients(userID, req.Visibility), post)

	c.JSON(http.StatusCreated, post)
}

// isEncryptedMime returns true for MIME types that carry E2EE ciphertext. The
// .ofe container carries an OpenField-encrypted blob and may only appear in
// chat attachments, never in posts.
func isEncryptedMime(mime string) bool {
	return mime == "application/x-openfield-encrypted" || strings.HasPrefix(mime, "application/x-openfield-encrypted;") ||
		strings.HasSuffix(mime, ".ofe")
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

	attachTags([]model.Post{*post})
	c.JSON(http.StatusOK, post)
}

// attachTags loads tags for every post in posts and stamps them onto the
// matching struct. Failures are logged but never fail the parent request.
func attachTags(posts []model.Post) {
	if len(posts) == 0 {
		return
	}
	ids := make([]int64, len(posts))
	for i, p := range posts {
		ids[i] = p.ID
	}
	tags, err := repository.LoadPostTagsFor(ids)
	if err != nil {
		logger.Log.Warn("failed to load post tags", "error", err)
		return
	}
	for i := range posts {
		if t, ok := tags[posts[i].ID]; ok {
			posts[i].Tags = t
		}
	}
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

// parseTimeParam accepts RFC3339 or unix-seconds timestamps.
func parseTimeParam(v string) (*time.Time, error) {
	if v == "" {
		return nil, nil
	}
	if ts, err := strconv.ParseInt(v, 10, 64); err == nil {
		t := time.Unix(ts, 0).UTC()
		return &t, nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return &t, nil
	}
	return nil, fmt.Errorf("invalid time (use RFC3339 or unix seconds)")
}

// ListPosts retrieves paginated posts with optional advanced filters: a
// content keyword (`q`), author (`author_id` or `author` name substring), a
// tag (`tag`) and an inclusive created-at time range (`from` / `to`). All
// combine with AND.
func (h *PostHandler) ListPosts(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	viewer := requesterID(c)

	filter := repository.PostSearchFilter{
		Query:      strings.TrimSpace(c.Query("q")),
		AuthorName: strings.TrimSpace(c.Query("author")),
		Tag:        strings.TrimPrefix(strings.ToLower(strings.TrimSpace(c.Query("tag"))), "#"),
	}
	if v := c.Query("author_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid author ID"})
			return
		}
		filter.AuthorID = id
	}
	from, err := parseTimeParam(c.Query("from"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	to, err := parseTimeParam(c.Query("to"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	filter.From = from
	filter.To = to

	posts, err := h.postRepo.Search(filter, page, limit, viewer)
	if err != nil {
		logger.Log.Error("failed to list posts", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list posts"})
		return
	}

	// When a tag is supplied, narrow the result to the ids that carry it.
	if filter.Tag != "" {
		ids, terr := repository.ListPostsByTag(filter.Tag, 0)
		if terr == nil {
			allow := map[int64]struct{}{}
			for _, id := range ids {
				allow[id] = struct{}{}
			}
			filtered := posts[:0]
			for _, p := range posts {
				if _, ok := allow[p.ID]; ok {
					filtered = append(filtered, p)
				}
			}
			posts = filtered
		}
	}

	attachTags(posts)
	c.JSON(http.StatusOK, gin.H{
		"posts": posts,
		"page":  page,
		"limit": limit,
		"query": filter.Query,
		"tag":   filter.Tag,
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

	attachTags(posts)
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

	attachTags(posts)
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

// maxTipCoins is the largest coin amount a single tip may carry.
const maxTipCoins = 100000

// TipPost charges the authenticated user [amount] coins to appreciate a post:
// the author's wallet receives 95%, the rest is the platform fee. The payment
// PIN (set up via the same flow as transfers) authorizes the charge.
func (h *PostHandler) TipPost(c *gin.Context) {
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
		Amount int64  `json:"amount" binding:"required"`
		Pin    string `json:"pin" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "amount and pin are required"})
		return
	}
	if req.Amount < 1 || req.Amount > maxTipCoins {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid tip amount"})
		return
	}

	post, err := h.postRepo.GetByID(postID)
	if err != nil {
		logger.Log.Error("failed to get post", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load post"})
		return
	}
	if post == nil || !canViewPost(post, userID) {
		c.JSON(http.StatusNotFound, gin.H{"error": "post not found"})
		return
	}
	if post.UserID == userID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot tip your own post"})
		return
	}

	pinHash, err := repository.NewUserRepository().GetPinHash(userID)
	if err != nil {
		logger.Log.Error("failed to load payment pin", "error", err, "user_id", userID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load payment pin"})
		return
	}
	if pinHash == "" {
		c.JSON(http.StatusForbidden, gin.H{"error": "payment pin not set"})
		return
	}
	if !security.VerifyPin(req.Pin, pinHash) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid payment pin"})
		return
	}

	tip, err := repository.NewTipRepository().Tip(postID, userID, req.Amount)
	if err != nil {
		if errors.Is(err, repository.ErrInsufficientBalance) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "insufficient balance"})
			return
		}
		if errors.Is(err, repository.ErrInvalidAmount) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid tip amount"})
			return
		}
		if errors.Is(err, repository.ErrSelfTip) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "cannot tip your own post"})
			return
		}
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "post not found"})
			return
		}
		logger.Log.Error("failed to tip post", "error", err, "user_id", userID, "post_id", postID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to tip post"})
		return
	}

	c.JSON(http.StatusCreated, tip)
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
// nested replies, the author of the parent reply (excluding the replier). It
// also drops durable inbox notifications for every recipient so they find
// out about the reply even when they were not online.
func (h *PostHandler) notifyReplyCreated(ctx context.Context, reply interface{}, postAuthorID int64) {
	recipients := []int64{postAuthorID}
	var r *model.PostReply
	if cast, ok := reply.(*model.PostReply); ok {
		r = cast
		if r.ParentID != nil {
			parent, err := h.replyRepo.GetByID(*r.ParentID)
			if err == nil && parent != nil && parent.UserID != postAuthorID {
				recipients = append(recipients, parent.UserID)
			}
		}
	}
	events.Publish(ctx, events.ReplyCreated, recipients, reply)
	if r == nil {
		return
	}
	for _, uid := range recipients {
		if uid == r.UserID {
			continue
		}
		data, _ := json.Marshal(map[string]any{
			"post_id":   r.PostID,
			"reply_id":  r.ID,
			"author_id": r.UserID,
		})
		title := "新回复"
		body := "你的帖子收到了新回复。"
		_ = repository.CreateNotification(uid, "post.reply.created", title, body, data)
	}
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
