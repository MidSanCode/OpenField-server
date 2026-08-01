package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/middleware"
)

// RegisterRoutes registers all storage service routes.
// Every route requires a validated gateway identity (X-User-ID).
func RegisterRoutes(r *gin.Engine, attHandler *AttachmentHandler) {
	api := r.Group("/api/v1")
	api.Use(middleware.GatewayAuthMiddleware())
	{
		att := api.Group("/attachments")
		{
			att.POST("", attHandler.Upload)
			att.GET("", attHandler.ListByUser)
			att.GET("/:id", attHandler.Get)
			att.DELETE("/:id", attHandler.Delete)
		}
		api.GET("/storage/usage", attHandler.ListByUser)
	}
}
