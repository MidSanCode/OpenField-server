package main

import (
	"flag"
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
	"github.com/openfield/server/services/account/internal/auth"
	"github.com/openfield/server/services/account/internal/handler"
)

func main() {
	migrateOnly := flag.Bool("migrate", false, "run database migrations and exit")
	flag.Parse()

	logger.Init()

	buildVersion := os.Getenv("OPENFIELD_VERSION")
	if buildVersion == "" {
		buildVersion = "dev"
	}

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

	if *migrateOnly {
		logger.Log.Info("database migrations completed (migrate-only mode)")
		return
	}

	store, err := storage.New(cfg.Storage)
	if err != nil {
		log.Fatalf("failed to initialize storage: %v", err)
	}

	authManager := auth.NewManager(cfg)
	tokenMgr := middleware.NewTokenManager(cfg.JWT.SecretKey, cfg.JWT.ExpiryHours)

	authHandler := handler.NewAuthHandler(authManager, tokenMgr, cfg.OIDC.AppRedirectURL, cfg.JWT.RefreshExpiryDays)
	userHandler := handler.NewUserHandler(store, cfg.Storage)
	userHandler.SetGameConfig(cfg.Game)
	walletHandler := handler.NewWalletHandler()
	capabilitiesHandler := handler.NewCapabilitiesHandler(buildVersion)
	taskHandler := handler.NewTaskHandler()
	taskHandler.SetGameConfig(cfg.Game)
	transferHandler := handler.NewTransferHandler()
	pinHandler := handler.NewPinHandler()
	membershipHandler := handler.NewMembershipHandler()

	// Background sweeper: refund pending transfers that are 24h unanswered.
	go startTransferSweeper()

	r := gin.New()
	r.Use(middleware.Recovery())
	r.Use(logger.GinLogger())
	r.NoRoute(middleware.NotFound())
	r.NoMethod(middleware.MethodNotAllowed())

	handler.RegisterRoutes(r, authHandler, userHandler, walletHandler, capabilitiesHandler, taskHandler, transferHandler, pinHandler, membershipHandler)

	addr := "127.0.0.1:" + cfg.ServicePort("ACCOUNT")
	logger.Log.Info("account service starting", "address", addr)
	if err := r.Run(addr); err != nil {
		log.Fatalf("failed to start account service: %v", err)
	}
}

// startTransferSweeper periodically refunds pending transfers that the
// recipient left unanswered past the 24-hour window.
func startTransferSweeper() {
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()

	repo := repository.NewTransferRepository()
	for range ticker.C {
		refunded, err := repo.RefundExpired(time.Now())
		if err != nil {
			logger.Log.Error("failed to sweep expired transfers", "error", err)
			continue
		}
		if refunded > 0 {
			logger.Log.Info("refunded expired transfers", "count", refunded)
		}
	}
}
