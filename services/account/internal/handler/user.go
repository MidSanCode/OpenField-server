package handler

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/config"
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

// UserHandler handles user-related requests.
type UserHandler struct {
	userRepo   *repository.UserRepository
	followRepo *repository.FollowRepository
	permRepo   *repository.PermissionRepository
	attRepo    *repository.AttachmentRepository
	taskRepo   *repository.TaskRepository
	store      *storage.Manager
	cfg        config.StorageConfig
	gameCfg    config.GameConfig
}

// NewUserHandler creates a new UserHandler.
func NewUserHandler(store *storage.Manager, cfg config.StorageConfig) *UserHandler {
	return &UserHandler{
		userRepo:   repository.NewUserRepository(),
		followRepo: repository.NewFollowRepository(),
		permRepo:   repository.NewPermissionRepository(),
		attRepo:    repository.NewAttachmentRepository(),
		taskRepo:   repository.NewTaskRepository(),
		store:      store,
		cfg:        cfg,
	}
}

// SetGameConfig attaches the gameplay config so the handler can run the
// daily-bonus grant with the right amount and timezone.
func (h *UserHandler) SetGameConfig(c config.GameConfig) {
	h.gameCfg = c
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
// an authenticated requester, whether the requester follows the target. When
// the target hides their follow lists, the aggregate counts are suppressed for
// everyone except the target themself.
func (h *UserHandler) populateFollowStats(user *model.User, requester int64) {
	if user == nil {
		return
	}
	if !user.HideFollowLists || requester == user.ID {
		if count, err := h.followRepo.CountFollowers(user.ID); err == nil {
			user.FollowerCount = count
		}
		if count, err := h.followRepo.CountFollowing(user.ID); err == nil {
			user.FollowingCount = count
		}
	}
	if requester > 0 && requester != user.ID {
		if following, err := h.followRepo.IsFollowing(requester, user.ID); err == nil {
			user.IsFollowing = following
		}
		if isFriend, err := h.followRepo.AreMutual(requester, user.ID); err == nil {
			user.IsFriend = isFriend
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

// ListStorageBuckets returns every configured storage bucket with its label,
// default quota and membership gate, plus the requester's current bucket.
func (h *UserHandler) ListStorageBuckets(c *gin.Context) {
	userID := requesterID(c)
	user, err := h.userRepo.GetByID(userID)
	if err != nil {
		logger.Log.Error("failed to get user", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get storage buckets"})
		return
	}
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	current := user.StorageBucket
	buckets := make([]gin.H, 0, len(h.store.Buckets()))
	for _, b := range h.store.Buckets() {
		buckets = append(buckets, gin.H{
			"name":             b.Name,
			"label":            b.Label,
			"default_quota":    b.DefaultQuota,
			"default_quota_mb": b.DefaultQuota / (1024 * 1024),
			"min_member_level": b.MinMemberLevel,
			"is_default":       b.IsDefault,
			"locked":           !h.bucketAllowed(b, user),
		})
	}

	c.JSON(http.StatusOK, gin.H{"buckets": buckets, "current": current})
}

// bucketAllowed reports whether a user may switch to (or stay on) a bucket:
// buckets with a minimum member level require an active membership at that
// level or higher.
func (h *UserHandler) bucketAllowed(b config.StorageBucketConfig, user *model.User) bool {
	if b.MinMemberLevel <= 0 {
		return true
	}
	if user == nil {
		return false
	}
	return model.MembershipActive(user.MemberLevel, user.MemberExpiresAt, time.Now()) && user.MemberLevel >= b.MinMemberLevel
}

// SetMyStorageBucket switches the current user to another logical storage
// bucket. A bucket gated behind a minimum member level is rejected when the
// user lacks an active membership at that level. The user's quota is set to
// the bucket's default quota; existing attachments are left where they are.
func (h *UserHandler) SetMyStorageBucket(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req struct {
		Bucket string `json:"bucket" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	user, err := h.userRepo.GetByID(userID)
	if err != nil {
		logger.Log.Error("failed to get user", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to switch storage bucket"})
		return
	}
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	bucket, exact := h.cfg.BucketByName(req.Bucket)
	if !exact {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown storage bucket"})
		return
	}
	if !h.bucketAllowed(bucket, user) {
		c.JSON(http.StatusForbidden, gin.H{"error": "membership level insufficient"})
		return
	}

	updated, err := h.userRepo.SetStorageBucket(userID, bucket.Name, bucket.DefaultQuota)
	if err != nil {
		logger.Log.Error("failed to switch storage bucket", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to switch storage bucket"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"user": updated})
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

// SetE2EEKey publishes (or removes) the current user's X25519 public key used
// for end-to-end-encrypted group-key envelopes.
func (h *UserHandler) SetE2EEKey(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req struct {
		PublicKey string `json:"public_key"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	if err := h.userRepo.SetE2EEPublicKey(userID, req.PublicKey); err != nil {
		logger.Log.Error("failed to set e2ee public key", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to set e2ee public key"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "e2ee public key updated"})
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

	if h.store == nil || !h.store.Enabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "storage not configured"})
		return
	}

	user, err := h.userRepo.GetByID(userID)
	if err != nil {
		logger.Log.Error("failed to get user", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to upload image"})
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

	if user != nil {
		now := time.Now()
		effectiveQuota := user.StorageQuota
		if bucket, ok := h.cfg.BucketByName(user.StorageBucket); ok && bucket.IsDefault {
			effectiveQuota += model.MemberStorageBonusAt(user.MemberLevel, user.MemberExpiresAt, now)
		}
		if effectiveQuota > 0 {
			used, err := h.attRepo.SumSizeByUser(userID)
			if err != nil {
				logger.Log.Error("failed to check storage quota", "error", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to check storage quota"})
				return
			}
			if used+header.Size > effectiveQuota {
				c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "storage quota exceeded"})
				return
			}
		}
	}

	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "image/jpeg"
	}

	data, err := io.ReadAll(file)
	if err != nil {
		logger.Log.Error("failed to read image", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to upload image"})
		return
	}

	store := h.store.For(user.StorageBucket)
	objectKey, url, err := store.Upload(c.Request.Context(), bytes.NewReader(data), int64(len(data)), contentType, header.Filename)
	if err != nil {
		logger.Log.Error("failed to upload image", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to upload image"})
		return
	}

	if _, err := h.attRepo.Create(userID, objectKey, header.Filename, contentType, int64(len(data)), url, "", "public", user.StorageBucket, sha256Hex(data)); err != nil {
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
	if !h.followListVisible(c, userID) {
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
	if !h.followListVisible(c, userID) {
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

// ListFriends returns the users who mutually follow the given user.
func (h *UserHandler) ListFriends(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user ID"})
		return
	}
	if !h.followListVisible(c, userID) {
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))

	users, err := h.followRepo.ListFriends(userID, page, limit)
	if err != nil {
		logger.Log.Error("failed to list friends", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list friends"})
		return
	}

	requester := requesterID(c)
	for i := range users {
		h.populateFollowStats(&users[i], requester)
	}

	c.JSON(http.StatusOK, gin.H{"users": users, "page": page, "limit": limit})
}

// followListVisible verifies the requester may read userID's follow lists. A
// target that hides its lists is only visible to the target themself; everyone
// else receives 403. The response is written on failure.
func (h *UserHandler) followListVisible(c *gin.Context, userID int64) bool {
	target, err := h.userRepo.GetByID(userID)
	if err != nil {
		logger.Log.Error("failed to get user", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get user"})
		return false
	}
	if target == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return false
	}
	if !target.HideFollowLists || requesterID(c) == userID {
		return true
	}
	c.JSON(http.StatusForbidden, gin.H{"error": "follow lists are hidden"})
	return false
}

// ClaimDailyBonus grants the day's experience + currency bonus to the
// authenticated user and updates the consecutive sign-in streak. Idempotent:
// returns {granted: false} when already claimed today. Delegates to the shared
// check-in logic so the daily task, exp history and wallet are all consistent.
func (h *UserHandler) ClaimDailyBonus(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	loc := h.gameCfg.Location()
	expAmt := h.gameCfg.EffectiveDailyBonus()
	curAmt := h.gameCfg.EffectiveDailyCurrency()
	granted, expGranted, streak, err := h.taskRepo.Checkin(userID, expAmt, curAmt, 0, false, loc, time.Now())
	if err != nil {
		logger.Log.Error("failed to grant daily bonus", "error", err, "user_id", userID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to grant daily bonus"})
		return
	}
	newExp, _ := h.userRepo.GetByID(userID)
	if newExp == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load user"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"granted":  granted,
		"amount":   expGranted,
		"currency": curAmt,
		"exp":      newExp.Exp,
		"level":    model.LevelForExp(newExp.Exp),
		"streak":   streak,
	})
}

// UpdateLocale stores the user's region preference and display language for
// server-pushed notifications.
func (h *UserHandler) UpdateLocale(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var req struct {
		Region string `json:"region"`
		Lang   string `json:"lang"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	user, err := h.userRepo.UpdateLocale(userID, req.Region, req.Lang)
	if err != nil {
		logger.Log.Error("failed to update locale", "error", err, "user_id", userID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update locale"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"region": user.Region, "lang": user.Lang})
}

// UpdatePrivacy toggles the current user's follow-list privacy. When enabled,
// the followers/following/friends lists are hidden from everyone else and the
// aggregate counts are suppressed on public profiles.
func (h *UserHandler) UpdatePrivacy(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var req struct {
		HideFollowLists bool `json:"hide_follow_lists"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	user, err := h.userRepo.SetHideFollowLists(userID, req.HideFollowLists)
	if err != nil {
		logger.Log.Error("failed to update privacy", "error", err, "user_id", userID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update privacy"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"hide_follow_lists": user.HideFollowLists})
}

// UpdateNameStyle stores the current user's display-name styling (a gradient
// color list + optional direction + optional animated flag) as permitted by
// their active membership tier. Multi-color gradients are gated to Lv.3+ and
// the animated flag to Lv.4; Lv.1 is restricted to the fixed preset palette.
func (h *UserHandler) UpdateNameStyle(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var req struct {
		Colors      []string `json:"colors"`
		Color       string   `json:"color"`
		ColorTo     string   `json:"color_to"`
		Dynamic     bool     `json:"dynamic"`
		Direction   string   `json:"direction"`
		AvatarFrame string   `json:"avatar_frame"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	user, err := h.userRepo.GetByID(userID)
	if err != nil || user == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load user"})
		return
	}
	now := time.Now()
	cap := model.MemberNameStyleAllowed(user.MemberLevel, user.MemberExpiresAt, now)

	colors := model.NameColorList(req.Colors)
	// Backward-compatible single color: the legacy color field maps to a
	// one-element list when the new colors array is absent.
	if len(colors) == 0 {
		if req.Color == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "color is required"})
			return
		}
		colors = model.NameColorList{req.Color}
		if req.ColorTo != "" {
			colors = append(colors, req.ColorTo)
		}
	}
	if len(colors) > cap.MaxColors {
		c.JSON(http.StatusForbidden, gin.H{"error": "too many gradient colors for this tier"})
		return
	}
	if cap.PresetsOnly {
		for _, col := range colors {
			if !model.IsPresetNameColor(col) {
				c.JSON(http.StatusForbidden, gin.H{"error": "current tier allows preset colors only"})
				return
			}
		}
	} else {
		for _, col := range colors {
			if !model.ValidHexColor(col) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid color"})
				return
			}
		}
	}
	if len(colors) > 1 {
		if !cap.AllowGradient {
			c.JSON(http.StatusForbidden, gin.H{"error": "current tier does not allow gradients"})
			return
		}
		if req.Direction != "" && !model.ValidGradientDirection(req.Direction) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid gradient direction"})
			return
		}
	}
	if req.Dynamic && !cap.AllowDynamic {
		c.JSON(http.StatusForbidden, gin.H{"error": "current tier does not allow dynamic colors"})
		return
	}

	updated, err := h.userRepo.UpdateNameStyle(userID, colors, req.Direction, req.Dynamic, req.AvatarFrame)
	if err != nil {
		logger.Log.Error("failed to update name style", "error", err, "user_id", userID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update name style"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"name_color":             updated.NameColor,
		"name_color_to":          updated.NameColorTo,
		"name_dynamic":           updated.NameDynamic,
		"name_colors":            updated.NameColors,
		"name_gradient_direction": updated.NameGradientDirection,
		"avatar_frame":           updated.AvatarFrame,
	})
}

// AdjustExp is an admin-only endpoint to add or subtract experience from a
// user's account. Positive adjustments are scaled by the user's active
// membership multiplier; negative (penalty) adjustments always pass through
// unchanged. Requires the user.adjust_exp permission (enforced by the gateway).
func (h *UserHandler) AdjustExp(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user ID"})
		return
	}
	var req struct {
		Delta int64 `json:"delta"`
		// Description is an optional note recorded in the exp history.
		Description string `json:"description"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	user, err := h.userRepo.GetByID(userID)
	if err != nil {
		logger.Log.Error("failed to load user for exp adjust", "error", err, "user_id", userID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to adjust exp"})
		return
	}
	if user == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}

	// Positive grants from the admin are treated like every other exp source:
	// they benefit from the membership multiplier. Negative deltas subtract the
	// requested amount verbatim.
	delta := req.Delta
	if delta > 0 {
		delta = model.ApplyMemberExp(delta, user.MemberLevel, user.MemberExpiresAt, time.Now())
	}

	exp, err := h.userRepo.AdjustExp(userID, delta)
	if err != nil {
		logger.Log.Error("failed to adjust exp", "error", err, "user_id", userID, "delta", delta)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to adjust exp"})
		return
	}
	if delta != 0 {
		desc := req.Description
		if desc == "" {
			desc = "管理员调整"
		}
		expRepo := repository.NewExpRepository()
		if err := expRepo.Add(userID, delta, model.ExpReasonAdjust, desc); err != nil {
			logger.Log.Warn("failed to record exp history", "error", err, "user_id", userID)
		}
	}
	c.JSON(http.StatusOK, gin.H{"exp": exp, "level": model.LevelForExp(exp)})
}
