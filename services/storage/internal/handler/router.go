package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/middleware"
)

// RegisterRoutes registers all storage service routes.
// Every route requires a validated gateway identity (X-User-ID), except the
// internal file proxy which serves objects publicly (parity with direct
// bucket URLs).
func RegisterRoutes(r *gin.Engine, attHandler *AttachmentHandler) {
	// Internal proxy mode: GET /api/v1/files/<physical-bucket>/<object-key>.
	// The gateway forwards this prefix without authentication.
	r.GET("/api/v1/files/*path", attHandler.ServeFile)

	api := r.Group("/api/v1")
	api.Use(middleware.GatewayAuthMiddleware())
	{
		att := api.Group("/attachments")
		{
			att.POST("", attHandler.Upload)
			att.GET("", attHandler.ListByUser)
			att.GET("/:id", attHandler.Get)
			att.GET("/by-hash/:hash", attHandler.Reuse)
			att.DELETE("/:id", attHandler.Delete)

			chunk := att.Group("/chunk")
			{
				chunk.POST("/init", attHandler.ChunkInit)
				chunk.GET("/:upload_id", attHandler.ChunkStatus)
				chunk.POST("/:upload_id/:index", attHandler.ChunkUpload)
				chunk.POST("/:upload_id/complete", attHandler.ChunkComplete)
			}
		}
		api.GET("/storage/usage", attHandler.Usage)
	}
}
