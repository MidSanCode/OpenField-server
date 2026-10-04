package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// SecurityHeaders adds transport and browser-hardening response headers.
//
// HSTS is the meaningful one: without it a client that reaches the API over
// plain HTTP once (a typed URL, a bookmark, a downgraded redirect) keeps using
// HTTP, so an on-path attacker can read or rewrite traffic. The header is only
// sent on connections that are actually secure — advertising it over HTTP is
// meaningless and, behind a TLS-terminating proxy, the request usually still
// arrives as cleartext to this process, so the proxy's own scheme signal is
// what we check.
func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		if requestIsSecure(c) {
			// Two years, including subdomains: the usual baseline for an API
			// that is HTTPS-only. Deliberately no preload token, since that is
			// a one-way commitment an operator must make knowingly.
			c.Header("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		}
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Referrer-Policy", "no-referrer")
		c.Next()
	}
}

// requestIsSecure reports whether the client's connection to the edge was over
// TLS. c.Request.TLS covers direct termination; X-Forwarded-Proto covers the
// common deployment where a proxy terminates TLS and forwards plain HTTP.
func requestIsSecure(c *gin.Context) bool {
	if c.Request.TLS != nil {
		return true
	}
	return strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https")
}
