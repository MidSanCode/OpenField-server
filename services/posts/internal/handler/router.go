package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/middleware"
)

// RegisterRoutes registers all posts service routes.
func RegisterRoutes(r *gin.Engine, postHandler *PostHandler) {
	api := r.Group("/api/v1")
	api.Use(middleware.GatewayAuthMiddleware())
	{
		posts := api.Group("/posts")
		{
			posts.GET("", postHandler.ListPosts)
			posts.POST("", postHandler.CreatePost)
			posts.GET("/:id", postHandler.GetPost)
			posts.PUT("/:id", postHandler.UpdatePost)
			posts.DELETE("/:id", postHandler.DeletePost)
			posts.GET("/:id/replies", postHandler.ListReplies)
			posts.POST("/:id/replies", postHandler.CreateReply)
			posts.PUT("/:id/replies/:reply_id", postHandler.UpdateReply)
			posts.DELETE("/:id/replies/:reply_id", postHandler.DeleteReply)
		}
		api.GET("/users/:user_id/posts", postHandler.ListPostsByUser)
	}
}
