package middleware

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/config"
)

// localhostOrigin matches local web origins such as http://localhost:54532,
// http://127.0.0.1:<port> and http://[::1]:<port>. Flutter web dev servers run
// on random local ports, so these are always allowed regardless of the
// configured allow-list. A local origin can only come from the browser of a
// user on the same machine, so permitting it never weakens the server for
// remote attackers.
var localhostOrigin = regexp.MustCompile(`^https?://(localhost|127\.0\.0\.1|\[::1\])(:\d+)?$`)

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
			allowed := allowAll || localhostOrigin.MatchString(origin)
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
