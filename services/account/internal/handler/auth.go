package handler

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/logger"
	"github.com/openfield/server/pkg/middleware"
	"github.com/openfield/server/pkg/repository"
	"github.com/openfield/server/services/account/internal/auth"
	"golang.org/x/crypto/bcrypt"
)

// AuthHandler handles authentication-related requests.
type AuthHandler struct {
	authManager    *auth.Manager
	tokenMgr       *middleware.TokenManager
	userRepo       *repository.UserRepository
	appRedirectURL string
}

// NewAuthHandler creates a new AuthHandler.
func NewAuthHandler(manager *auth.Manager, tokenMgr *middleware.TokenManager, appRedirectURL string) *AuthHandler {
	return &AuthHandler{
		authManager:    manager,
		tokenMgr:       tokenMgr,
		userRepo:       repository.NewUserRepository(),
		appRedirectURL: appRedirectURL,
	}
}

// GetProviders returns available OAuth2/OIDC providers.
func (h *AuthHandler) GetProviders(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"providers": []string{"oidc", "password"}})
}

// OIDCLogin redirects the user to the OIDC provider's authorization page.
func (h *AuthHandler) OIDCLogin(c *gin.Context) {
	provider := h.authManager.GetProvider()
	authURL := provider.Config().AuthCodeURL("openfield-state")
	c.JSON(http.StatusOK, gin.H{
		"auth_url": authURL,
		"provider": "oidc",
	})
}

// Login handles password-based login for local accounts.
func (h *AuthHandler) Login(c *gin.Context) {
	var req struct {
		Username string `json:"username" binding:"required"`
		Password string `json:"password" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	user, err := h.userRepo.GetByUsername(req.Username)
	if err != nil {
		logger.Log.Error("failed to get user by username", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to login"})
		return
	}
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid username or password"})
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid username or password"})
		return
	}

	accessToken, err := h.tokenMgr.GenerateToken(user.ID, user.Email, user.Username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate token"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"access_token":  accessToken,
		"refresh_token": generateRefreshToken(),
		"token_type":    "Bearer",
		"expires_in":    86400,
		"user":          user,
	})
}

// Register completes registration for a newly-created OAuth user.
func (h *AuthHandler) Register(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req struct {
		Username string `json:"username" binding:"required"`
		Nickname string `json:"nickname" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	user, err := h.userRepo.UpdateProfile(userID, req.Username, req.Nickname)
	if err != nil {
		if errors.Is(err, repository.ErrUsernameTaken) {
			c.JSON(http.StatusConflict, gin.H{"error": "username already taken"})
			return
		}
		logger.Log.Error("failed to update profile", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to complete registration"})
		return
	}

	c.JSON(http.StatusOK, user)
}

// OIDCCallback handles the OIDC callback and returns JWT tokens.
func (h *AuthHandler) OIDCCallback(c *gin.Context) {
	code := c.Query("code")
	if code == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing authorization code"})
		return
	}

	user, err := h.authManager.Authenticate(c.Request.Context(), code)
	if err != nil {
		logger.Log.Error("oidc authentication failed", "error", err)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication failed"})
		return
	}

	accessToken, err := h.tokenMgr.GenerateToken(user.ID, user.Email, user.Username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate token"})
		return
	}

	if h.appRedirectURL != "" {
		redirect := fmt.Sprintf("%s?access_token=%s&username=%s&email=%s&avatar_url=%s&needs_registration=%t",
			h.appRedirectURL, accessToken, url.QueryEscape(user.Username), url.QueryEscape(user.Email), url.QueryEscape(user.AvatarURL), user.NeedsRegistration)
		c.Redirect(http.StatusFound, redirect)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"access_token":  accessToken,
		"refresh_token": generateRefreshToken(),
		"token_type":    "Bearer",
		"expires_in":    86400,
		"user":          user,
	})
}

// RefreshToken exchanges a refresh token for a new access token.
func (h *AuthHandler) RefreshToken(c *gin.Context) {
	var req struct {
		RefreshToken string `json:"refresh_token" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	userID, err := h.validateRefreshToken(req.RefreshToken)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid refresh token"})
		return
	}

	user, err := h.userRepo.GetByID(userID)
	if err != nil || user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "user not found"})
		return
	}

	accessToken, err := h.tokenMgr.GenerateToken(user.ID, user.Email, user.Username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate token"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"access_token": accessToken,
		"token_type":   "Bearer",
		"expires_in":   86400,
	})
}

// generateRefreshToken creates a random refresh token string.
func generateRefreshToken() string {
	return time.Now().Format(time.RFC3339Nano) + "-" + randomString(32)
}

func randomString(n int) string {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = chars[time.Now().UnixNano()%int64(len(chars))]
	}
	return string(b)
}

// validateRefreshToken checks if the refresh token is valid.
func (h *AuthHandler) validateRefreshToken(token string) (int64, error) {
	return repository.ValidateRefreshToken(token)
}
