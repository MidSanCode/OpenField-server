package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/middleware"
)

// RegisterRoutes registers the push service routes.
func RegisterRoutes(r *gin.Engine, wsHandler *WsHandler, ticketHandler *TicketHandler) {
	api := r.Group("/api/v1")
	{
		ws := api.Group("/ws")
		ws.Use(middleware.GatewayAuthMiddleware())
		{
			// Native clients connect directly with their JWT (X-User-ID set by
			// the gateway); browsers mint a one-time ticket via POST first.
			ws.GET("", wsHandler.Connect)
			ws.POST("", ticketHandler.Create)
		}
	}
}
