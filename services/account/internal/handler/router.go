package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/middleware"
)

// RegisterRoutes registers all account service routes.
// Public auth endpoints and protected user endpoints.
func RegisterRoutes(r *gin.Engine, authHandler *AuthHandler, userHandler *UserHandler) {
	api := r.Group("/api/v1")
	{
		auth := api.Group("/auth")
		{
			auth.GET("/providers", authHandler.GetProviders)
			auth.GET("/oidc/login", authHandler.OIDCLogin)
			auth.POST("/oidc/bind", middleware.GatewayAuthMiddleware(), authHandler.OIDCBind)
			auth.GET("/oidc/callback", authHandler.OIDCCallback)
			auth.POST("/login", authHandler.Login)
			auth.POST("/register", middleware.GatewayAuthMiddleware(), authHandler.Register)
			auth.POST("/refresh", authHandler.RefreshToken)
		}

		users := api.Group("/users")
		users.Use(middleware.GatewayAuthMiddleware())
		{
			users.GET("/me", userHandler.GetCurrentUser)
			users.PUT("/me", userHandler.UpdateProfile)
			users.POST("/me/avatar", userHandler.UploadAvatar)
			users.POST("/me/banner", userHandler.UploadBanner)
			users.GET("/me/permissions", userHandler.GetMyPermissions)
			users.GET("/search", userHandler.SearchUsers)
			users.GET("/:id", userHandler.GetUser)
		}
	}
}
