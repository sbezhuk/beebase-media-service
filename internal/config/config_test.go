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
	if cfg.StorageEndpoint != "" || cfg.StorageAccessKeyID != "" || cfg.StorageSecretAccessKey != "" {
		t.Errorf("expected empty optional storage fields by default, got: endpoint=%q accessKeyID=%q secretAccessKey=%q",
			cfg.StorageEndpoint, cfg.StorageAccessKeyID, cfg.StorageSecretAccessKey)
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

func TestLoad_PartialStorageCredentials_Rejected(t *testing.T) {
	setRequiredBaseEnv(t)
	t.Setenv("STORAGE_ACCESS_KEY_ID", "only-the-id")
	// STORAGE_SECRET_ACCESS_KEY intentionally left unset.

	if _, err := Load(); err == nil {
		t.Fatal("expected error when only STORAGE_ACCESS_KEY_ID is set without STORAGE_SECRET_ACCESS_KEY")
	}
}

func TestLoad_BothStorageCredentials_Accepted(t *testing.T) {
	setRequiredBaseEnv(t)
	t.Setenv("STORAGE_ACCESS_KEY_ID", "id")
	t.Setenv("STORAGE_SECRET_ACCESS_KEY", "secret")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.StorageAccessKeyID != "id" || cfg.StorageSecretAccessKey != "secret" {
		t.Errorf("credentials not loaded correctly: %+v", cfg)
	}
}

func TestLoad_NeitherStorageCredential_Accepted(t *testing.T) {
	setRequiredBaseEnv(t)
	// Neither set - the production/IAM-role case.

	if _, err := Load(); err != nil {
		t.Fatalf("Load should succeed with no static storage credentials (IAM role case): %v", err)
	}
}
