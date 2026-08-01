package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/middleware"
)

// RegisterRoutes registers all posts service routes.
func RegisterRoutes(r *gin.Engine, postHandler *PostHandler) {
	api := r.Group("/api/v1")
	{
		// Public reads: posts and replies are viewable without authentication.
		api.GET("/posts", postHandler.ListPosts)
		api.GET("/posts/:id", postHandler.GetPost)
		api.GET("/posts/:id/replies", postHandler.ListReplies)
		api.GET("/users/:user_id/posts", postHandler.ListPostsByUser)

		auth := api.Group("")
		auth.Use(middleware.GatewayAuthMiddleware())
		{
			auth.POST("/posts", postHandler.CreatePost)
			auth.PUT("/posts/:id", postHandler.UpdatePost)
			auth.DELETE("/posts/:id", postHandler.DeletePost)
			auth.POST("/posts/:id/replies", postHandler.CreateReply)
			auth.PUT("/posts/:id/replies/:reply_id", postHandler.UpdateReply)
			auth.DELETE("/posts/:id/replies/:reply_id", postHandler.DeleteReply)
		}
	}
}
