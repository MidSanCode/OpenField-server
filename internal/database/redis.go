package database

import (
	"context"
	"fmt"
	"time"

	"github.com/openfield/server/internal/config"
	"github.com/openfield/server/internal/logger"
	"github.com/redis/go-redis/v9"
)

var RedisClient *redis.Client

// ConnectRedis initializes the Redis client if enabled.
func ConnectRedis(cfg *config.Config) error {
	if !cfg.Redis.Enabled {
		logger.Log.Info("redis is disabled")
		return nil
	}

	RedisClient = redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Addr,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	})

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := RedisClient.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("failed to connect to redis: %w", err)
	}

	logger.Log.Info("redis connected successfully", "addr", cfg.Redis.Addr)
	return nil
}

// CloseRedis closes the Redis connection.
func CloseRedis() {
	if RedisClient != nil {
		RedisClient.Close()
	}
}

// GetRedis returns the Redis client.
func GetRedis() *redis.Client {
	return RedisClient
}
