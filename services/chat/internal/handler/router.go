package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/middleware"
)

// RegisterRoutes registers all chat service routes.
func RegisterRoutes(r *gin.Engine, convHandler *ConversationHandler, consentHandler *ConsentHandler, msgHandler *MessageHandler) {
	api := r.Group("/api/v1")
	api.Use(middleware.GatewayAuthMiddleware())
	{
		consent := api.Group("/consent-requests")
		{
			consent.GET("", consentHandler.ListPending)
			consent.POST("/:id/accept", consentHandler.Accept)
			consent.POST("/:id/decline", consentHandler.Decline)
		}

		convs := api.Group("/conversations")
		{
			convs.GET("", convHandler.List)
			convs.POST("", convHandler.CreateGroup)
			convs.POST("/start", convHandler.StartPrivateChat)
			convs.GET("/:id", convHandler.Get)
			convs.POST("/:id/invite", convHandler.InviteToGroup)
			convs.PUT("/:id/note", convHandler.UpdateNote)
			convs.PUT("/:id/group-nickname", convHandler.UpdateGroupNickname)
			convs.POST("/:id/read", convHandler.MarkRead)
			convs.POST("/:id/leave", convHandler.Leave)
			convs.DELETE("/:id/members/:user_id", convHandler.RemoveMember)
		}

		msgs := api.Group("/conversations/:id/messages")
		{
			msgs.GET("", msgHandler.List)
			msgs.POST("", msgHandler.Send)
			msgs.PUT("/:message_id", msgHandler.Update)
			msgs.DELETE("/:message_id", msgHandler.Delete)
		}
	}
}
