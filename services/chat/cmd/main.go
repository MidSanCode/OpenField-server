package main

import (
	"log"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/config"
	"github.com/openfield/server/pkg/database"
	"github.com/openfield/server/pkg/logger"
	"github.com/openfield/server/pkg/middleware"
	"github.com/openfield/server/services/chat/internal/handler"
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

	convHandler := handler.NewConversationHandler()
	consentHandler := handler.NewConsentHandler()
	msgHandler := handler.NewMessageHandler()

	r := gin.New()
	r.Use(middleware.Recovery())
	r.Use(logger.GinLogger())
	r.NoRoute(middleware.NotFound())
	r.NoMethod(middleware.MethodNotAllowed())

	handler.RegisterRoutes(r, convHandler, consentHandler, msgHandler)

	addr := "127.0.0.1:" + cfg.ServicePort("CHAT")
	logger.Log.Info("chat service starting", "address", addr)
	if err := r.Run(addr); err != nil {
		log.Fatalf("failed to start chat service: %v", err)
	}
}
