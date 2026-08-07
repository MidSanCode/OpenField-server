package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/config"
	"github.com/openfield/server/pkg/logger"
	"github.com/openfield/server/pkg/middleware"
	"github.com/openfield/server/pkg/model"
	"github.com/openfield/server/pkg/repository"
	"github.com/openfield/server/pkg/storage"
)

// UserHandler handles user-related requests.
type UserHandler struct {
	userRepo   *repository.UserRepository
	followRepo *repository.FollowRepository
	permRepo   *repository.PermissionRepository
	attRepo    *repository.AttachmentRepository
	store      *storage.Store
	cfg        config.StorageConfig
}

// NewUserHandler creates a new UserHandler.
func NewUserHandler(store *storage.Store, cfg config.StorageConfig) *UserHandler {
	return &UserHandler{
		userRepo:   repository.NewUserRepository(),
		followRepo: repository.NewFollowRepository(),
		permRepo:   repository.NewPermissionRepository(),
		attRepo:    repository.NewAttachmentRepository(),
		store:      store,
		cfg:        cfg,
	}
}

// requesterID returns the authenticated user id forwarded by the gateway, or 0
// when the request is anonymous. Works for both protected (context) and public
// (header) routes.
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

// populateFollowStats fills follower/following counts and, for profile reads by
// an authenticated requester, whether the requester follows the target.
func (h *UserHandler) populateFollowStats(user *model.User, requester int64) {
	if user == nil {
		return
	}
	if count, err := h.followRepo.CountFollowers(user.ID); err == nil {
		user.FollowerCount = count
	}
	if count, err := h.followRepo.CountFollowing(user.ID); err == nil {
		user.FollowingCount = count
	}
	if requester > 0 && requester != user.ID {
		if following, err := h.followRepo.IsFollowing(requester, user.ID); err == nil {
			user.IsFollowing = following
		}
	}
}

// GetCurrentUser retrieves the current authenticated user's profile.
func (h *UserHandler) GetCurrentUser(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	user, err := h.userRepo.GetByID(userID)
	if err != nil {
		logger.Log.Error("failed to get user", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get user"})
		return
	}
	if user == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}

	if used, err := h.attRepo.SumSizeByUser(userID); err == nil {
		user.StorageUsed = used
	}

	h.populateFollowStats(user, userID)

	c.JSON(http.StatusOK, user)
}

// GetMyPermissions returns the current user's effective permissions and groups.
func (h *UserHandler) GetMyPermissions(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	perms, err := h.permRepo.GetEffectivePermissions(userID)
	if err != nil {
		logger.Log.Error("failed to get permissions", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get permissions"})
		return
	}
	groups, err := h.permRepo.GetUserGroups(userID)
	if err != nil {
		logger.Log.Error("failed to get groups", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get groups"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"permissions": perms, "groups": groups})
}

// SearchUsers searches users by username or nickname.
func (h *UserHandler) SearchUsers(c *gin.Context) {
	q := c.Query("q")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))

	users, err := h.userRepo.Search(q, limit)
	if err != nil {
		logger.Log.Error("failed to search users", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to search users"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"users": users})
}

// UpdateProfile updates the current user's nickname.
func (h *UserHandler) UpdateProfile(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req struct {
		Username string `json:"username"`
		Nickname string `json:"nickname"`
		Bio      string `json:"bio"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	if req.Username == "" && req.Nickname == "" && req.Bio == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "nothing to update"})
		return
	}

	current, err := h.userRepo.GetByID(userID)
	if err != nil {
		logger.Log.Error("failed to get user", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update profile"})
		return
	}

	username := req.Username
	if username == "" {
		username = current.Username
	}
	nickname := req.Nickname
	if nickname == "" {
		nickname = current.Nickname
	}
	bio := req.Bio
	if bio == "" {
		bio = current.Bio
	}

	user, err := h.userRepo.UpdateProfile(userID, username, nickname, bio)
	if err != nil {
		logger.Log.Error("failed to update profile", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, user)
}

// UploadAvatar uploads a new avatar image and updates the user profile.
func (h *UserHandler) UploadAvatar(c *gin.Context) {
	h.uploadImage(c, "avatar")
}

// UploadBanner uploads a new banner (header) image and updates the user profile.
func (h *UserHandler) UploadBanner(c *gin.Context) {
	h.uploadImage(c, "banner")
}

func (h *UserHandler) uploadImage(c *gin.Context, kind string) {
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

	user, err := h.userRepo.GetByID(userID)
	if err != nil {
		logger.Log.Error("failed to get user", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to upload image"})
		return
	}
	if user != nil && user.StorageQuota > 0 {
		used, err := h.attRepo.SumSizeByUser(userID)
		if err != nil {
			logger.Log.Error("failed to check storage quota", "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to check storage quota"})
			return
		}
		if used+header.Size > user.StorageQuota {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "storage quota exceeded"})
			return
		}
	}

	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "image/jpeg"
	}

	objectKey, url, err := h.store.Upload(c.Request.Context(), file, header.Size, contentType, header.Filename)
	if err != nil {
		logger.Log.Error("failed to upload image", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to upload image"})
		return
	}

	if _, err := h.attRepo.Create(userID, objectKey, header.Filename, contentType, header.Size, url, "", "public"); err != nil {
		logger.Log.Error("failed to save image attachment", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save image"})
		return
	}

	switch kind {
	case "avatar":
		err = h.userRepo.UpdateAvatar(userID, url)
	case "banner":
		err = h.userRepo.UpdateBanner(userID, url)
	}
	if err != nil {
		logger.Log.Error("failed to update user image", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update image"})
		return
	}

	finalUser, err := h.userRepo.GetByID(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get user"})
		return
	}

	c.JSON(http.StatusOK, finalUser)
}

// GetUser retrieves a user by ID.
func (h *UserHandler) GetUser(c *gin.Context) {
	idStr := c.Param("id")
	userID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user ID"})
		return
	}

	user, err := h.userRepo.GetByID(userID)
	if err != nil {
		logger.Log.Error("failed to get user", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get user"})
		return
	}
	if user == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}

	h.populateFollowStats(user, requesterID(c))

	c.JSON(http.StatusOK, user)
}

// FollowUser makes the current user follow the target user.
func (h *UserHandler) FollowUser(c *gin.Context) {
	followerID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	followeeID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user ID"})
		return
	}
	if followerID == followeeID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "you cannot follow yourself"})
		return
	}

	target, err := h.userRepo.GetByID(followeeID)
	if err != nil {
		logger.Log.Error("failed to get user", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to follow user"})
		return
	}
	if target == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}

	if err := h.followRepo.Follow(followerID, followeeID); err != nil {
		logger.Log.Error("failed to follow user", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to follow user"})
		return
	}

	h.populateFollowStats(target, followerID)
	c.JSON(http.StatusOK, target)
}

// UnfollowUser makes the current user stop following the target user.
func (h *UserHandler) UnfollowUser(c *gin.Context) {
	followerID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	followeeID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user ID"})
		return
	}

	if err := h.followRepo.Unfollow(followerID, followeeID); err != nil {
		logger.Log.Error("failed to unfollow user", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to unfollow user"})
		return
	}

	c.Status(http.StatusNoContent)
}

// ListFollowers returns the users who follow the given user.
func (h *UserHandler) ListFollowers(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user ID"})
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))

	users, err := h.followRepo.ListFollowers(userID, page, limit)
	if err != nil {
		logger.Log.Error("failed to list followers", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list followers"})
		return
	}

	requester := requesterID(c)
	for i := range users {
		h.populateFollowStats(&users[i], requester)
	}

	c.JSON(http.StatusOK, gin.H{"users": users, "page": page, "limit": limit})
}

// ListFollowing returns the users the given user follows.
func (h *UserHandler) ListFollowing(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user ID"})
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))

	users, err := h.followRepo.ListFollowing(userID, page, limit)
	if err != nil {
		logger.Log.Error("failed to list following", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list following"})
		return
	}

	requester := requesterID(c)
	for i := range users {
		h.populateFollowStats(&users[i], requester)
	}

	c.JSON(http.StatusOK, gin.H{"users": users, "page": page, "limit": limit})
}
