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
	"github.com/openfield/server/pkg/model"
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

// OIDCBind starts the OIDC account-binding flow for the authenticated user.
// It returns an authorization URL whose state carries the user's identity.
func (h *AuthHandler) OIDCBind(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	state, err := h.tokenMgr.GeneratePurposeToken(userID, "bind")
	if err != nil {
		logger.Log.Error("failed to generate bind state", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start binding"})
		return
	}

	provider := h.authManager.GetProvider()
	authURL := provider.Config().AuthCodeURL(state)
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
		Bio      string `json:"bio"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	user, err := h.userRepo.UpdateProfile(userID, req.Username, req.Nickname, req.Bio)
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
// If the state parameter is a valid "bind" purpose token, the callback instead
// links the OAuth identity to the account referenced by the token.
func (h *AuthHandler) OIDCCallback(c *gin.Context) {
	// Accept code from both query string (GET redirect) and form body (POST).
	code := c.Query("code")
	if code == "" {
		code = c.PostFormValue("code")
	}
	if code == "" {
		logger.Log.Warn("oidc callback missing code", "query", c.Request.URL.Query(), "method", c.Request.Method)
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing authorization code"})
		return
	}

	// Surface OIDC provider errors (e.g. access_denied, invalid_request).
	if oidcErr := c.Query("error"); oidcErr != "" {
		logger.Log.Warn("oidc provider error", "error", oidcErr, "desc", c.Query("error_description"))
		c.JSON(http.StatusBadRequest, gin.H{"error": oidcErr, "description": c.Query("error_description")})
		return
	}

	state := c.Query("state")
	if bindUserID, err := h.tokenMgr.ParsePurposeToken(state, "bind"); err == nil {
		h.handleOIDCBind(c, code, bindUserID)
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

// handleOIDCBind completes the account-binding flow. The browser is shown an
// HTML result page that also links back into the app via the app redirect URL.
func (h *AuthHandler) handleOIDCBind(c *gin.Context, code string, userID int64) {
	user, err := h.authManager.Bind(c.Request.Context(), code, userID)
	if err != nil {
		logger.Log.Error("oidc bind failed", "error", err, "user_id", userID)
		reason := "unknown"
		if errors.Is(err, auth.ErrOAuth2AlreadyBound) {
			reason = "taken"
		}
		appLink := ""
		if h.appRedirectURL != "" {
			appLink = fmt.Sprintf("%s?bind=error&reason=%s", h.appRedirectURL, url.QueryEscape(reason))
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(bindResultPage("error", reason, "", appLink)))
		return
	}

	appLink := ""
	if h.appRedirectURL != "" {
		appLink = fmt.Sprintf("%s?bind=success&name=%s", h.appRedirectURL, url.QueryEscape(boundName(user)))
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(bindResultPage("success", "", boundName(user), appLink)))
}

// boundName returns the human-readable OAuth identity label for display.
func boundName(user *model.User) string {
	if user == nil {
		return ""
	}
	if user.OAuth2Username != "" {
		return user.OAuth2Username
	}
	if user.Email != "" {
		return user.Email
	}
	return user.OAuth2ID
}

// bindResultPage renders a small HTML page shown in the browser after an OIDC
// account-binding attempt. It displays the outcome and offers a button that
// deep-links back into the desktop/mobile app.
func bindResultPage(result, reason, accountName, appLink string) string {
	ok := result == "success"
	title := "绑定成功"
	desc := "你的账号已成功关联 OIDC 身份，可以关闭此页面。"
	if accountName != "" {
		desc = fmt.Sprintf("已绑定账号：%s。下次可直接使用该账号登录 OpenField。", accountName)
	}
	btnLabel := "打开 OpenField"
	btnHref := appLink
	if !ok {
		title = "绑定失败"
		desc = "无法完成账号绑定，请回到应用内重试。"
		if reason == "taken" {
			desc = "该 OIDC 身份已被其他账号绑定，请回到应用内重试。"
		}
		btnLabel = "返回应用"
		btnHref = hmmAppFallback
	}

	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>OpenField 账号绑定</title>
<style>
  body { margin:0; font-family: -apple-system, "Segoe UI", Roboto, "PingFang SC", "Microsoft YaHei", sans-serif;
         background:#f4f6f8; display:flex; align-items:center; justify-content:center; min-height:100vh; }
  .card { background:#fff; border-radius:16px; padding:40px 48px; text-align:center; max-width:420px;
          box-shadow:0 4px 24px rgba(0,0,0,.08); }
  .icon { width:64px; height:64px; border-radius:50%%; margin:0 auto 20px; display:flex; align-items:center;
          justify-content:center; font-size:34px; color:#fff; }
  .ok { background:#22c55e; } .err { background:#ef4444; }
  h1 { font-size:22px; margin:0 0 8px; color:#111827; }
  p { color:#6b7280; font-size:14px; line-height:1.6; margin:0 0 24px; }
  a.button { display:inline-block; background:#2563eb; color:#fff; text-decoration:none; padding:10px 22px;
             border-radius:8px; font-size:14px; font-weight:500; }
  a.button:hover { background:#1d4ed8; }
</style>
</head>
<body>
  <div class="card">
    <div class="icon %s">%s</div>
    <h1>%s</h1>
    <p>%s</p>
    <a class="button" href="%s">%s</a>
  </div>
</body>
</html>`,
		iconClass(ok), iconGlyph(ok), title, desc, btnHref, btnLabel)
}

// hmmAppFallback is a safe placeholder when no app redirect URL is configured.
const hmmAppFallback = "#"

func iconClass(ok bool) string {
	if ok {
		return "ok"
	}
	return "err"
}

func iconGlyph(ok bool) string {
	if ok {
		return "&#10003;"
	}
	return "&#10005;"
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
