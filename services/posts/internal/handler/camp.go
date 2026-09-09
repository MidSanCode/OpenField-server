package handler

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/logger"
	"github.com/openfield/server/pkg/middleware"
	"github.com/openfield/server/pkg/model"
	"github.com/openfield/server/pkg/repository"
)

// CampHandler handles 贴吧-style camp endpoints.
type CampHandler struct {
	repo     *repository.CampRepository
	postRepo *repository.PostRepository
	userRepo *repository.UserRepository
}

// NewCampHandler creates a new CampHandler.
func NewCampHandler() *CampHandler {
	return &CampHandler{
		repo:     repository.NewCampRepository(),
		postRepo: repository.NewPostRepository(),
		userRepo: repository.NewUserRepository(),
	}
}

// List returns visible camps; ?mine=1 lists the caller's camps instead.
func (h *CampHandler) List(c *gin.Context) {
	userID, _ := middleware.GetUserID(c)
	if c.Query("mine") == "1" {
		if userID == 0 {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		camps, err := h.repo.ListMine(userID, 50)
		if err != nil {
			logger.Log.Error("failed to list my camps", "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list camps"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"camps": camps})
		return
	}
	camps, err := h.repo.List(userID, c.Query("q"), 50)
	if err != nil {
		logger.Log.Error("failed to list camps", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list camps"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"camps": camps})
}

// Get returns one camp. Hidden camps are only visible to their members.
func (h *CampHandler) Get(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid camp ID"})
		return
	}
	userID, _ := middleware.GetUserID(c)
	camp, err := h.repo.GetByID(id, userID)
	if err != nil {
		logger.Log.Error("failed to get camp", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get camp"})
		return
	}
	if camp == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "camp not found"})
		return
	}
	if !camp.IsVisible && !camp.IsMember {
		c.JSON(http.StatusNotFound, gin.H{"error": "camp not found"})
		return
	}
	c.JSON(http.StatusOK, camp)
}

// Create mints a camp; the per-user creation quota scales with membership.
func (h *CampHandler) Create(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var req struct {
		Name        string `json:"name" binding:"required"`
		Description string `json:"description"`
		IsVisible   *bool  `json:"is_visible"`
		DirectJoin  *bool  `json:"direct_join"`
		MemberPost  *bool  `json:"member_post"`
		MemberPin   *bool  `json:"member_pin"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 60 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "camp name must be 1-60 characters"})
		return
	}
	if len(req.Description) > 500 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "description too long (max 500)"})
		return
	}
	if len([]rune(req.Name)) < 1 || len([]rune(req.Name)) > 60 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "camp name must be 1-60 characters"})
		return
	}

	user, err := h.userRepo.GetByID(userID)
	if err != nil || user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	owned, err := h.repo.CountByCreator(userID)
	if err != nil {
		logger.Log.Error("failed to count camps", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create camp"})
		return
	}
	quota := repository.CampCreateQuotaFor(user.MemberLevel, user.MemberExpiresAt)
	if owned >= quota {
		c.JSON(http.StatusForbidden, gin.H{
			"error":   "camp quota exceeded",
			"limit":   quota,
			"created": owned,
		})
		return
	}

	isVisible := true
	if req.IsVisible != nil {
		isVisible = *req.IsVisible
	}
	directJoin := true
	if req.DirectJoin != nil {
		directJoin = *req.DirectJoin
	}
	memberPost := true
	if req.MemberPost != nil {
		memberPost = *req.MemberPost
	}
	memberPin := false
	if req.MemberPin != nil {
		memberPin = *req.MemberPin
	}
	camp, err := h.repo.Create(userID, req.Name, req.Description, isVisible, directJoin, memberPost, memberPin)
	if err != nil {
		logger.Log.Error("failed to create camp", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create camp"})
		return
	}
	c.JSON(http.StatusCreated, camp)
}

// Update mutates camp settings. Admins may change the basics; the
// permission switches (member_post/member_pin) stay owner-only.
func (h *CampHandler) Update(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid camp ID"})
		return
	}
	var req struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		IsVisible   *bool   `json:"is_visible"`
		DirectJoin  *bool   `json:"direct_join"`
		MemberPost  *bool   `json:"member_post"`
		MemberPin   *bool   `json:"member_pin"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if req.Name != nil {
		n := strings.TrimSpace(*req.Name)
		if n == "" || len([]rune(n)) > 60 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "camp name must be 1-60 characters"})
			return
		}
		req.Name = &n
	}
	if err := h.repo.Update(id, userID, req.Name, req.Description, req.IsVisible, req.DirectJoin, req.MemberPost, req.MemberPin); err != nil {
		if err == repository.ErrNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "camp not found"})
			return
		}
		if err == repository.ErrForbidden {
			c.JSON(http.StatusForbidden, gin.H{"error": "camp admins only; permission switches are owner-only"})
			return
		}
		logger.Log.Error("failed to update camp", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update camp"})
		return
	}
	camp, err := h.repo.GetByID(id, userID)
	if err != nil || camp == nil {
		c.JSON(http.StatusOK, gin.H{"status": "updated"})
		return
	}
	c.JSON(http.StatusOK, camp)
}

// Delete removes a camp (creator only).
func (h *CampHandler) Delete(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid camp ID"})
		return
	}
	if err := h.repo.Delete(id, userID); err != nil {
		if err == repository.ErrNotFound {
			c.JSON(http.StatusForbidden, gin.H{"error": "camp not found or you are not the creator"})
			return
		}
		logger.Log.Error("failed to delete camp", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete camp"})
		return
	}
	c.Status(http.StatusNoContent)
}

// Join enters a camp. Camps with direct_join disabled reject self-joining.
func (h *CampHandler) Join(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid camp ID"})
		return
	}
	camp, err := h.repo.GetByID(id, 0)
	if err != nil {
		logger.Log.Error("failed to get camp", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to join camp"})
		return
	}
	if camp == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "camp not found"})
		return
	}
	if !camp.DirectJoin {
		c.JSON(http.StatusForbidden, gin.H{"error": "this camp does not allow direct joining"})
		return
	}
	if _, err := h.repo.Join(id, userID); err != nil {
		logger.Log.Error("failed to join camp", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to join camp"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "joined"})
}

// Leave exits a camp; the creator cannot leave.
func (h *CampHandler) Leave(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid camp ID"})
		return
	}
	camp, err := h.repo.GetByID(id, 0)
	if err != nil || camp == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "camp not found"})
		return
	}
	if camp.CreatorID == userID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the creator cannot leave their own camp"})
		return
	}
	if err := h.repo.Leave(id, userID); err != nil {
		if err == repository.ErrNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "you are not a member of this camp"})
			return
		}
		logger.Log.Error("failed to leave camp", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to leave camp"})
		return
	}
	c.Status(http.StatusNoContent)
}

// ListPosts returns the camp's posts. Camp content is members-only: hidden
// camps answer 404 to outsiders (they reveal nothing, not even membership
// gating), visible camps answer 403 with a join hint.
func (h *CampHandler) ListPosts(c *gin.Context) {
	userID, _ := middleware.GetUserID(c)
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid camp ID"})
		return
	}
	camp, err := h.repo.GetByID(id, userID)
	if err != nil {
		logger.Log.Error("failed to get camp", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list camp posts"})
		return
	}
	if camp == nil || (!camp.IsVisible && !camp.IsMember) {
		c.JSON(http.StatusNotFound, gin.H{"error": "camp not found"})
		return
	}
	if !camp.IsMember {
		c.JSON(http.StatusForbidden, gin.H{"error": "camp posts are visible to members only"})
		return
	}
	beforeID, _ := strconv.ParseInt(c.DefaultQuery("before", "0"), 10, 64)
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	posts, err := h.postRepo.ListByCamp(id, userID, beforeID, limit)
	if err != nil {
		logger.Log.Error("failed to list camp posts", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list camp posts"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"posts": posts})
}

// requireCampRole resolves the camp and the caller's role, answering the
// request when access is insufficient. Returns the camp (nil when not found
// and already answered) and the role.
func (h *CampHandler) requireCampRole(c *gin.Context, campID, userID int64, minRole string) (*model.Camp, string, bool) {
	camp, err := h.repo.GetByID(campID, userID)
	if err != nil {
		logger.Log.Error("failed to get camp", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load camp"})
		return nil, "", false
	}
	if camp == nil || (!camp.IsVisible && camp.MyRole == "") {
		c.JSON(http.StatusNotFound, gin.H{"error": "camp not found"})
		return nil, "", false
	}
	if model.CampRoleRank(camp.MyRole) < model.CampRoleRank(minRole) {
		c.JSON(http.StatusForbidden, gin.H{"error": "insufficient camp role"})
		return nil, "", false
	}
	return camp, camp.MyRole, true
}

// ListMembers returns the camp roster. Members see it; outsiders get 403
// (404 for hidden camps).
func (h *CampHandler) ListMembers(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid camp ID"})
		return
	}
	camp, role, ok := h.requireCampRole(c, id, userID, model.CampRoleMember)
	if !ok {
		return
	}
	_ = camp
	members, err := h.repo.ListMembers(id, 200)
	if err != nil {
		logger.Log.Error("failed to list camp members", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list camp members"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"members": members, "my_role": role})
}

// AddMember directly enrolls a user (admin invite, bypasses direct_join).
// Admins may only invite plain members; role grants go through SetMemberRole.
func (h *CampHandler) AddMember(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	campID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid camp ID"})
		return
	}
	targetID, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil || targetID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user ID"})
		return
	}
	if _, _, ok := h.requireCampRole(c, campID, userID, model.CampRoleAdmin); !ok {
		return
	}
	added, err := h.repo.AddMember(campID, targetID, model.CampRoleMember)
	if err != nil {
		logger.Log.Error("failed to add camp member", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to add camp member"})
		return
	}
	if !added {
		c.JSON(http.StatusConflict, gin.H{"error": "user is already a member"})
		return
	}
	c.Status(http.StatusNoContent)
}

// SetMemberRole promotes/demotes a roster member (owner only; the owner's
// own role is immutable).
func (h *CampHandler) SetMemberRole(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	campID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid camp ID"})
		return
	}
	targetID, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil || targetID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user ID"})
		return
	}
	var req struct {
		Role string `json:"role" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body (role required)"})
		return
	}
	if req.Role != model.CampRoleAdmin && req.Role != model.CampRoleMember {
		c.JSON(http.StatusBadRequest, gin.H{"error": "role must be admin or member"})
		return
	}
	if _, _, ok := h.requireCampRole(c, campID, userID, model.CampRoleOwner); !ok {
		return
	}
	if err := h.repo.SetMemberRole(campID, targetID, req.Role); err != nil {
		if err == repository.ErrNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "user is not a member of this camp"})
			return
		}
		logger.Log.Error("failed to set camp member role", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to set camp member role"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "updated", "role": req.Role})
}

// RemoveMember kicks a member. Admins may remove plain members; only the
// owner may remove another admin; nobody removes the owner.
func (h *CampHandler) RemoveMember(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	campID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid camp ID"})
		return
	}
	targetID, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil || targetID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user ID"})
		return
	}
	_, role, ok := h.requireCampRole(c, campID, userID, model.CampRoleAdmin)
	if !ok {
		return
	}
	if targetID == userID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "use leave to exit your own camp membership"})
		return
	}
	targetRole, err := h.repo.GetRole(campID, targetID)
	if err != nil {
		logger.Log.Error("failed to read target camp role", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to remove camp member"})
		return
	}
	if targetRole == model.CampRoleOwner {
		c.JSON(http.StatusForbidden, gin.H{"error": "the owner cannot be removed"})
		return
	}
	if targetRole == model.CampRoleAdmin && role != model.CampRoleOwner {
		c.JSON(http.StatusForbidden, gin.H{"error": "only the owner may remove an admin"})
		return
	}
	if err := h.repo.RemoveMember(campID, targetID); err != nil {
		if err == repository.ErrNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "user is not a member of this camp"})
			return
		}
		logger.Log.Error("failed to remove camp member", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to remove camp member"})
		return
	}
	c.Status(http.StatusNoContent)
}

// PinCampPost toggles a post's camp-scoped pin. Owner/admins may pin any
// camp post; plain members only their own, and only when the camp's
// member_pin switch allows it.
func (h *CampHandler) PinCampPost(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	campID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid camp ID"})
		return
	}
	postID, err := strconv.ParseInt(c.Param("post_id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid post ID"})
		return
	}
	var req struct {
		Pinned *bool `json:"pinned"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Pinned == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body (pinned boolean required)"})
		return
	}
	camp, role, ok := h.requireCampRole(c, campID, userID, model.CampRoleMember)
	if !ok {
		return
	}
	if model.CampRoleRank(role) < model.CampRoleRank(model.CampRoleAdmin) {
		// Plain members may only pin their own posts, and only when the
		// camp's member_pin switch allows it.
		if !camp.MemberPin {
			c.JSON(http.StatusForbidden, gin.H{"error": "pinning is limited to camp admins in this camp"})
			return
		}
		post, err := h.postRepo.GetByID(postID)
		if err != nil {
			logger.Log.Error("failed to load camp post", "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to set camp pinned"})
			return
		}
		if post == nil || post.UserID != userID {
			c.JSON(http.StatusForbidden, gin.H{"error": "you may only pin your own posts in this camp"})
			return
		}
	}
	if err := h.postRepo.SetCampPinned(postID, campID, *req.Pinned); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "post not found in this camp"})
			return
		}
		logger.Log.Error("failed to set camp pinned", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to set camp pinned"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "updated", "camp_pinned": *req.Pinned})
}
