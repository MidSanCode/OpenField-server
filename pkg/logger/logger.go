// Package logger provides the shared structured (JSON) logger and a Gin
// request-logging middleware used by every service.
package logger

import (
	"log/slog"
	"os"
	"time"

	"github.com/gin-gonic/gin"
)

// Log is the process-wide structured logger created by Init or InitWithLevel.
// Every package logs through it, so it must be initialized before first use.
var Log *slog.Logger

// Init initializes the logger with default settings.
func Init() {
	Log = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
}

// InitWithLevel initializes the logger with a custom level.
func InitWithLevel(level slog.Level) {
	Log = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	}))
}

// GinLogger returns a Gin middleware for logging requests.
func GinLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		query := c.Request.URL.RawQuery

		c.Next()

		latency := time.Since(start)
		status := c.Writer.Status()

		Log.Info("request",
			"method", c.Request.Method,
			"path", path,
			"query", query,
			"status", status,
			"latency", latency.Milliseconds(),
			"ip", c.ClientIP(),
			"user_agent", c.Request.UserAgent(),
		)
	}
}
