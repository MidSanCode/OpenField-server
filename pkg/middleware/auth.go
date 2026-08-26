package middleware

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/config"
	"github.com/openfield/server/pkg/logger"
)

// AuthMiddleware validates JWT tokens and sets user context.
func AuthMiddleware(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "missing authorization header"})
			c.Abort()
			return
		}

		tokenStr := strings.TrimPrefix(authHeader, "Bearer ")
		if tokenStr == authHeader {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid authorization header format"})
			c.Abort()
			return
		}

		userID, err := ParseToken(tokenStr, cfg.JWT.SecretKey)
		if err != nil {
			logger.Log.Warn("invalid token", "error", err)
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
			c.Abort()
			return
		}

		c.Set("user_id", userID)
		c.Next()
	}
}

// GetUserID extracts user ID from context.
func GetUserID(c *gin.Context) (int64, bool) {
	userID, exists := c.Get("user_id")
	if !exists {
		return 0, false
	}
	id, ok := userID.(int64)
	return id, ok
}

// UserIDHeader is the header used by the gateway to pass the authenticated user id to internal services.
const UserIDHeader = "X-User-ID"

// UserNeedsRegHeader carries the nreg flag from the gateway: when set to "1"
// the user has logged in via OIDC but has not completed it yet. Internal
// services refuse every privileged route until the flag is cleared.
const UserNeedsRegHeader = "X-User-Needs-Registration"

// GatewayAuthMiddleware is used by internal services to trust the user id set by the gateway.
// The gateway validates JWT and forwards the authenticated user id in the X-User-ID header.
func GatewayAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader(UserIDHeader)
		if header == "" {
			// WebSocket upgrades authenticate with a single-use ?ticket=
			// minted via POST /api/v1/ws (browsers cannot set headers).
			// Let them through; the handler redeems the ticket.
			if c.Query("ticket") != "" {
				c.Next()
				return
			}
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			c.Abort()
			return
		}
		userID, err := strconv.ParseInt(header, 10, 64)
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid user id"})
			c.Abort()
			return
		}
		c.Set("user_id", userID)
		c.Set("needs_registration", c.GetHeader(UserNeedsRegHeader) == "1")
		c.Next()
	}
}

// NeedsRegistration reports whether the current request was authenticated
// with an OIDC token whose owner has not completed registration yet.
func NeedsRegistration(c *gin.Context) bool {
	v, ok := c.Get("needs_registration")
	if !ok {
		return false
	}
	b, _ := v.(bool)
	return b
}
