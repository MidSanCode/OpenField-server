package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/logger"
)

// Recovery converts a handler panic into a JSON 500 response instead of Gin's
// default HTML/plain-text error page, so API clients always receive a
// machine-readable body and the process keeps serving other requests.
func Recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				logger.Log.Error("panic recovered",
					"error", err,
					"method", c.Request.Method,
					"path", c.Request.URL.Path,
				)
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
					"error": "internal server error",
				})
			}
		}()
		c.Next()
	}
}

// NotFound responds with a JSON 404 for unknown routes so clients never have
// to parse Gin's HTML 404 page.
func NotFound() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
	}
}

// MethodNotAllowed responds with a JSON 405 when a route exists for a
// different HTTP method.
func MethodNotAllowed() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusMethodNotAllowed, gin.H{"error": "method not allowed"})
	}
}
