package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// fakeBucket is a minimal, single-bucket S3-compatible HTTP server
// covering HeadBucket, PutObject (with If-None-Match), GetObject,
// HeadObject, and ListObjectsV2 (single page - the small object counts
// these tests use never need pagination) - exactly what migrate.go uses.
// Two independent instances stand in for the R2 source and S3
// destination, so Copy/Verify are exercised end-to-end with no real AWS
// or R2 access, and no LocalStack.
type fakeBucket struct {
	mu          sync.Mutex
	objects     map[string][]byte
	contentType map[string]string
}

func newFakeBucket() *fakeBucket {
	return &fakeBucket{objects: map[string][]byte{}, contentType: map[string]string{}}
}

func etagFor(content []byte) string {
	sum := md5.Sum(content)
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

func (f *fakeBucket) server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(f.handle))
}

func (f *fakeBucket) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)

	// Bucket-root requests: HeadBucket, or ListObjectsV2 (?list-type=2).
	if len(parts) < 2 || parts[1] == "" {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2" {
			f.writeListObjectsV2(w)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		return
	}
	key := parts[1]

	switch r.Method {
	case http.MethodPut:
		if r.Header.Get("If-None-Match") == "*" {
			if _, exists := f.objects[key]; exists {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(http.StatusPreconditionFailed)
				_, _ = fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>
<Error><Code>PreconditionFailed</Code><Message>precondition failed</Message><RequestId>test</RequestId></Error>`)
				return
			}
		}
		body := make([]byte, r.ContentLength)
		_, _ = io.ReadFull(r.Body, body)
		f.objects[key] = body
		f.contentType[key] = r.Header.Get("Content-Type")
		w.Header().Set("ETag", etagFor(body))
		w.WriteHeader(http.StatusOK)

	case http.MethodGet:
		content, ok := f.objects[key]
		if !ok {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>
<Error><Code>NoSuchKey</Code><Message>not found</Message><Key>`+key+`</Key><RequestId>test</RequestId></Error>`)
			return
		}
		w.Header().Set("Content-Type", f.contentType[key])
		w.Header().Set("ETag", etagFor(content))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(content)

	case http.MethodHead:
		content, ok := f.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", f.contentType[key])
		w.Header().Set("ETag", etagFor(content))
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		w.WriteHeader(http.StatusOK)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (f *fakeBucket) writeListObjectsV2(w http.ResponseWriter) {
	var buf bytes.Buffer
	buf.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	buf.WriteString(`<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`)
	buf.WriteString(`<IsTruncated>false</IsTruncated>`)
	for key, content := range f.objects {
		buf.WriteString("<Contents>")
		buf.WriteString("<Key>" + key + "</Key>")
		buf.WriteString("<LastModified>" + time.Now().UTC().Format(time.RFC3339) + "</LastModified>")
		buf.WriteString("<ETag>" + etagFor(content) + "</ETag>")
		buf.WriteString("<Size>" + strconv.Itoa(len(content)) + "</Size>")
		buf.WriteString("<StorageClass>STANDARD</StorageClass>")
		buf.WriteString("</Contents>")
	}
	buf.WriteString(`</ListBucketResult>`)

	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

func newTestClient(t *testing.T, fake *fakeBucket) *s3.Client {
	t.Helper()
	srv := fake.server()
	t.Cleanup(srv.Close)

	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(srv.URL)
		o.UsePathStyle = true
	})
}

func TestMigrator_Copy_CopiesAllObjects(t *testing.T) {
	src := newFakeBucket()
	src.objects["media/a"] = []byte("photo a")
	src.contentType["media/a"] = "image/jpeg"
	src.objects["media/b"] = []byte("photo b")
	src.contentType["media/b"] = "image/png"

	dst := newFakeBucket()

	m := &migrator{
		src: newTestClient(t, src), srcBucket: "r2-bucket",
		dst: newTestClient(t, dst), dstBucket: "s3-bucket",
		log: log.New(io.Discard, "", 0),
	}

	report, err := m.Copy(context.Background())
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if report.TotalObjects != 2 || report.Copied != 2 || len(report.Failed) != 0 {
		t.Fatalf("unexpected report: %+v", report)
	}

	dst.mu.Lock()
	defer dst.mu.Unlock()
	if string(dst.objects["media/a"]) != "photo a" || dst.contentType["media/a"] != "image/jpeg" {
		t.Errorf("media/a not copied correctly: content=%q type=%q", dst.objects["media/a"], dst.contentType["media/a"])
	}
	if string(dst.objects["media/b"]) != "photo b" || dst.contentType["media/b"] != "image/png" {
		t.Errorf("media/b not copied correctly: content=%q type=%q", dst.objects["media/b"], dst.contentType["media/b"])
	}
}

func TestMigrator_Copy_IsResumable(t *testing.T) {
	src := newFakeBucket()
	src.objects["media/a"] = []byte("photo a")
	src.objects["media/b"] = []byte("photo b")

	dst := newFakeBucket()
	// Simulate a prior, interrupted run that already copied media/a.
	dst.objects["media/a"] = []byte("photo a")

	m := &migrator{
		src: newTestClient(t, src), srcBucket: "r2-bucket",
		dst: newTestClient(t, dst), dstBucket: "s3-bucket",
		log: log.New(io.Discard, "", 0),
	}

	report, err := m.Copy(context.Background())
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if report.Copied != 1 || report.AlreadyAtDst != 1 {
		t.Fatalf("expected 1 copied + 1 already-at-destination, got: %+v", report)
	}
}

func TestMigrator_Verify_AllMatch(t *testing.T) {
	src := newFakeBucket()
	src.objects["media/a"] = []byte("photo a")
	src.contentType["media/a"] = "image/jpeg"

	dst := newFakeBucket()
	dst.objects["media/a"] = []byte("photo a")
	dst.contentType["media/a"] = "image/jpeg"

	m := &migrator{
		src: newTestClient(t, src), srcBucket: "r2-bucket",
		dst: newTestClient(t, dst), dstBucket: "s3-bucket",
		log: log.New(io.Discard, "", 0),
	}

	report, err := m.Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if report.Verified != 1 || len(report.Mismatches) != 0 {
		t.Fatalf("expected 1 verified, 0 mismatches, got: %+v", report)
	}
}

func TestMigrator_Verify_DetectsMissingAndCorruptObjects(t *testing.T) {
	src := newFakeBucket()
	src.objects["media/missing"] = []byte("only in source")
	src.objects["media/corrupt"] = []byte("original content")
	src.objects["media/ok"] = []byte("same everywhere")

	dst := newFakeBucket()
	dst.objects["media/corrupt"] = []byte("DIFFERENT content!!")
	dst.objects["media/ok"] = []byte("same everywhere")

	m := &migrator{
		src: newTestClient(t, src), srcBucket: "r2-bucket",
		dst: newTestClient(t, dst), dstBucket: "s3-bucket",
		log: log.New(io.Discard, "", 0),
	}

	report, err := m.Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if report.Verified != 1 {
		t.Errorf("Verified = %d, want 1 (only media/ok)", report.Verified)
	}
	if len(report.Mismatches) != 2 {
		t.Fatalf("expected 2 mismatches (missing + corrupt), got %d: %v", len(report.Mismatches), report.Mismatches)
	}
}

func TestMigrator_CopyThenVerify_EndToEnd(t *testing.T) {
	src := newFakeBucket()
	for i := 0; i < 5; i++ {
		key := fmt.Sprintf("media/%d", i)
		src.objects[key] = []byte(fmt.Sprintf("content-%d", i))
		src.contentType[key] = "application/octet-stream"
	}

	dst := newFakeBucket()

	m := &migrator{
		src: newTestClient(t, src), srcBucket: "r2-bucket",
		dst: newTestClient(t, dst), dstBucket: "s3-bucket",
		log: log.New(io.Discard, "", 0),
	}

	copyReport, err := m.Copy(context.Background())
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if copyReport.Copied != 5 {
		t.Fatalf("Copy: expected 5 copied, got %+v", copyReport)
	}

	verifyReport, err := m.Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if verifyReport.Verified != 5 || len(verifyReport.Mismatches) != 0 {
		t.Fatalf("Verify after Copy: expected 5 verified, 0 mismatches, got %+v", verifyReport)
	}
}
