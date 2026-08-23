package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/middleware"
)

// RegisterRoutes registers all account service routes.
// Public auth endpoints and protected user endpoints.
func RegisterRoutes(r *gin.Engine, authHandler *AuthHandler, userHandler *UserHandler, walletHandler *WalletHandler, capabilitiesHandler *CapabilitiesHandler, taskHandler *TaskHandler, transferHandler *TransferHandler, pinHandler *PinHandler, membershipHandler *MembershipHandler, punishmentHandler *PunishmentHandler, checkHandler *CheckHandler) {
	api := r.Group("/api/v1")
	{
		// Public capabilities introspection — unauthenticated so the client
		// can show a compatibility matrix in settings before login.
		api.GET("/capabilities", capabilitiesHandler.Capabilities)

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
			users.PUT("/me/e2ee-key", userHandler.SetE2EEKey)
			users.POST("/me/avatar", userHandler.UploadAvatar)
			users.POST("/me/banner", userHandler.UploadBanner)
			users.GET("/me/permissions", userHandler.GetMyPermissions)
			users.POST("/me/claim-daily-bonus", userHandler.ClaimDailyBonus)
			users.PUT("/me/locale", userHandler.UpdateLocale)
			users.PUT("/me/privacy", userHandler.UpdatePrivacy)
			users.PUT("/me/name-style", userHandler.UpdateNameStyle)
			users.PUT("/me/storage-bucket", userHandler.SetMyStorageBucket)
			users.GET("/storage-buckets", userHandler.ListStorageBuckets)
			users.GET("/search", userHandler.SearchUsers)
			users.POST("/me/pin", pinHandler.SetPin)
			users.POST("/me/pin/verify", pinHandler.VerifyPin)
		}
		// Public profile lookup (used to view other users' public profiles).
		api.GET("/users/:id", userHandler.GetUser)
		// Admin exp adjustment (gateway enforces the user.adjust_exp permission).
		api.PUT("/users/:id/exp", middleware.GatewayAuthMiddleware(), userHandler.AdjustExp)
		// Admin membership grant (gateway enforces user.membership.grant).
		api.PUT("/users/:id/membership", middleware.GatewayAuthMiddleware(), membershipHandler.Grant)

		// Moderation: punish a user / view history (gateway enforces
		// user.punish, and granted-revoke is honoured by the permission system).
		punished := api.Group("/users/:id")
		punished.Use(middleware.GatewayAuthMiddleware())
		{
			punished.POST("/punishments", punishmentHandler.Punish)
			punished.GET("/punishments", punishmentHandler.ListPunishments)
		}

		// Membership: the catalog + current state is a protected read; purchases
		// charge the wallet and require the payment PIN.
		membership := api.Group("/membership")
		membership.Use(middleware.GatewayAuthMiddleware())
		{
			membership.GET("", membershipHandler.GetMembership)
			membership.POST("/purchase", membershipHandler.Purchase)
			membership.PUT("/auto-renew", membershipHandler.SetAutoRenew)
			membership.GET("/purchases", membershipHandler.ListPurchases)
		}

		// Follow relationships: mutations require auth; lists are public reads.
		follow := api.Group("/users/:id")
		{
			follow.POST("/follow", middleware.GatewayAuthMiddleware(), userHandler.FollowUser)
			follow.DELETE("/follow", middleware.GatewayAuthMiddleware(), userHandler.UnfollowUser)
			follow.GET("/followers", userHandler.ListFollowers)
			follow.GET("/following", userHandler.ListFollowing)
			follow.GET("/friends", userHandler.ListFriends)
		}

		// Wallet: read requires auth; balance adjustments require the
		// wallet.manage permission (checked by the gateway).
		wallet := api.Group("/wallet")
		{
			wallet.GET("", middleware.GatewayAuthMiddleware(), walletHandler.GetMyWallet)
			wallet.POST("/adjust", middleware.GatewayAuthMiddleware(), walletHandler.AdjustWallet)
		}

		// Tasks / experience: auth required.
		tasks := api.Group("/tasks")
		tasks.Use(middleware.GatewayAuthMiddleware())
		{
			tasks.GET("", taskHandler.ListTasks)
			tasks.GET("/daily-login/calendar", taskHandler.CheckinCalendar)
			tasks.POST("/daily-login/claim", taskHandler.ClaimDailyLogin)
			tasks.POST("/daily-login/makeup", taskHandler.MakeupCheckin)
			tasks.POST("/daily-login/makeup-date", taskHandler.MakeupByDate)
			tasks.POST("/:code/claim", taskHandler.ClaimOneTime)
		}

		// Experience history: auth required.
		exp := api.Group("/exp")
		exp.Use(middleware.GatewayAuthMiddleware())
		{
			exp.GET("/history", taskHandler.ListExpHistory)
		}

		// Transfers: auth required; any user may send to any valid user.
		transfers := api.Group("/transfers")
		transfers.Use(middleware.GatewayAuthMiddleware())
		{
			transfers.GET("", transferHandler.ListTransfers)
			transfers.POST("", transferHandler.CreateTransfer)
			transfers.POST("/:id/accept", transferHandler.AcceptTransfer)
			transfers.POST("/:id/decline", transferHandler.DeclineTransfer)
		}

		// Checks (red packets): creating escrows money and requires the payment
		// PIN; claiming pays out one share; reads are open to authenticated users.
		checks := api.Group("/checks")
		checks.Use(middleware.GatewayAuthMiddleware())
		{
			checks.POST("", checkHandler.Create)
			checks.GET("/:id", checkHandler.Get)
			checks.POST("/:id/claim", checkHandler.Claim)
		}
	}
}
