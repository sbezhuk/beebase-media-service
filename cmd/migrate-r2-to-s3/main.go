// Command migrate-r2-to-s3 copies every media object from the old
// Cloudflare R2 bucket to the new Amazon S3 bucket, preserving object
// keys exactly (so existing `media` rows in PostgreSQL - which only ever
// reference a deterministic key derived from their id, never a storage
// location - keep resolving without any database change) and content
// type. It never deletes or modifies anything in the R2 source.
//
// Two modes:
//
//	go run ./cmd/migrate-r2-to-s3 -mode=copy
//	go run ./cmd/migrate-r2-to-s3 -mode=verify
//
// copy is safe to re-run: it uses the same conditional
// ("create if absent") PutObject the service itself uses for uploads, so
// an object already present at the destination is skipped, not
// overwritten - an interrupted run resumes cleanly. verify then compares
// every source object against its destination copy (size and ETag) and
// reports any mismatch, without touching either bucket. Only after a
// clean verify should the application's STORAGE_* configuration be
// pointed at S3 - see the deployment report's migration procedure.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func main() {
	mode := flag.String("mode", "", "copy | verify")
	flag.Parse()

	if *mode != "copy" && *mode != "verify" {
		fmt.Fprintln(os.Stderr, "usage: migrate-r2-to-s3 -mode=copy|verify")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()

	srcClient, srcBucket, err := buildSourceClient(ctx)
	if err != nil {
		log.Fatalf("source (R2) client: %v", err)
	}
	dstClient, dstBucket, err := buildDestClient(ctx)
	if err != nil {
		log.Fatalf("destination (S3) client: %v", err)
	}

	m := &migrator{
		src: srcClient, srcBucket: srcBucket,
		dst: dstClient, dstBucket: dstBucket,
		log: log.Default(),
	}

	var report Report
	switch *mode {
	case "copy":
		report, err = m.Copy(ctx)
	case "verify":
		report, err = m.Verify(ctx)
	}

	report.Print(os.Stdout)

	if err != nil {
		log.Fatalf("%s failed: %v", *mode, err)
	}
	if *mode == "verify" && len(report.Mismatches) > 0 {
		os.Exit(1)
	}
}

// buildSourceClient builds the R2 client. R2 has no IAM-role concept, so
// static credentials are required here regardless of how the destination
// is authenticated.
func buildSourceClient(ctx context.Context) (*s3.Client, string, error) {
	endpoint := requireEnv("SOURCE_R2_ENDPOINT")
	bucket := requireEnv("SOURCE_R2_BUCKET")
	accessKeyID := requireEnv("SOURCE_R2_ACCESS_KEY_ID")
	secretAccessKey := requireEnv("SOURCE_R2_SECRET_ACCESS_KEY")

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("auto"),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, ""),
		),
	)
	if err != nil {
		return nil, "", fmt.Errorf("load config: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true // R2 requires path-style addressing
	})
	return client, bucket, nil
}

// buildDestClient builds the S3 client. Mirrors media-service's own
// STORAGE_* configuration: DEST_STORAGE_ACCESS_KEY_ID/SECRET_ACCESS_KEY
// are optional and should normally be left unset, letting the SDK's
// default credential chain resolve credentials itself (e.g. the EC2
// instance role, if this tool is ever run from the production host
// instead of a developer machine).
func buildDestClient(ctx context.Context) (*s3.Client, string, error) {
	bucket := requireEnv("DEST_STORAGE_BUCKET")
	region := requireEnv("DEST_STORAGE_REGION")
	endpoint := os.Getenv("DEST_STORAGE_ENDPOINT")
	accessKeyID := os.Getenv("DEST_STORAGE_ACCESS_KEY_ID")
	secretAccessKey := os.Getenv("DEST_STORAGE_SECRET_ACCESS_KEY")

	opts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(region)}
	if accessKeyID != "" && secretAccessKey != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, ""),
		))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, "", fmt.Errorf("load config: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
		}
		o.UsePathStyle = os.Getenv("DEST_STORAGE_FORCE_PATH_STYLE") == "true"
	})
	return client, bucket, nil
}

func requireEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("%s is required", key)
	}
	return v
}
