// Command push runs the realtime push service: a WebSocket hub fed by
// Postgres NOTIFY events plus one-time connection tickets for browsers.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/gin-gonic/gin"
	"github.com/openfield/server/pkg/config"
	"github.com/openfield/server/pkg/health"
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
	// Internal services sit behind the gateway on loopback and are never called
	// directly by clients, so no proxy is trusted here: c.ClientIP() must be the
	// real TCP peer, never a client-supplied X-Forwarded-For value. Gin trusts
	// every proxy by default, which made the IP in these services' logs
	// attacker-chosen and left the audit trail worthless.
	if err := r.SetTrustedProxies(nil); err != nil {
		log.Fatalf("failed to configure trusted proxies: %v", err)
	}
	r.Use(middleware.Recovery())
	r.Use(logger.GinLogger())
	r.NoRoute(middleware.NotFound())
	r.NoMethod(middleware.MethodNotAllowed())

	// Liveness probe (the push service owns its DB LISTEN connection, so the
	// shared pool check reports n/a).
	r.GET("/healthz", health.Handler(nil))

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
