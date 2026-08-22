package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/config"
	"github.com/openfield/server/pkg/logger"
	"github.com/openfield/server/pkg/middleware"
	"github.com/openfield/server/services/push/internal/handler"
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

	hub := handler.NewHub()
	go hub.Run()

	ln, err := handler.StartListener(hub, cfg.DSN())
	if err != nil {
		log.Fatalf("failed to start push listener: %v", err)
	}

	wsHandler := handler.NewWsHandler(hub)

	r := gin.New()
	r.Use(middleware.Recovery())
	r.Use(logger.GinLogger())
	r.NoRoute(middleware.NotFound())
	r.NoMethod(middleware.MethodNotAllowed())

	handler.RegisterRoutes(r, wsHandler, &handler.TicketHandler{})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		addr := "127.0.0.1:" + cfg.ServicePort("PUSH")
		logger.Log.Info("push service starting", "address", addr)
		if err := r.Run(addr); err != nil {
			log.Fatalf("failed to start push service: %v", err)
		}
	}()

	<-ctx.Done()
	hub.Close()
	if ln != nil {
		ln.Close()
	}
	logger.Log.Info("push service stopped")
}
