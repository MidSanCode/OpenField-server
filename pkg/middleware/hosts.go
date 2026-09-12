package middleware

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/config"
)

// normalizeHost lower-cases a host header value and strips a trailing numeric
// port (":443"), so "API.A.com:8443" and "api.a.com" compare equal. IPv6
// literals such as "[::1]:8080" are handled: the port suffix after the last
// colon is removed while the bracketed address stays intact.
func normalizeHost(h string) string {
	host := strings.TrimSpace(h)
	if i := strings.LastIndex(host, ":"); i >= 0 {
		if _, err := strconv.Atoi(host[i+1:]); err == nil {
			host = host[:i]
		}
	}
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

// HostAllowlist returns a middleware that rejects requests whose Host header
// is not listed in server.allowed_hosts (e.g. ["api.a.com", "api.b.com"]).
// This hardens the gateway against host-header injection and lets one
// deployment answer on several API domains. An empty allow-list keeps the
// previous behavior of accepting any Host. The check runs before routing so
// probes on wrong hosts never reach handlers.
func HostAllowlist(cfg *config.Config) gin.HandlerFunc {
	allowed := make(map[string]struct{}, len(cfg.Server.AllowedHosts))
	for _, h := range cfg.Server.AllowedHosts {
		if n := normalizeHost(h); n != "" {
			allowed[n] = struct{}{}
		}
	}
	if len(allowed) == 0 {
		return func(c *gin.Context) { c.Next() }
	}
	return func(c *gin.Context) {
		if _, ok := allowed[normalizeHost(c.Request.Host)]; !ok {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "host not allowed"})
			return
		}
		c.Next()
	}
}
