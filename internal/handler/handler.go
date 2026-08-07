package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/internal/config"
	"github.com/openfield/server/internal/middleware"
	"github.com/openfield/server/internal/storage"
)

type Handler struct {
	authHandler       *AuthHandler
	postHandler       *PostHandler
	userHandler       *UserHandler
	messageHandler    *MessageHandler
	attachmentHandler *AttachmentHandler
	config            *config.Config
}

func NewHandler(authHandler *AuthHandler, postHandler *PostHandler, userHandler *UserHandler, messageHandler *MessageHandler, attachmentHandler *AttachmentHandler, store *storage.Store, config *config.Config) *Handler {
	return &Handler{
		authHandler:       authHandler,
		postHandler:       postHandler,
		userHandler:       userHandler,
		messageHandler:    messageHandler,
		attachmentHandler: attachmentHandler,
		config:            config,
	}
}

func (h *Handler) RegisterRoutes(r *gin.Engine) {
	api := r.Group("/api/v1")
	{
		api.GET("/health", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"status": "ok"})
		})

		auth := api.Group("/auth")
		{
			auth.GET("/providers", h.authHandler.GetProviders)
			auth.GET("/oidc/login", h.authHandler.OIDCLogin)
			auth.GET("/oidc/callback", h.authHandler.OIDCCallback)
			auth.POST("/login", h.authHandler.Login)
			auth.POST("/register", middleware.AuthMiddleware(h.config), h.authHandler.Register)
			auth.POST("/refresh", h.authHandler.RefreshToken)
		}

		posts := api.Group("/posts")
		{
			posts.GET("", h.postHandler.ListPosts)
			posts.GET("/:id", h.postHandler.GetPost)
			posts.Use(middleware.AuthMiddleware(h.config))
			{
				posts.POST("", h.postHandler.CreatePost)
				posts.PUT("/:id", h.postHandler.UpdatePost)
				posts.DELETE("/:id", h.postHandler.DeletePost)
			}
		}

		users := api.Group("/users")
		{
			users.GET("/me", middleware.AuthMiddleware(h.config), h.userHandler.GetCurrentUser)
			users.PUT("/me", middleware.AuthMiddleware(h.config), h.userHandler.UpdateProfile)
			users.POST("/me/avatar", middleware.AuthMiddleware(h.config), h.userHandler.UploadAvatar)
			users.POST("/me/banner", middleware.AuthMiddleware(h.config), h.userHandler.UploadBanner)
			users.GET("/:id", h.userHandler.GetUser)
		}

		attachments := api.Group("/attachments")
		attachments.Use(middleware.AuthMiddleware(h.config))
		{
			attachments.POST("", h.attachmentHandler.Upload)
			attachments.GET("", h.attachmentHandler.ListByUser)
			attachments.GET("/:id", h.attachmentHandler.Get)
			attachments.DELETE("/:id", h.attachmentHandler.Delete)
		}

		messages := api.Group("/messages")
		messages.Use(middleware.AuthMiddleware(h.config))
		{
			messages.POST("", h.messageHandler.SendMessage)
			messages.GET("/:user_id", h.messageHandler.GetConversation)
		}
	}
}
