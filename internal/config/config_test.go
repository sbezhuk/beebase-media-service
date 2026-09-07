package config

import (
	"testing"
)

func setRequiredBaseEnv(t *testing.T) {
	t.Helper()
	env := map[string]string{
		"DATABASE_URL":    "postgres://localhost/test",
		"REDIS_ADDR":      "localhost:6379",
		"AUTH_JWKS_URL":   "http://localhost/.well-known/jwks.json",
		"PUBLIC_BASE_URL": "http://localhost:8080",
		"STORAGE_BUCKET":  "test-bucket",
		"STORAGE_REGION":  "us-east-1",
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
}

func TestLoad_ValidMinimalConfig(t *testing.T) {
	setRequiredBaseEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.StorageBucket != "test-bucket" || cfg.StorageRegion != "us-east-1" {
		t.Errorf("unexpected storage config: %+v", cfg)
	}
}

func TestLoad_MissingStorageBucket(t *testing.T) {
	setRequiredBaseEnv(t)
	t.Setenv("STORAGE_BUCKET", "")

	if _, err := Load(); err == nil {
		t.Fatal("expected error when STORAGE_BUCKET is unset")
	}
}

func TestLoad_MissingStorageRegion(t *testing.T) {
	setRequiredBaseEnv(t)
	t.Setenv("STORAGE_REGION", "")

	if _, err := Load(); err == nil {
		t.Fatal("expected error when STORAGE_REGION is unset")
	}
}
