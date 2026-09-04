// Package r2 implements domain/media.BlobStore against Cloudflare R2.
// R2 is S3-API-compatible, so this is a thin wrapper around the AWS SDK's
// S3 client pointed at R2's endpoint - nothing outside this package
// (see domain/media.BlobStore) knows or needs to know that.
package r2

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithy "github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/google/uuid"

	"github.com/sbezhuk/beebase-media-service/internal/domain/media"
)

// keyPrefix namespaces every object this service writes, in case the
// bucket is ever shared with something else.
const keyPrefix = "media/"

// Config configures a Store. Endpoint is the full jurisdiction-specific
// R2 endpoint URL (e.g. https://<account-hash>.r2.cloudflarestorage.com,
// or an EU/FedRAMP jurisdiction variant); R2 has no separate "region"
// concept, so none is exposed here.
type Config struct {
	Endpoint        string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
}

// Store implements domain/media.BlobStore against a single R2 bucket,
// keyed deterministically by media ID.
type Store struct {
	client *s3.Client
	bucket string
}

// New builds a Store from cfg and verifies the bucket is reachable
// (HeadBucket) before returning, so a misconfigured deployment fails at
// boot rather than on the first upload - the same "fail fast" contract
// platform/postgres.New already has for the database connection.
func New(ctx context.Context, cfg Config) (*Store, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		// R2 has no AWS regions; "auto" is R2's own documented value and
		// is never actually sent anywhere meaningful once BaseEndpoint is
		// set below, but the SDK requires some non-empty region.
		awsconfig.WithRegion("auto"),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("r2: load client config: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(cfg.Endpoint)
		// R2 requires path-style bucket addressing
		// (https://<endpoint>/<bucket>/<key>), not virtual-hosted-style.
		o.UsePathStyle = true
	})

	store := &Store{client: client, bucket: cfg.Bucket}

	if _, err := client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(cfg.Bucket)}); err != nil {
		return nil, fmt.Errorf("r2: head bucket %s: %w", cfg.Bucket, err)
	}

	return store, nil
}

func objectKey(mediaID uuid.UUID) string {
	return keyPrefix + mediaID.String()
}

// Put implements domain/media.BlobStore. It uses R2's conditional-write
// support (If-None-Match: *) to make the write a true "create if absent":
// a second Put for the same mediaID can never overwrite bytes already
// stored under that key, it just reports media.ErrBlobAlreadyExists.
func (s *Store) Put(ctx context.Context, mediaID uuid.UUID, content []byte, contentType string) error {
	key := objectKey(mediaID)

	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(content),
		ContentType: aws.String(contentType),
		IfNoneMatch: aws.String("*"),
	})
	if err != nil {
		if isPreconditionFailed(err) {
			return media.ErrBlobAlreadyExists
		}
		return fmt.Errorf("r2: put object %s: %w", key, err)
	}

	return nil
}

func (s *Store) Get(ctx context.Context, mediaID uuid.UUID) ([]byte, error) {
	key := objectKey(mediaID)

	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var nsk *types.NoSuchKey
		if errors.As(err, &nsk) {
			return nil, media.ErrBlobNotFound
		}
		return nil, fmt.Errorf("r2: get object %s: %w", key, err)
	}
	defer func() { _ = out.Body.Close() }()

	content, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, fmt.Errorf("r2: read object %s: %w", key, err)
	}

	return content, nil
}

// Delete removes mediaID's object. Deleting a key that doesn't exist is
// not an error - S3's (and R2's) DeleteObject is itself idempotent that
// way, matching domain/media.BlobStore's documented contract.
func (s *Store) Delete(ctx context.Context, mediaID uuid.UUID) error {
	key := objectKey(mediaID)

	if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}); err != nil {
		return fmt.Errorf("r2: delete object %s: %w", key, err)
	}

	return nil
}

// isPreconditionFailed reports whether err is the S3/R2 API's response to
// a failed If-None-Match condition on PutObject (HTTP 412, error code
// "PreconditionFailed") - i.e. an object already exists at that key.
func isPreconditionFailed(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && apiErr.ErrorCode() == "PreconditionFailed" {
		return true
	}

	var respErr *smithyhttp.ResponseError
	if errors.As(err, &respErr) && respErr.HTTPStatusCode() == http.StatusPreconditionFailed {
		return true
	}

	return false
}
