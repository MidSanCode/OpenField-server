package main

import (
	"log"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/config"
	"github.com/openfield/server/pkg/database"
	"github.com/openfield/server/pkg/logger"
	"github.com/openfield/server/pkg/storage"
	"github.com/openfield/server/services/storage/internal/handler"
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

	attHandler := handler.NewAttachmentHandler(store, cfg.Storage)

	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(logger.GinLogger())

	handler.RegisterRoutes(r, attHandler)

	addr := "127.0.0.1:" + cfg.ServicePort("STORAGE")
	logger.Log.Info("storage service starting", "address", addr)
	if err := r.Run(addr); err != nil {
		log.Fatalf("failed to start storage service: %v", err)
	}
}
