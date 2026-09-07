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

	// RedisAddr is the shared session store every BeeBase service checks on
	// every request, so an access token can be rejected the instant its
	// session is superseded by a newer one instead of staying valid until
	// its own JWT expiry.
	RedisAddr           string
	RedisConnectTimeout time.Duration

	LogLevel string // "debug", "info", "warn", "error"

	// AuthJWKSURL points at auth-service's public key endpoint
	// (GET /.well-known/jwks.json), used to verify access tokens without
	// ever holding a key that could mint one.
	AuthJWKSURL string

	// PublicBaseURL is the gateway's externally reachable base URL, used
	// to build the image_url returned for each media item
	// (PublicBaseURL + "/api/v1/media/{id}/download"). Unlike every other
	// *_URL setting in this service, it must resolve for the client, not
	// just for server-to-server calls.
	PublicBaseURL string

	// MaxUploadSizeBytes bounds how large a single uploaded file can be.
	MaxUploadSizeBytes int64

	// File content is stored in Amazon S3, not PostgreSQL - see the
	// README's Storage section. Credentials are always resolved by the
	// AWS SDK's default chain - in production, the EC2 instance's IAM
	// role - so there is no static access key configuration here.
	StorageBucket         string
	StorageRegion         string
	StorageConnectTimeout time.Duration
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

		RedisAddr:           getEnv("REDIS_ADDR", ""),
		RedisConnectTimeout: getDuration("REDIS_CONNECT_TIMEOUT", 5*time.Second),

		LogLevel: getEnv("LOG_LEVEL", "info"),

		AuthJWKSURL:   getEnv("AUTH_JWKS_URL", ""),
		PublicBaseURL: getEnv("PUBLIC_BASE_URL", ""),

		MaxUploadSizeBytes: getInt64("MAX_UPLOAD_SIZE_BYTES", 15*1024*1024),

		StorageBucket:         getEnv("STORAGE_BUCKET", ""),
		StorageRegion:         getEnv("STORAGE_REGION", ""),
		StorageConnectTimeout: getDuration("STORAGE_CONNECT_TIMEOUT", 5*time.Second),
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("config: DATABASE_URL is required")
	}
	if cfg.RedisAddr == "" {
		return nil, fmt.Errorf("config: REDIS_ADDR is required")
	}
	if cfg.AuthJWKSURL == "" {
		return nil, fmt.Errorf("config: AUTH_JWKS_URL is required")
	}
	if cfg.PublicBaseURL == "" {
		return nil, fmt.Errorf("config: PUBLIC_BASE_URL is required")
	}
	if cfg.StorageBucket == "" {
		return nil, fmt.Errorf("config: STORAGE_BUCKET is required")
	}
	if cfg.StorageRegion == "" {
		return nil, fmt.Errorf("config: STORAGE_REGION is required")
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
