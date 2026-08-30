// Package config loads media-service configuration from environment
// variables, with sane defaults for local development.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds all runtime configuration for the service.
type Config struct {
	Env string // "development" or "production"

	HTTPPort            string
	HTTPReadTimeout     time.Duration
	HTTPWriteTimeout    time.Duration
	HTTPIdleTimeout     time.Duration
	HTTPShutdownTimeout time.Duration

	DatabaseURL            string
	DatabaseConnectTimeout time.Duration

	LogLevel string // "debug", "info", "warn", "error"

	// AuthJWKSURL points at auth-service's public key endpoint
	// (GET /.well-known/jwks.json), used to verify access tokens without
	// ever holding a key that could mint one.
	AuthJWKSURL string

	// ApiaryServiceURL / HiveServiceURL are apiary-service's and
	// hive-service's base URLs. A file can only be attached to an apiary
	// or hive the caller owns, and those services are the only source of
	// truth for that: this service asks whichever one applies, once, at
	// upload time.
	ApiaryServiceURL string
	HiveServiceURL   string

	// MaxUploadSizeBytes bounds how large a single uploaded file can be.
	// File bytes are stored directly in PostgreSQL rows (see the README's
	// Storage section), so this is kept conservative by default.
	MaxUploadSizeBytes int64
}

// Load builds a Config from environment variables, falling back to
// defaults suitable for local development where a variable is unset.
func Load() (*Config, error) {
	cfg := &Config{
		Env: getEnv("APP_ENV", "development"),

		HTTPPort:            getEnv("HTTP_PORT", "8080"),
		HTTPReadTimeout:     getDuration("HTTP_READ_TIMEOUT", 5*time.Second),
		HTTPWriteTimeout:    getDuration("HTTP_WRITE_TIMEOUT", 10*time.Second),
		HTTPIdleTimeout:     getDuration("HTTP_IDLE_TIMEOUT", 60*time.Second),
		HTTPShutdownTimeout: getDuration("HTTP_SHUTDOWN_TIMEOUT", 15*time.Second),

		DatabaseURL:            getEnv("DATABASE_URL", ""),
		DatabaseConnectTimeout: getDuration("DATABASE_CONNECT_TIMEOUT", 5*time.Second),

		LogLevel: getEnv("LOG_LEVEL", "info"),

		AuthJWKSURL:      getEnv("AUTH_JWKS_URL", ""),
		ApiaryServiceURL: getEnv("APIARY_SERVICE_URL", ""),
		HiveServiceURL:   getEnv("HIVE_SERVICE_URL", ""),

		MaxUploadSizeBytes: getInt64("MAX_UPLOAD_SIZE_BYTES", 15*1024*1024),
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("config: DATABASE_URL is required")
	}
	if cfg.AuthJWKSURL == "" {
		return nil, fmt.Errorf("config: AUTH_JWKS_URL is required")
	}
	if cfg.ApiaryServiceURL == "" {
		return nil, fmt.Errorf("config: APIARY_SERVICE_URL is required")
	}
	if cfg.HiveServiceURL == "" {
		return nil, fmt.Errorf("config: HIVE_SERVICE_URL is required")
	}

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func getDuration(key string, fallback time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}

func getInt64(key string, fallback int64) int64 {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return fallback
	}
	return n
}
