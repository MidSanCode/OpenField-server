package main

import (
	"log"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/internal/auth"
	"github.com/openfield/server/internal/config"
	"github.com/openfield/server/internal/database"
	"github.com/openfield/server/internal/handler"
	"github.com/openfield/server/internal/logger"
	"github.com/openfield/server/internal/middleware"
	"github.com/openfield/server/internal/storage"
)

func main() {
	// initialize logger
	logger.Init()

	// load configuration from config file
	configPath := os.Getenv("OPENFIELD_CONFIG")
	if configPath == "" {
		configPath = "config/config.local.yaml"
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("failed to load configuration: %v", err)
	}

	// set gin mode
	if cfg.Server.Mode != "" {
		gin.SetMode(cfg.Server.Mode)
	}

	// connect to database
	if err := database.Connect(cfg); err != nil {
		log.Fatalf("failed to connect to database: %v", err)
	}
	defer database.Close()

	// run migrations
	if err := database.RunMigrations(); err != nil {
		log.Fatalf("failed to run migrations: %v", err)
	}

	// connect to redis if enabled
	if err := database.ConnectRedis(cfg); err != nil {
		log.Fatalf("failed to connect to redis: %v", err)
	}
	defer database.CloseRedis()

	// initialize storage (RustFS S3)
	store, err := storage.New(cfg.Storage)
	if err != nil {
		log.Fatalf("failed to initialize storage: %v", err)
	}

	// initialize auth manager
	authManager := auth.NewManager(cfg)

	// initialize token manager
	tokenMgr := middleware.NewTokenManager(cfg.JWT.SecretKey, cfg.JWT.ExpiryHours)

	// initialize handlers
	authHandler := handler.NewAuthHandler(authManager, tokenMgr, cfg.OIDC.AppRedirectURL)
	postHandler := handler.NewPostHandler()
	userHandler := handler.NewUserHandler(store, cfg.Storage)
	messageHandler := handler.NewMessageHandler()
	attachmentHandler := handler.NewAttachmentHandler(store, cfg.Storage)
	routerHandler := handler.NewHandler(authHandler, postHandler, userHandler, messageHandler, attachmentHandler, store, cfg)

	// setup gin router
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(logger.GinLogger())

	// register routes
	routerHandler.RegisterRoutes(r)

	// start server
	addr := cfg.Address()
	logger.Log.Info("starting server", "address", addr, "mode", cfg.Server.Mode)
	if err := r.Run(addr); err != nil {
		log.Fatalf("failed to start server: %v", err)
	}
}
