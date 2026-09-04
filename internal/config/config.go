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

	// MaxUploadSizeBytes bounds how large a single uploaded file can be.
	MaxUploadSizeBytes int64

	// R2Endpoint is Cloudflare R2's jurisdiction-specific S3 API endpoint
	// for the account (e.g. https://<account-hash>.r2.cloudflarestorage.com).
	// File content is stored there, not in PostgreSQL - see the README's
	// Storage section.
	R2Endpoint        string
	R2Bucket          string
	R2AccessKeyID     string
	R2SecretAccessKey string
	R2ConnectTimeout  time.Duration
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

		AuthJWKSURL: getEnv("AUTH_JWKS_URL", ""),

		MaxUploadSizeBytes: getInt64("MAX_UPLOAD_SIZE_BYTES", 15*1024*1024),

		R2Endpoint:        getEnv("R2_ENDPOINT", ""),
		R2Bucket:          getEnv("R2_BUCKET", ""),
		R2AccessKeyID:     getEnv("R2_ACCESS_KEY_ID", ""),
		R2SecretAccessKey: getEnv("R2_SECRET_ACCESS_KEY", ""),
		R2ConnectTimeout:  getDuration("R2_CONNECT_TIMEOUT", 5*time.Second),
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("config: DATABASE_URL is required")
	}
	if cfg.AuthJWKSURL == "" {
		return nil, fmt.Errorf("config: AUTH_JWKS_URL is required")
	}
	if cfg.R2Endpoint == "" {
		return nil, fmt.Errorf("config: R2_ENDPOINT is required")
	}
	if cfg.R2Bucket == "" {
		return nil, fmt.Errorf("config: R2_BUCKET is required")
	}
	if cfg.R2AccessKeyID == "" {
		return nil, fmt.Errorf("config: R2_ACCESS_KEY_ID is required")
	}
	if cfg.R2SecretAccessKey == "" {
		return nil, fmt.Errorf("config: R2_SECRET_ACCESS_KEY is required")
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
