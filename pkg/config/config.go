package config

import (
	"fmt"
	"net/url"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// ServerConfig holds server configuration.
type ServerConfig struct {
	Port            string   `yaml:"port"`
	Mode            string   `yaml:"mode"`
	AllowedOrigins  []string `yaml:"allowed_origins"`
	AllowAllOrigins bool     `yaml:"allow_all_origins"`
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
	// Migrate forces a full schema migration at startup. Historically only the
	// account service (the schema owner) enabled this; other services relied on
	// the owner to have already created the schema.
	Migrate bool `yaml:"migrate"`
	// AutoMigrate enables automatic schema upgrades: when the database is behind
	// the schema version this binary knows about (recorded in
	// schema_migrations), the service applies the missing numbered migrations on
	// boot. Defaults to true when unset, so every service in every environment
	// (including production) upgrades its schema without operator intervention.
	// Set to false to disable.
	AutoMigrate *bool `yaml:"auto_migrate"`
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
	SecretKey         string `yaml:"secret_key"`
	ExpiryHours       int    `yaml:"expiry_hours"`
	RefreshExpiryDays int    `yaml:"refresh_expiry_days"`
}

// StorageConfig holds S3-compatible object storage configuration.
type StorageConfig struct {
	Endpoint       string `yaml:"endpoint"`
	AccessKey      string `yaml:"access_key"`
	SecretKey      string `yaml:"secret_key"`
	Bucket         string `yaml:"bucket"`
	Region         string `yaml:"region"`
	UseSSL         bool   `yaml:"use_ssl"`
	PublicBaseURL  string `yaml:"public_base_url"`
	MaxUploadBytes int64  `yaml:"max_upload_bytes"`
	MaxAttachments int    `yaml:"max_attachments_per_post"`
}

// ServicesConfig holds the base URLs of the internal microservices.
type ServicesConfig struct {
	Account string `yaml:"account"`
	Storage string `yaml:"storage"`
	Chat    string `yaml:"chat"`
	Posts   string `yaml:"posts"`
	Push    string `yaml:"push"`
}

// GameConfig holds gameplay / level-system configuration.
type GameConfig struct {
	// DailyBonusExp is the amount of exp granted once per server-day on the
	// user's first authenticated request. Defaults to 100.
	DailyBonusExp int64 `yaml:"daily_bonus_exp"`
	// Timezone is the IANA timezone name (e.g. "Asia/Shanghai") the server
	// uses to compute "calendar day" for the daily bonus. Defaults to UTC.
	Timezone string `yaml:"timezone"`
}

// Config holds all configuration for the server.
type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Database DatabaseConfig `yaml:"database"`
	Redis    RedisConfig    `yaml:"redis"`
	OIDC     OIDCConfig     `yaml:"oidc"`
	JWT      JWTConfig      `yaml:"jwt"`
	Storage  StorageConfig  `yaml:"storage"`
	Services ServicesConfig `yaml:"services"`
	Game     GameConfig     `yaml:"game"`
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
	if v := os.Getenv("DATABASE_MIGRATE"); v != "" {
		c.Database.Migrate = v == "true" || v == "1"
	}
	if v := os.Getenv("DATABASE_AUTO_MIGRATE"); v != "" {
		enabled := v == "true" || v == "1"
		c.Database.AutoMigrate = &enabled
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
	if v := os.Getenv("JWT_EXPIRY_HOURS"); v != "" {
		fmt.Sscanf(v, "%d", &c.JWT.ExpiryHours)
	}
	if v := os.Getenv("JWT_REFRESH_EXPIRY_DAYS"); v != "" {
		fmt.Sscanf(v, "%d", &c.JWT.RefreshExpiryDays)
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
	if v := os.Getenv("STORAGE_REGION"); v != "" {
		c.Storage.Region = v
	}
	if v := os.Getenv("STORAGE_PUBLIC_BASE_URL"); v != "" {
		c.Storage.PublicBaseURL = v
	}
	if v := os.Getenv("SERVICE_ACCOUNT"); v != "" {
		c.Services.Account = v
	}
	if v := os.Getenv("SERVICE_STORAGE"); v != "" {
		c.Services.Storage = v
	}
	if v := os.Getenv("SERVICE_CHAT"); v != "" {
		c.Services.Chat = v
	}
	if v := os.Getenv("SERVICE_POSTS"); v != "" {
		c.Services.Posts = v
	}
	if v := os.Getenv("SERVICE_PUSH"); v != "" {
		c.Services.Push = v
	}
	if v := os.Getenv("GAME_DAILY_BONUS_EXP"); v != "" {
		fmt.Sscanf(v, "%d", &c.Game.DailyBonusExp)
	}
	if v := os.Getenv("GAME_TIMEZONE"); v != "" {
		c.Game.Timezone = v
	}
}

// ServicePort returns the listen port for a named internal service.
// It checks SERVICE_<NAME>_PORT (e.g. SERVICE_ACCOUNT_PORT) first, then derives
// the port from the service's configured base URL, and finally falls back to server.port.
func (c *Config) ServicePort(name string) string {
	env := "SERVICE_" + name + "_PORT"
	if v := os.Getenv(env); v != "" {
		return v
	}
	var base string
	switch name {
	case "ACCOUNT":
		base = c.Services.Account
	case "STORAGE":
		base = c.Services.Storage
	case "CHAT":
		base = c.Services.Chat
	case "POSTS":
		base = c.Services.Posts
	case "PUSH":
		base = c.Services.Push
	}
	if base != "" {
		if u, err := url.Parse(base); err == nil {
			if u.Port() != "" {
				return u.Port()
			}
		}
	}
	return c.Server.Port
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

// EffectiveDailyBonus returns the configured daily login exp grant, or 100
// when unset / non-positive so a missing key still gives the documented
// baseline behavior.
func (c *Config) EffectiveDailyBonus() int64 {
	if c.Game.DailyBonusExp <= 0 {
		return 100
	}
	return c.Game.DailyBonusExp
}

// Location returns the IANA timezone configured for the daily bonus, or UTC
// when unset / invalid. Errors are swallowed so a typo in the config doesn't
// crash the service.
func (g GameConfig) Location() *time.Location {
	if g.Timezone == "" {
		return time.UTC
	}
	if loc, err := time.LoadLocation(g.Timezone); err == nil {
		return loc
	}
	return time.UTC
}
