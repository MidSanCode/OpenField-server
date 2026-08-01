package main

import (
	"log"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/config"
	"github.com/openfield/server/pkg/database"
	"github.com/openfield/server/pkg/logger"
	"github.com/openfield/server/pkg/middleware"
	"github.com/openfield/server/pkg/storage"
	"github.com/openfield/server/services/account/internal/auth"
	"github.com/openfield/server/services/account/internal/handler"
)

func main() {
	logger.Init()

	configPath := os.Getenv("OPENFIELD_CONFIG")
	if configPath == "" {
		configPath = "config/config.local.yaml"
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("failed to load configuration: %v", err)
	}

	if cfg.Server.Mode != "" {
		gin.SetMode(cfg.Server.Mode)
	}

	if err := database.Connect(cfg); err != nil {
		log.Fatalf("failed to connect to database: %v", err)
	}
	defer database.Close()

	store, err := storage.New(cfg.Storage)
	if err != nil {
		log.Fatalf("failed to initialize storage: %v", err)
	}

	authManager := auth.NewManager(cfg)
	tokenMgr := middleware.NewTokenManager(cfg.JWT.SecretKey, cfg.JWT.ExpiryHours)

	authHandler := handler.NewAuthHandler(authManager, tokenMgr, cfg.OIDC.AppRedirectURL)
	userHandler := handler.NewUserHandler(store, cfg.Storage)

	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(logger.GinLogger())

	handler.RegisterRoutes(r, authHandler, userHandler)

	addr := ":" + cfg.ServicePort("ACCOUNT")
	logger.Log.Info("account service starting", "address", addr)
	if err := r.Run(addr); err != nil {
		log.Fatalf("failed to start account service: %v", err)
	}
}
