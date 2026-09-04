// Command migrate-media-blobs is BEEB-32's one-time (but safely
// re-runnable) migration of existing media content out of the database
// and into Cloudflare R2. It uploads every row still in the pre-R2
// media_blobs table and removes that row once the upload is confirmed
// stored - see internal/migration/blobmigrator for why that ordering
// makes the whole operation safe to interrupt and simply re-run.
//
// It reads DATABASE_URL and the R2_* variables directly rather than
// through internal/config, so running it doesn't require unrelated
// service configuration (AUTH_JWKS_URL, ...) to be set - the same reason
// cmd/dbclear does the same thing.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/joho/godotenv"

	"github.com/sbezhuk/beebase-media-service/internal/migration/blobmigrator"
	"github.com/sbezhuk/beebase-media-service/internal/platform/postgres"
	"github.com/sbezhuk/beebase-media-service/internal/platform/r2"
	repopostgres "github.com/sbezhuk/beebase-media-service/internal/repository/postgres"

	"github.com/sbezhuk/beebase-common/logger"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "migrate-media-blobs:", err)
		os.Exit(1)
	}
}

func run() error {
	batchSize := flag.Int("batch-size", 50, "number of blobs to migrate per database round-trip")
	flag.Parse()

	// .env is optional: present in local dev, absent in production/containers.
	_ = godotenv.Load()

	dsn, err := requiredEnv("DATABASE_URL")
	if err != nil {
		return err
	}
	endpoint, err := requiredEnv("R2_ENDPOINT")
	if err != nil {
		return err
	}
	bucket, err := requiredEnv("R2_BUCKET")
	if err != nil {
		return err
	}
	accessKeyID, err := requiredEnv("R2_ACCESS_KEY_ID")
	if err != nil {
		return err
	}
	secretAccessKey, err := requiredEnv("R2_SECRET_ACCESS_KEY")
	if err != nil {
		return err
	}

	ctx := context.Background()
	log := logger.New(getEnv("APP_ENV", "production"), getEnv("LOG_LEVEL", "info"))

	pool, err := postgres.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer pool.Close()

	store, err := r2.New(ctx, r2.Config{
		Endpoint:        endpoint,
		Bucket:          bucket,
		AccessKeyID:     accessKeyID,
		SecretAccessKey: secretAccessKey,
	})
	if err != nil {
		return fmt.Errorf("connect to r2: %w", err)
	}

	legacy := repopostgres.NewLegacyBlobStore(pool)

	migrated, migrateErr := blobmigrator.Migrate(ctx, legacy, store, *batchSize, log)
	fmt.Printf("migrated %d blob(s) to R2\n", migrated)
	if migrateErr != nil {
		return migrateErr
	}

	fmt.Println("done - every media_blobs row has been migrated")
	return nil
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func requiredEnv(key string) (string, error) {
	v := os.Getenv(key)
	if v == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return v, nil
}
