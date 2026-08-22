package main

import (
	"log"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/config"
	"github.com/openfield/server/pkg/database"
	"github.com/openfield/server/pkg/logger"
	"github.com/openfield/server/pkg/middleware"
	"github.com/openfield/server/pkg/repository"
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

	if err := database.RunMigrationsIfEnabled(cfg); err != nil {
		log.Fatalf("failed to run database migrations: %v", err)
	}

	store, err := storage.New(cfg.Storage)
	if err != nil {
		log.Fatalf("failed to initialize storage: %v", err)
	}

	attHandler := handler.NewAttachmentHandler(store, cfg.Storage)

	// Background sweeper: drop abandoned chunked-upload sessions so dead
	// sessions do not accumulate. The chunk objects themselves are removed on
	// completion; leftovers are harmless temp objects.
	go func() {
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			if err := repository.PurgeStaleUploadSessions(24 * time.Hour); err != nil {
				logger.Log.Error("failed to purge stale upload sessions", "error", err)
			}
		}
	}()

	r := gin.New()
	r.Use(middleware.Recovery())
	r.Use(logger.GinLogger())
	r.NoRoute(middleware.NotFound())
	r.NoMethod(middleware.MethodNotAllowed())

	handler.RegisterRoutes(r, attHandler)

	addr := "127.0.0.1:" + cfg.ServicePort("STORAGE")
	logger.Log.Info("storage service starting", "address", addr)
	if err := r.Run(addr); err != nil {
		log.Fatalf("failed to start storage service: %v", err)
	}
}
