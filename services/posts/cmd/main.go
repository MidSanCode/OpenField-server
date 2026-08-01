package main

import (
	"log"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/config"
	"github.com/openfield/server/pkg/database"
	"github.com/openfield/server/pkg/logger"
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

	postHandler := handler.NewPostHandler()

	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(logger.GinLogger())

	handler.RegisterRoutes(r, postHandler)

	addr := ":" + cfg.ServicePort("POSTS")
	logger.Log.Info("posts service starting", "address", addr)
	if err := r.Run(addr); err != nil {
		log.Fatalf("failed to start posts service: %v", err)
	}
}
