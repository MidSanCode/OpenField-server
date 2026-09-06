package handler

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
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
	authManager       *auth.Manager
	tokenMgr          *middleware.TokenManager
	userRepo          *repository.UserRepository
	punishRepo        *repository.PunishmentRepository
	appRedirectURL    string
	webRedirectURL    string
	refreshExpiryDays int
}

// NewAuthHandler creates a new AuthHandler. Both the app-protocol and the web
// redirect URLs are honoured: OIDCLogin reads a `flow` query parameter to
// decide which target to send the browser to on callback.
func NewAuthHandler(manager *auth.Manager, tokenMgr *middleware.TokenManager, appRedirectURL, webRedirectURL string, refreshExpiryDays int) *AuthHandler {
	return &AuthHandler{
		authManager:       manager,
		tokenMgr:          tokenMgr,
		userRepo:          repository.NewUserRepository(),
		punishRepo:        repository.NewPunishmentRepository(),
		appRedirectURL:    appRedirectURL,
		webRedirectURL:    webRedirectURL,
		refreshExpiryDays: refreshExpiryDays,
	}
}

// bannedMessage returns a 4xx JSON response when the user is currently banned,
// otherwise nil (proceed with the request). Temporary bans automatically lift
// once banned_until passes.
func (h *AuthHandler) bannedResponse(c *gin.Context, user *model.User) bool {
	now := time.Now()
	if user != nil && user.Status == "banned" {
		if user.BannedUntil != nil && !now.Before(*user.BannedUntil) {
			return false // auto-lifted temporary ban
		}
		until := user.BannedUntil
		if until == nil {
			c.JSON(http.StatusForbidden, gin.H{"error": "account is permanently banned"})
		} else {
			c.JSON(http.StatusForbidden, gin.H{
				"error":        "account is temporarily banned",
				"banned_until": *until,
			})
		}
		return true
	}
	return false
}

// accessExpiresIn returns the access token lifetime in seconds.
func (h *AuthHandler) accessExpiresIn() int {
	return h.tokenMgr.ExpirySeconds()
}

// refreshExpiresIn returns the refresh token lifetime in seconds.
func (h *AuthHandler) refreshExpiresIn() int {
	days := h.refreshExpiryDays
	if days <= 0 {
		days = 30
	}
	return days * 24 * 60 * 60
}

// GetProviders returns available OAuth2/OIDC providers.
func (h *AuthHandler) GetProviders(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"providers": []string{"oidc", "password"}})
}

// OIDCLogin redirects the user to the OIDC provider's authorization page.
// A fresh single-use state nonce is minted for every login attempt and stored
// server-side; the callback rejects any code exchange that does not carry it,
// which blocks login CSRF and callback replay.
//
// The optional `flow` query parameter (values: "app" | "web") tells the
// callback which redirect URL to use. Web logins bounce to the frontend's
// callback route so the browser does not try to open the openfield://
// protocol which most browsers refuse silently.
func (h *AuthHandler) OIDCLogin(c *gin.Context) {
	state, err := randomState()
	if err != nil {
		logger.Log.Error("failed to generate oidc state", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start login"})
		return
	}
	flow := c.Query("flow")
	if flow != "web" {
		flow = "app"
	}
	if err := repository.IssueOIDCState(state, flow); err != nil {
		logger.Log.Error("failed to store oidc state", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start login"})
		return
	}

	provider := h.authManager.GetProvider()
	authURL := provider.Config().AuthCodeURL(state)
	c.JSON(http.StatusOK, gin.H{
		"auth_url": authURL,
		"provider": "oidc",
		"flow":     flow,
	})
}

// randomState returns a 256-bit random hex nonce for OIDC flows.
func randomState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
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

	// Brute-force protection: reject attempts when the source IP or this
	// account has burned its failure budget.
	ip := clientAddress(c)
	accountKey := strings.ToLower(req.Username)
	if retry := maxDuration(loginIPLimiter.RetryAfter(ip), loginAccountLimiter.RetryAfter(accountKey)); retry > 0 {
		lockedResponse(c, retry)
		return
	}

	user, err := h.userRepo.GetByUsername(req.Username)
	if err != nil {
		logger.Log.Error("failed to get user by username", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to login"})
		return
	}
	if user == nil {
		loginIPLimiter.Fail(ip)
		loginAccountLimiter.Fail(accountKey)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid username or password"})
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		loginIPLimiter.Fail(ip)
		loginAccountLimiter.Fail(accountKey)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid username or password"})
		return
	}
	loginIPLimiter.Reset(ip)
	loginAccountLimiter.Reset(accountKey)

	if h.bannedResponse(c, user) {
		return
	}
	if user.DeletedAt != nil {
		loginIPLimiter.Reset(ip)
		loginAccountLimiter.Reset(accountKey)
		c.JSON(http.StatusForbidden, gin.H{"error": "account scheduled for deletion"})
		return
	}

	accessToken, err := h.tokenMgr.GenerateToken(user.ID, user.Email, user.Username, false)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate token"})
		return
	}

	refreshToken, err := generateRefreshToken()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate refresh token"})
		return
	}
	device, devIP := clientDevice(c)
	knownDevice, err := repository.CreateSession(user.ID, refreshToken, h.refreshExpiresIn(), device, devIP)
	if err != nil {
		logger.Log.Error("failed to create session", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to login"})
		return
	}
	if !knownDevice {
		h.notifyNewDeviceLogin(user.ID, device, devIP)
	}

	c.JSON(http.StatusOK, gin.H{
		"access_token":       accessToken,
		"refresh_token":      refreshToken,
		"token_type":         "Bearer",
		"expires_in":         h.accessExpiresIn(),
		"refresh_expires_in": h.refreshExpiresIn(),
		"user":               user,
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
		code = c.PostForm("code")
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

	// Plain login: the state must be a nonce we issued and not yet consumed.
	flow, err := repository.ConsumeOIDCState(state)
	if err != nil {
		logger.Log.Warn("oidc login rejected", "reason", "invalid or expired state")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid oidc state"})
		return
	}

	user, err := h.authManager.Authenticate(c.Request.Context(), code)
	if err != nil {
		logger.Log.Error("oidc authentication failed", "error", err)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication failed"})
		return
	}

	if h.bannedResponse(c, user) {
		return
	}
	if user.DeletedAt != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "account scheduled for deletion"})
		return
	}

	accessToken, err := h.tokenMgr.GenerateToken(user.ID, user.Email, user.Username, user.NeedsRegistration)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate token"})
		return
	}

	refreshToken, err := generateRefreshToken()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate refresh token"})
		return
	}
	device, ip := clientDevice(c)
	knownDevice, err := repository.CreateSession(user.ID, refreshToken, h.refreshExpiresIn(), device, ip)
	if err != nil {
		logger.Log.Error("failed to create session", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "authentication failed"})
		return
	}
	if !knownDevice {
		h.notifyNewDeviceLogin(user.ID, device, ip)
	}

	// Web sign-ins get redirected to the frontend callback page rather than
	// the app-protocol deep link, which most browsers refuse to open.
	if flow == "web" && h.webRedirectURL != "" {
		webLink := fmt.Sprintf("%s?access_token=%s&refresh_token=%s&expires_in=%d&refresh_expires_in=%d&username=%s&email=%s&avatar_url=%s&needs_registration=%t",
			h.webRedirectURL, accessToken, refreshToken, h.accessExpiresIn(), h.refreshExpiresIn(), url.QueryEscape(user.Username), url.QueryEscape(user.Email), url.QueryEscape(user.AvatarURL), user.NeedsRegistration)
		c.Redirect(http.StatusFound, webLink)
		return
	}

	if h.appRedirectURL != "" {
		appLink := fmt.Sprintf("%s?access_token=%s&refresh_token=%s&expires_in=%d&refresh_expires_in=%d&username=%s&email=%s&avatar_url=%s&needs_registration=%t",
			h.appRedirectURL, accessToken, refreshToken, h.accessExpiresIn(), h.refreshExpiresIn(), url.QueryEscape(user.Username), url.QueryEscape(user.Email), url.QueryEscape(user.AvatarURL), user.NeedsRegistration)
		if strings.HasPrefix(h.appRedirectURL, "openfield://") || !strings.HasPrefix(h.appRedirectURL, "http") {
			c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(loginResultPage(appLink, accessToken, refreshToken)))
			return
		}
		c.Redirect(http.StatusFound, appLink)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"access_token":       accessToken,
		"refresh_token":      refreshToken,
		"token_type":         "Bearer",
		"expires_in":         h.accessExpiresIn(),
		"refresh_expires_in": h.refreshExpiresIn(),
		"user":               user,
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

	iconClass := "ok"
	iconGlyph := "&#10003;"
	if !ok {
		iconClass = "err"
		iconGlyph = "&#10005;"
	}

	return renderResultPage(iconClass, iconGlyph, title, desc, btnLabel, btnHref, "", "")
}

// loginResultPage renders a small HTML page shown after a successful OIDC login.
// It offers a deep-link button into the app plus copyable access and refresh
// tokens as a fallback when the app's custom protocol cannot be opened (e.g.
// deep links are blocked on the current OS). Users can paste the two tokens
// into the app's token login to sign in with auto-refresh support.
func loginResultPage(appLink, accessToken, refreshToken string) string {
	return renderResultPage("ok", "&#10003;", "登录成功",
		"你的账号已登录成功。点击下方按钮打开 OpenField；若无法打开，请复制下方访问令牌和刷新令牌，并在应用的「令牌登录」中粘贴完成登录。",
		"打开 OpenField", appLink, accessToken, refreshToken)
}

// renderResultPage is the shared HTML template for OIDC result pages. When a
// token is non-empty, a copyable token block is rendered as a deep-link fallback.
func renderResultPage(iconClass, iconGlyph, title, desc, btnLabel, btnHref, accessToken, refreshToken string) string {
	tokenBlock := ""
	if accessToken != "" {
		tokenBlock = fmt.Sprintf(`<div class="token-wrap">
    <div class="token-box">
      <code id="openfield-token">%s</code>
      <button class="copy-btn" onclick="copyToken('openfield-token', this)">复制访问令牌</button>
    </div>
    %s
    <p class="token-hint">若按钮无法打开 OpenField，请依次点击「复制访问令牌」和「复制刷新令牌」，然后在应用的「令牌登录」中先粘贴访问令牌，再换行粘贴刷新令牌登录。</p>
  </div>`, accessToken, refreshBlock(refreshToken))
	}
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>OpenField</title>
<style>
  body { margin:0; font-family: -apple-system, "Segoe UI", Roboto, "PingFang SC", "Microsoft YaHei", sans-serif;
         background:#f4f6f8; display:flex; align-items:center; justify-content:center; min-height:100vh; }
  .card { background:#fff; border-radius:16px; padding:40px 48px; text-align:center; max-width:460px;
           box-shadow:0 4px 24px rgba(0,0,0,.08); }
  .icon { width:64px; height:64px; border-radius:50%%; margin:0 auto 20px; display:flex; align-items:center;
           justify-content:center; font-size:34px; color:#fff; }
  .ok { background:#22c55e; } .err { background:#ef4444; }
  h1 { font-size:22px; margin:0 0 8px; color:#111827; }
  p { color:#6b7280; font-size:14px; line-height:1.6; margin:0 0 24px; }
  a.button { display:inline-block; background:#2563eb; color:#fff; text-decoration:none; padding:10px 22px;
             border-radius:8px; font-size:14px; font-weight:500; }
  a.button:hover { background:#1d4ed8; }
  .token-wrap { margin-top:20px; text-align:left; }
  .token-box { display:flex; align-items:stretch; gap:8px; background:#f9fafb; border:1px solid #e5e7eb;
               border-radius:8px; padding:8px; }
  .token-box code { flex:1; font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
                    font-size:12px; color:#111827; word-break:break-all; background:transparent;
                    border:none; resize:none; padding:8px; min-height:64px; }
  .copy-btn { flex-shrink:0; align-self:stretch; background:#2563eb; color:#fff; border:none; border-radius:6px;
              padding:0 16px; font-size:13px; font-weight:500; cursor:pointer; }
  .copy-btn:hover { background:#1d4ed8; }
  .token-hint { font-size:12px; color:#6b7280; margin:8px 0 0; }
</style>
</head>
<body>
  <div class="card">
    <div class="icon %s">%s</div>
    <h1>%s</h1>
    <p>%s</p>
    <a class="button" href="%s">%s</a>
    %s
    <script>
      function copyToken(id, btn) {
        var code = document.getElementById(id);
        if (navigator.clipboard && navigator.clipboard.writeText) {
          navigator.clipboard.writeText(code.textContent).then(function() {
            btn.textContent = '已复制';
          });
        } else {
          var range = document.createRange();
          range.selectNodeContents(code);
          var sel = window.getSelection();
          sel.removeAllRanges();
          sel.addRange(range);
          document.execCommand('copy');
          var btn2 = btn;
          btn2.textContent = '已复制';
        }
      }
      setTimeout(function() { window.location.href = "%s"; }, 1500);
    </script>
  </div>
</body>
</html>`,
		iconClass, iconGlyph, title, desc, btnHref, btnLabel, tokenBlock, btnHref)
}

// refreshBlock returns the refresh-token copy block for the login result page.
func refreshBlock(refreshToken string) string {
	if refreshToken == "" {
		return ""
	}
	return fmt.Sprintf(`<div class="token-box" style="margin-top:8px;">
      <code id="openfield-refresh">%s</code>
      <button class="copy-btn" onclick="copyToken('openfield-refresh', this)">复制刷新令牌</button>
    </div>`, refreshToken)
}

// maxDuration returns the larger of two durations.
func maxDuration(a, b time.Duration) time.Duration {
	if a >= b {
		return a
	}
	return b
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

// RefreshToken exchanges a refresh token for a new access token, rotating the
// refresh token so each refresh token is single-use.
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

	if h.bannedResponse(c, user) {
		return
	}
	if user.DeletedAt != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "account scheduled for deletion"})
		return
	}

	accessToken, err := h.tokenMgr.GenerateToken(user.ID, user.Email, user.Username, user.NeedsRegistration)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate token"})
		return
	}

	newRefreshToken, err := generateRefreshToken()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate refresh token"})
		return
	}

	// Rotate: invalidate the old refresh token and persist the new one.
	if err := repository.RotateRefreshToken(req.RefreshToken, newRefreshToken, user.ID, h.refreshExpiresIn()); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid refresh token"})
			return
		}
		logger.Log.Error("failed to rotate refresh token", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to refresh token"})
		return
	}
	_, ip := clientDevice(c)
	_ = repository.TouchSession(newRefreshToken, ip)

	c.JSON(http.StatusOK, gin.H{
		"access_token":       accessToken,
		"refresh_token":      newRefreshToken,
		"token_type":         "Bearer",
		"expires_in":         h.accessExpiresIn(),
		"refresh_expires_in": h.refreshExpiresIn(),
	})
}

// generateRefreshToken creates a cryptographically random refresh token string.
// Characters are drawn with rejection sampling so every symbol is equally
// likely (62 does not divide 256, plain modulo would bias early letters).
func generateRefreshToken() (string, error) {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	const length = 32
	max := byte(256 - 256%len(chars))
	out := make([]byte, 0, length)
	buf := make([]byte, 64)
	for len(out) < length {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, v := range buf {
			if v >= max {
				continue
			}
			out = append(out, chars[int(v)%len(chars)])
			if len(out) == length {
				break
			}
		}
	}
	return time.Now().Format(time.RFC3339Nano) + "-" + string(out), nil
}

// validateRefreshToken checks if the refresh token is valid.
func (h *AuthHandler) validateRefreshToken(token string) (int64, error) {
	return repository.ValidateRefreshToken(token)
}

// --- helpers shared with the QR + sessions flow ---

// clientDevice returns a short label describing the client (user-supplied or
// parsed from the User-Agent) and the originating IP, used as session metadata.
func clientDevice(c *gin.Context) (device, ip string) {
	device = c.GetHeader("X-Client-Device")
	if device == "" {
		ua := c.GetHeader("User-Agent")
		device = guessDevice(ua)
	}
	ip = clientAddress(c)
	return
}

// guessDevice picks a short, human-readable device label from a User-Agent.
// Unknown clients get the trimmed raw UA so they can still be distinguished.
func guessDevice(ua string) string {
	low := strings.ToLower(ua)
	switch {
	case strings.Contains(low, "openfield-ios"):
		return "iOS App"
	case strings.Contains(low, "openfield-android"):
		return "Android App"
	case strings.Contains(low, "openfield-macos"):
		return "macOS App"
	case strings.Contains(low, "openfield-windows"):
		return "Windows App"
	case strings.Contains(low, "openfield-linux"):
		return "Linux App"
	case strings.Contains(low, "openfield-web"):
		return "Web App"
	}
	if strings.HasPrefix(strings.ToLower(ua), "mozilla/") || strings.Contains(low, "chrome") || strings.Contains(low, "safari") || strings.Contains(low, "firefox") {
		return "Web Browser"
	}
	if ua == "" {
		return "Unknown Device"
	}
	if len(ua) > 80 {
		return ua[:80]
	}
	return ua
}

// notifyNewDeviceLogin drops a "new device" notification in the user's inbox.
func (h *AuthHandler) notifyNewDeviceLogin(userID int64, device, ip string) {
	data, _ := json.Marshal(map[string]string{"device": device, "ip": ip})
	title := "新设备登录"
	body := "你的账号在 " + device + " 上登录。如非本人操作，请尽快修改密码并注销其他会话。"
	_ = repository.CreateNotification(userID, "auth.new_device", title, body, data)
}

// CreateQrLogin mints a fresh pending handshake code the caller can render as
// a QR image. The code is single-use and short-lived.
func (h *AuthHandler) CreateQrLogin(c *gin.Context) {
	var req struct {
		Device string `json:"device"`
	}
	_ = c.ShouldBindJSON(&req)
	code, err := randomState()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create code"})
		return
	}
	if req.Device == "" {
		req.Device = "Unknown Device"
	}
	if err := repository.CreateQrLogin(code, req.Device); err != nil {
		logger.Log.Error("failed to create qr login", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create code"})
		return
	}
	// Mirrors repository.qrLoginTTL (5 minutes); the client renders a
	// countdown and a refresh action from this value.
	c.JSON(http.StatusOK, gin.H{"code": code, "expires_in": 300})
}

// PollQrLogin returns the current state of a handshake. Once a phone approves
// it, the poll response carries the access + refresh tokens that sign the
// requesting device in.
func (h *AuthHandler) PollQrLogin(c *gin.Context) {
	code := c.Param("code")
	qr, err := repository.GetQrLogin(code)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "code not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to poll code"})
		return
	}
	resp := gin.H{"code": qr.Code, "status": qr.Status}
	if qr.Status == "confirmed" && qr.AccessToken != "" {
		resp["access_token"] = qr.AccessToken
		resp["refresh_token"] = qr.RefreshToken
		resp["expires_in"] = h.accessExpiresIn()
		resp["refresh_expires_in"] = h.refreshExpiresIn()
	}
	c.JSON(http.StatusOK, resp)
}

// ApproveQrLogin consumes a pending handshake and grants tokens signed for
// the approving user.
func (h *AuthHandler) ApproveQrLogin(c *gin.Context) {
	userID, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	code := c.Param("code")
	qr, err := repository.GetQrLogin(code)
	if err != nil || qr == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "code not found"})
		return
	}
	if qr.Status != "pending" {
		c.JSON(http.StatusConflict, gin.H{"error": "code already used"})
		return
	}
	user, err := h.userRepo.GetByID(userID)
	if err != nil || user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "user not found"})
		return
	}
	if user.DeletedAt != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "account scheduled for deletion"})
		return
	}
	accessToken, err := h.tokenMgr.GenerateToken(user.ID, user.Email, user.Username, false)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate token"})
		return
	}
	refreshToken, err := generateRefreshToken()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate refresh token"})
		return
	}
	device, ip := clientDevice(c)
	if _, err := repository.CreateSession(user.ID, refreshToken, h.refreshExpiresIn(), device, ip); err != nil {
		logger.Log.Error("failed to record qr session", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to approve"})
		return
	}
	if err := repository.ConfirmQrLogin(code, user.ID, accessToken, refreshToken); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "code already used"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "confirmed"})
}
