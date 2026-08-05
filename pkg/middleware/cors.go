package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/config"
)

// CORS returns a gin middleware that allows cross-origin requests from the
// configured web app origins. Because auth uses Bearer tokens (not cookies),
// the wildcard origin is safe to use when AllowAllOrigins is enabled.
func CORS(cfg *config.Config) gin.HandlerFunc {
	allowedOrigins := make(map[string]struct{})
	for _, o := range cfg.Server.AllowedOrigins {
		allowedOrigins[strings.TrimSuffix(o, "/")] = struct{}{}
	}

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		allowAll := cfg.Server.AllowAllOrigins || len(allowedOrigins) == 0

		if origin != "" {
			allowed := allowAll
			if !allowed {
				_, allowed = allowedOrigins[strings.TrimSuffix(origin, "/")]
			}
			if allowed {
				if allowAll {
					c.Header("Access-Control-Allow-Origin", "*")
				} else {
					c.Header("Access-Control-Allow-Origin", origin)
					c.Header("Vary", "Origin")
				}
			}
		}

		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Requested-With")
		c.Header("Access-Control-Max-Age", "86400")

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
