package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	smithy "github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

type migrator struct {
	src       *s3.Client
	srcBucket string
	dst       *s3.Client
	dstBucket string
	log       *log.Logger
}

// Report summarizes one Copy or Verify run.
type Report struct {
	TotalObjects int
	Copied       int
	AlreadyAtDst int // Copy only: object already existed at the destination
	Verified     int // Verify only: object matched
	Failed       []string
	Mismatches   []string
}

func (r Report) Print(w io.Writer) {
	fmt.Fprintf(w, "\n--- migration report ---\n")
	fmt.Fprintf(w, "total source objects: %d\n", r.TotalObjects)
	if r.Copied > 0 || r.AlreadyAtDst > 0 {
		fmt.Fprintf(w, "copied: %d\n", r.Copied)
		fmt.Fprintf(w, "already at destination (skipped): %d\n", r.AlreadyAtDst)
	}
	if r.Verified > 0 {
		fmt.Fprintf(w, "verified matching: %d\n", r.Verified)
	}
	fmt.Fprintf(w, "failed: %d\n", len(r.Failed))
	for _, k := range r.Failed {
		fmt.Fprintf(w, "  FAILED  %s\n", k)
	}
	fmt.Fprintf(w, "mismatches: %d\n", len(r.Mismatches))
	for _, k := range r.Mismatches {
		fmt.Fprintf(w, "  MISMATCH  %s\n", k)
	}
}

// Copy enumerates every object in the source bucket and copies it to the
// destination bucket, preserving key and content type exactly. Copying
// is a conditional ("create if absent") PutObject - the same idiom
// media-service's own BlobStore.Put uses - so re-running Copy after a
// partial/interrupted run only copies what's still missing.
func (m *migrator) Copy(ctx context.Context) (Report, error) {
	var report Report

	err := m.forEachSourceObject(ctx, func(key string, size int64) error {
		report.TotalObjects++

		obj, err := m.src.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(m.srcBucket),
			Key:    aws.String(key),
		})
		if err != nil {
			report.Failed = append(report.Failed, key)
			m.log.Printf("get %s from source: %v", key, err)
			return nil // keep going - a single bad object shouldn't abort the whole run
		}
		content, err := io.ReadAll(obj.Body)
		_ = obj.Body.Close()
		if err != nil {
			report.Failed = append(report.Failed, key)
			m.log.Printf("read %s from source: %v", key, err)
			return nil
		}

		contentType := ""
		if obj.ContentType != nil {
			contentType = *obj.ContentType
		}

		_, err = m.dst.PutObject(ctx, &s3.PutObjectInput{
			Bucket:      aws.String(m.dstBucket),
			Key:         aws.String(key),
			Body:        bytes.NewReader(content),
			ContentType: aws.String(contentType),
			IfNoneMatch: aws.String("*"),
		})
		switch {
		case err == nil:
			report.Copied++
			m.log.Printf("copied %s (%d bytes, %s)", key, size, contentType)
		case isPreconditionFailed(err):
			report.AlreadyAtDst++
		default:
			report.Failed = append(report.Failed, key)
			m.log.Printf("put %s to destination: %v", key, err)
		}
		return nil
	})

	return report, err
}

// Verify compares every source object against its destination copy by
// size and ETag, without transferring or modifying either bucket's
// content (HeadObject only). Any object missing at the destination, or
// present with a different size/ETag, is reported as a mismatch.
func (m *migrator) Verify(ctx context.Context) (Report, error) {
	var report Report

	err := m.forEachSourceObject(ctx, func(key string, srcSize int64) error {
		report.TotalObjects++

		srcHead, err := m.src.HeadObject(ctx, &s3.HeadObjectInput{
			Bucket: aws.String(m.srcBucket), Key: aws.String(key),
		})
		if err != nil {
			report.Failed = append(report.Failed, key)
			m.log.Printf("head %s on source: %v", key, err)
			return nil
		}

		dstHead, err := m.dst.HeadObject(ctx, &s3.HeadObjectInput{
			Bucket: aws.String(m.dstBucket), Key: aws.String(key),
		})
		if err != nil {
			report.Mismatches = append(report.Mismatches, key+" (missing at destination)")
			return nil
		}

		srcSizeVal, dstSizeVal := aws.ToInt64(srcHead.ContentLength), aws.ToInt64(dstHead.ContentLength)
		srcETag, dstETag := aws.ToString(srcHead.ETag), aws.ToString(dstHead.ETag)

		if srcSizeVal != dstSizeVal || srcETag != dstETag {
			report.Mismatches = append(report.Mismatches, fmt.Sprintf(
				"%s (size %d/%d, etag %s/%s)", key, srcSizeVal, dstSizeVal, srcETag, dstETag))
			return nil
		}

		report.Verified++
		return nil
	})

	return report, err
}

func (m *migrator) forEachSourceObject(ctx context.Context, fn func(key string, size int64) error) error {
	var token *string
	for {
		out, err := m.src.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(m.srcBucket),
			ContinuationToken: token,
		})
		if err != nil {
			return fmt.Errorf("list source objects: %w", err)
		}
		for _, obj := range out.Contents {
			if err := fn(aws.ToString(obj.Key), aws.ToInt64(obj.Size)); err != nil {
				return err
			}
		}
		if out.IsTruncated == nil || !*out.IsTruncated {
			return nil
		}
		token = out.NextContinuationToken
	}
}

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
