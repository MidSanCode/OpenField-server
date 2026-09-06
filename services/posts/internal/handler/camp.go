package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/logger"
	"github.com/openfield/server/pkg/middleware"
	"github.com/openfield/server/pkg/repository"
)

// CampHandler handles 贴吧-style camp endpoints.
type CampHandler struct {
	repo      *repository.CampRepository
	postRepo  *repository.PostRepository
	userRepo  *repository.UserRepository
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
	camp, err := h.repo.Create(userID, req.Name, req.Description, isVisible, directJoin)
	if err != nil {
		logger.Log.Error("failed to create camp", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create camp"})
		return
	}
	c.JSON(http.StatusCreated, camp)
}

// Update mutates camp settings (creator only).
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
	if err := h.repo.Update(id, userID, req.Name, req.Description, req.IsVisible, req.DirectJoin); err != nil {
		if err == repository.ErrNotFound {
			c.JSON(http.StatusForbidden, gin.H{"error": "camp not found or you are not the creator"})
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

// ListPosts returns the camp's posts (member or visible-camp viewers).
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
