package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/openfield/server/pkg/middleware"
	"github.com/openfield/server/pkg/permission"
)

// RegisterRoutes wires all plugin routes onto the engine.
func RegisterRoutes(r *gin.Engine, h *PluginHandler) {
	// Defense-in-depth gate for admin routes: re-checks the plugin.manage
	// permission against the database even though the gateway already did.
	manage := middleware.NewPermissionMiddlewareFactory().Require(permission.PluginManage)

	api := r.Group("/api/v1")
	{
		// Public catalog (readable without login so the store page can render
		// before sign-in; installs still require an authenticated download).
		api.GET("/plugins", h.List)
		api.GET("/plugins/:id", h.Get)

		// Download requires a valid token (counted per install).
		api.GET("/plugins/:id/download", h.Download)

		// Admin management.
		admin := api.Group("/plugins/admin", manage)
		{
			admin.POST("/upload", h.Upload)
			admin.PUT("/:id/publish", h.Publish)
			admin.PUT("/:id/unpublish", h.Unpublish)
			admin.DELETE("/:id", h.Delete)
		}
	}
}
