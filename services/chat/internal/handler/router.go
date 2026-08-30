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
			convs.GET("/public", convHandler.ListPublicGroups)
			convs.GET("/:id", convHandler.Get)
			convs.GET("/:id/e2ee-keys", convHandler.GetE2EEKeys)
			convs.POST("/:id/e2ee-keys", convHandler.PutE2EEKeys)
			convs.POST("/:id/invite", convHandler.InviteToGroup)
			convs.PUT("/:id/note", convHandler.UpdateNote)
			convs.PUT("/:id/group-nickname", convHandler.UpdateGroupNickname)
			convs.PUT("/:id/notify-level", convHandler.UpdateNotifyLevel)
			convs.POST("/:id/read", convHandler.MarkRead)
			convs.POST("/:id/typing", convHandler.Typing)
			convs.POST("/:id/leave", convHandler.Leave)
			convs.POST("/:id/join", convHandler.JoinGroup)
			convs.PUT("/:id/settings", convHandler.UpdateSettings)
			convs.PUT("/:id/title", convHandler.UpdateTitle)
			convs.PUT("/:id/avatar", convHandler.UpdateAvatar)
			convs.POST("/:id/mute-all", convHandler.MuteAll)
			convs.DELETE("/:id/mute-all", convHandler.UnmuteAll)
			convs.DELETE("/:id", convHandler.Delete)
			convs.DELETE("/:id/members/:user_id", convHandler.RemoveMember)
			convs.PUT("/:id/members/:user_id/role", convHandler.SetMemberRole)
			convs.PUT("/:id/members/:user_id/title", convHandler.SetMemberTitle)
			convs.POST("/:id/members/:user_id/mute", convHandler.MuteMember)
			convs.DELETE("/:id/members/:user_id/mute", convHandler.UnmuteMember)
		}

		msgs := api.Group("/conversations/:id/messages")
		{
			msgs.GET("", msgHandler.List)
			msgs.GET("/search", msgHandler.Search)
			msgs.POST("", msgHandler.Send)
			msgs.POST("/:message_id/read", msgHandler.MarkBurnRead)
			msgs.PUT("/:message_id", msgHandler.Update)
			msgs.DELETE("/:message_id", msgHandler.Delete)
		}
	}
}
