// Package blobstore implements domain/media.BlobStore against Amazon S3.
package blobstore

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

// Config configures a Store.
//
// Bucket and Region are the only fields the application itself ever
// sets. Endpoint, AccessKeyID, SecretAccessKey, and ForcePathStyle exist
// solely so this package's own tests can point the real AWS SDK client
// at an in-process fake S3 server (see store_test.go) - they are never
// set from application configuration. Left empty, the AWS SDK resolves
// S3's standard regional endpoint, virtual-hosted-style addressing, and
// - in production - credentials from the EC2 instance's IAM role via the
// SDK's default credential chain, with no long-lived key ever needing to
// exist.
type Config struct {
	Bucket          string
	Region          string
	Endpoint        string
	AccessKeyID     string
	SecretAccessKey string
	ForcePathStyle  bool
}

// Store implements domain/media.BlobStore against a single S3 (or
// S3-compatible) bucket, keyed deterministically by media ID.
type Store struct {
	client *s3.Client
	bucket string
}

// New builds a Store from cfg and verifies the bucket is reachable
// (HeadBucket) before returning, so a misconfigured deployment fails at
// boot rather than on the first upload - the same "fail fast" contract
// platform/postgres.New already has for the database connection.
func New(ctx context.Context, cfg Config) (*Store, error) {
	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.Region),
	}

	// Static credentials are the exception, not the default: only used
	// by this package's own tests, against a fake server. Everywhere
	// else, LoadDefaultConfig with no credentials override resolves them
	// from the SDK's normal chain - on the production EC2 host, that
	// means the instance role.
	if cfg.AccessKeyID != "" && cfg.SecretAccessKey != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("blobstore: load client config: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
		o.UsePathStyle = cfg.ForcePathStyle
	})

	store := &Store{client: client, bucket: cfg.Bucket}

	if _, err := client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(cfg.Bucket)}); err != nil {
		return nil, fmt.Errorf("blobstore: head bucket %s: %w", cfg.Bucket, err)
	}

	return store, nil
}

// ObjectKey returns the deterministic object key for mediaID. Exported
// so tools outside this package (e.g. a one-off storage migration) can
// compute the exact same key without duplicating the scheme.
func ObjectKey(mediaID uuid.UUID) string {
	return keyPrefix + mediaID.String()
}

// Put implements domain/media.BlobStore. It uses S3's conditional-write
// support (If-None-Match: *) to make the write a true "create if absent":
// a second Put for the same mediaID can never overwrite bytes already
// stored under that key, it just reports media.ErrBlobAlreadyExists.
func (s *Store) Put(ctx context.Context, mediaID uuid.UUID, content []byte, contentType string) error {
	key := ObjectKey(mediaID)

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
		return fmt.Errorf("blobstore: put object %s: %w", key, err)
	}

	return nil
}

func (s *Store) Get(ctx context.Context, mediaID uuid.UUID) ([]byte, error) {
	key := ObjectKey(mediaID)

	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var nsk *types.NoSuchKey
		if errors.As(err, &nsk) {
			return nil, media.ErrBlobNotFound
		}
		return nil, fmt.Errorf("blobstore: get object %s: %w", key, err)
	}
	defer func() { _ = out.Body.Close() }()

	content, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, fmt.Errorf("blobstore: read object %s: %w", key, err)
	}

	return content, nil
}

// Delete removes mediaID's object. Deleting a key that doesn't exist is
// not an error - S3's DeleteObject is itself idempotent that way,
// matching domain/media.BlobStore's documented contract.
func (s *Store) Delete(ctx context.Context, mediaID uuid.UUID) error {
	key := ObjectKey(mediaID)

	if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}); err != nil {
		return fmt.Errorf("blobstore: delete object %s: %w", key, err)
	}

	return nil
}

// isPreconditionFailed reports whether err is the S3 API's response to a
// failed If-None-Match condition on PutObject (HTTP 412, error code
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
