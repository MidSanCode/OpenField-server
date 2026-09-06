package main

import (
	"log"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/config"
	"github.com/openfield/server/pkg/database"
	"github.com/openfield/server/pkg/health"
	"github.com/openfield/server/pkg/logger"
	"github.com/openfield/server/pkg/middleware"
	"github.com/openfield/server/services/posts/internal/handler"
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

	if err := database.RunMigrationsIfEnabled(cfg); err != nil {
		log.Fatalf("failed to run database migrations: %v", err)
	}

	postHandler := handler.NewPostHandler()
	campHandler := handler.NewCampHandler()

	r := gin.New()
	r.Use(middleware.Recovery())
	r.Use(logger.GinLogger())
	r.NoRoute(middleware.NotFound())
	r.NoMethod(middleware.MethodNotAllowed())

	r.GET("/healthz", health.Handler(nil))

	handler.RegisterRoutes(r, postHandler, campHandler)

	addr := "127.0.0.1:" + cfg.ServicePort("POSTS")
	logger.Log.Info("posts service starting", "address", addr)
	if err := r.Run(addr); err != nil {
		log.Fatalf("failed to start posts service: %v", err)
	}
}
