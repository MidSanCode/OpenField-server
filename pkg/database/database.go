package database

import (
	"database/sql"
	"fmt"

	_ "github.com/lib/pq"
	"github.com/openfield/server/pkg/config"
	"github.com/openfield/server/pkg/logger"
)

// DB is the global database connection.
var DB *sql.DB

// Connect initializes the PostgreSQL database connection.
func Connect(cfg *config.Config) error {
	var err error
	DB, err = sql.Open("postgres", cfg.DSN())
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}

	DB.SetMaxOpenConns(cfg.Database.MaxOpenConns)
	DB.SetMaxIdleConns(cfg.Database.MaxIdleConns)

	if err = DB.Ping(); err != nil {
		return fmt.Errorf("failed to ping database: %w", err)
	}

	logger.Log.Info("database connected successfully", "host", cfg.Database.Host, "db", cfg.Database.DBName)
	return nil
}

// Close closes the database connection.
func Close() {
	if DB != nil {
		DB.Close()
	}
}
