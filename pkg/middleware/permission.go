package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/logger"
	"github.com/openfield/server/pkg/repository"
)

// PermissionMiddlewareFactory produces permission-checking middleware.
type PermissionMiddlewareFactory struct {
	permRepo *repository.PermissionRepository
}

// NewPermissionMiddlewareFactory creates a new factory.
func NewPermissionMiddlewareFactory() *PermissionMiddlewareFactory {
	return &PermissionMiddlewareFactory{
		permRepo: repository.NewPermissionRepository(),
	}
}

// Permit checks whether the user holds the given permission without aborting the request.
func (f *PermissionMiddlewareFactory) Permit(userID int64, key string) (bool, error) {
	return f.permRepo.HasPermission(userID, key)
}

// Require returns middleware that requires the user to hold the given permission.
func (f *PermissionMiddlewareFactory) Require(key string) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := GetUserID(c)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			c.Abort()
			return
		}
		allowed, err := f.permRepo.HasPermission(userID, key)
		if err != nil {
			logger.Log.Error("permission check failed", "error", err, "user_id", userID)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "permission check failed"})
			c.Abort()
			return
		}
		if !allowed {
			c.JSON(http.StatusForbidden, gin.H{"error": "permission denied", "permission": key})
			c.Abort()
			return
		}
		c.Next()
	}
}
