package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// ServerConfig holds server configuration.
type ServerConfig struct {
	Port string `yaml:"port"`
	Mode string `yaml:"mode"`
}

// DatabaseConfig holds database configuration.
type DatabaseConfig struct {
	Host         string `yaml:"host"`
	Port         int    `yaml:"port"`
	User         string `yaml:"user"`
	Password     string `yaml:"password"`
	DBName       string `yaml:"dbname"`
	SSLMode      string `yaml:"sslmode"`
	MaxOpenConns int    `yaml:"max_open_conns"`
	MaxIdleConns int    `yaml:"max_idle_conns"`
}

// RedisConfig holds Redis configuration.
type RedisConfig struct {
	Addr     string `yaml:"addr"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
	Enabled  bool   `yaml:"enabled"`
}

// OIDCConfig holds OIDC configuration.
type OIDCConfig struct {
	IssuerURL      string   `yaml:"issuer_url"`
	ClientID       string   `yaml:"client_id"`
	ClientSecret   string   `yaml:"client_secret"`
	RedirectURL    string   `yaml:"redirect_url"`
	AppRedirectURL string   `yaml:"app_redirect_url"`
	Scopes         []string `yaml:"scopes"`
}

// JWTConfig holds JWT configuration.
type JWTConfig struct {
	SecretKey   string `yaml:"secret_key"`
	ExpiryHours int    `yaml:"expiry_hours"`
}

// StorageConfig holds RustFS (S3-compatible) storage configuration.
type StorageConfig struct {
	Endpoint       string `yaml:"endpoint"`
	AccessKey      string `yaml:"access_key"`
	SecretKey      string `yaml:"secret_key"`
	Bucket         string `yaml:"bucket"`
	UseSSL         bool   `yaml:"use_ssl"`
	PublicBaseURL  string `yaml:"public_base_url"`
	MaxUploadBytes int64  `yaml:"max_upload_bytes"`
	MaxAttachments int    `yaml:"max_attachments_per_post"`
}

// Config holds all configuration for the server.
type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Database DatabaseConfig `yaml:"database"`
	Redis    RedisConfig    `yaml:"redis"`
	OIDC     OIDCConfig     `yaml:"oidc"`
	JWT      JWTConfig      `yaml:"jwt"`
	Storage  StorageConfig  `yaml:"storage"`
}

// Load reads configuration from a YAML file, with environment variable overrides.
func Load(configPath string) (*Config, error) {
	cfg := &Config{}

	// Read YAML config file
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Override with environment variables if set
	cfg.overrideFromEnv()

	return cfg, nil
}

// overrideFromEnv overrides config values with environment variables if they are set.
func (c *Config) overrideFromEnv() {
	if v := os.Getenv("SERVER_PORT"); v != "" {
		c.Server.Port = v
	}
	if v := os.Getenv("DATABASE_URL"); v != "" {
		// Parse DATABASE_URL if provided (format: postgres://user:pass@host:port/dbname)
		c.DatabaseURLFromEnv(v)
	}
	if v := os.Getenv("REDIS_ADDR"); v != "" {
		c.Redis.Addr = v
	}
	if v := os.Getenv("REDIS_PASSWORD"); v != "" {
		c.Redis.Password = v
	}
	if v := os.Getenv("OIDC_ISSUER_URL"); v != "" {
		c.OIDC.IssuerURL = v
	}
	if v := os.Getenv("OIDC_CLIENT_ID"); v != "" {
		c.OIDC.ClientID = v
	}
	if v := os.Getenv("OIDC_CLIENT_SECRET"); v != "" {
		c.OIDC.ClientSecret = v
	}
	if v := os.Getenv("OIDC_REDIRECT_URL"); v != "" {
		c.OIDC.RedirectURL = v
	}
	if v := os.Getenv("OIDC_APP_REDIRECT_URL"); v != "" {
		c.OIDC.AppRedirectURL = v
	}
	if v := os.Getenv("JWT_SECRET_KEY"); v != "" {
		c.JWT.SecretKey = v
	}
	if v := os.Getenv("STORAGE_ENDPOINT"); v != "" {
		c.Storage.Endpoint = v
	}
	if v := os.Getenv("STORAGE_ACCESS_KEY"); v != "" {
		c.Storage.AccessKey = v
	}
	if v := os.Getenv("STORAGE_SECRET_KEY"); v != "" {
		c.Storage.SecretKey = v
	}
	if v := os.Getenv("STORAGE_BUCKET"); v != "" {
		c.Storage.Bucket = v
	}
	if v := os.Getenv("STORAGE_PUBLIC_BASE_URL"); v != "" {
		c.Storage.PublicBaseURL = v
	}
}

// DatabaseURLFromEnv parses a DATABASE_URL environment variable.
func (c *Config) DatabaseURLFromEnv(url string) {
	// Simple parsing for postgres://user:pass@host:port/dbname format
	// In production, use a proper URL parser
	fmt.Sscanf(url, "postgres://%[^:]:%[^@]@%[^:]:%d/%s", &c.Database.User, &c.Database.Password, &c.Database.Host, &c.Database.Port, &c.Database.DBName)
}

// DSN returns the database connection string.
func (c *Config) DSN() string {
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=%s",
		c.Database.User, c.Database.Password, c.Database.Host, c.Database.Port, c.Database.DBName, c.Database.SSLMode)
}

// Address returns the server address.
func (c *Config) Address() string {
	return ":" + c.Server.Port
}
