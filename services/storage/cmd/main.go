package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/config"
	"github.com/openfield/server/pkg/database"
	"github.com/openfield/server/pkg/health"
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

	// Background sweeper: delete attachments whose burn-after-view deadline
	// has passed (object + thumbnail + row). 15s cadence keeps the delay
	// between an armed countdown expiring and its files disappearing short.
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			attHandler.SweepBurnedAttachments(ctx)
			cancel()
		}
	}()

	r := gin.New()
	r.Use(middleware.Recovery())
	r.Use(logger.GinLogger())
	r.NoRoute(middleware.NotFound())
	r.NoMethod(middleware.MethodNotAllowed())

	// Liveness/readiness probe; also reports whether object storage is usable.
	r.GET("/healthz", health.Handler(func(ctx context.Context) map[string]string {
		if store.Enabled() {
			return map[string]string{"storage": "up"}
		}
		return map[string]string{"status": "down", "storage": "disabled"}
	}))

	handler.RegisterRoutes(r, attHandler)

	// Background sweeper: recycle uploaded files that were never attached to
	// anything (posts, replies, chat messages, profile pictures). Runs every
	// five minutes by default; configurable through storage.recycle.
	go startRecycleSweeper(store, cfg.Storage.Recycle)

	addr := "127.0.0.1:" + cfg.ServicePort("STORAGE")
	logger.Log.Info("storage service starting", "address", addr)
	if err := r.Run(addr); err != nil {
		log.Fatalf("failed to start storage service: %v", err)
	}
}

// startRecycleSweeper removes orphan attachments on a fixed cadence. The
// sweeper reads its buckets from config (an empty list means every bucket),
// runs as soon as the process boots, then on the configured interval.
func startRecycleSweeper(store *storage.Manager, cfg config.RecycleConfig) {
	if !cfg.Enabled {
		return
	}
	interval := time.Duration(cfg.RecycleInterval()) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	attRepo := repository.NewAttachmentRepository()
	run := func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		items, err := attRepo.ListRecyclableAttachments(cfg.Buckets, cfg.RecycleMinAge(), 200)
		if err != nil {
			logger.Log.Error("failed to list recyclable attachments", "error", err)
			return
		}
		if len(items) == 0 {
			return
		}
		cleaned := 0
		for _, item := range items {
			bucketStore := store.For(item.Bucket)
			if bucketStore == nil || !bucketStore.Enabled() {
				bucketStore = store.Default()
			}
			if bucketStore == nil || !bucketStore.Enabled() {
				continue
			}
			if err := bucketStore.Delete(ctx, item.ObjectKey); err != nil {
				logger.Log.Warn("failed to delete recycled object", "bucket", item.Bucket, "key", item.ObjectKey, "error", err)
				continue
			}
			if err := attRepo.DeleteByID(item.ID); err != nil {
				logger.Log.Warn("failed to delete recycled attachment row", "id", item.ID, "error", err)
				continue
			}
			cleaned++
		}
		if cleaned > 0 {
			logger.Log.Info("recycled orphan attachments", "count", cleaned, "interval", interval)
		}
	}

	go func() {
		run()
		for range ticker.C {
			run()
		}
	}()
}
