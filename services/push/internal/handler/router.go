package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/middleware"
)

// RegisterRoutes registers the push service routes.
func RegisterRoutes(r *gin.Engine, wsHandler *WsHandler) {
	api := r.Group("/api/v1")
	{
		ws := api.Group("/ws")
		ws.Use(middleware.GatewayAuthMiddleware())
		{
			ws.GET("", wsHandler.Connect)
		}
	}
}
