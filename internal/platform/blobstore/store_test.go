package blobstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/sbezhuk/beebase-media-service/internal/domain/media"
)

// fakeS3 is a minimal S3-compatible HTTP server covering exactly the
// operations Store uses (HeadBucket, PutObject with If-None-Match,
// GetObject, DeleteObject), including S3's real XML error shapes for the
// two conditions Store distinguishes (NoSuchKey, PreconditionFailed).
// This exercises Store's actual HTTP-level behavior against the real AWS
// SDK client, without requiring LocalStack or real AWS/R2 credentials.
type fakeS3 struct {
	mu          sync.Mutex
	objects     map[string][]byte
	contentType map[string]string
}

func newFakeS3() *fakeS3 {
	return &fakeS3{
		objects:     make(map[string][]byte),
		contentType: make(map[string]string),
	}
}

func (f *fakeS3) server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(f.handle))
}

func (f *fakeS3) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	// Path-style addressing: /<bucket>/<key...>
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
	if len(parts) < 2 || parts[1] == "" {
		// HeadBucket: /<bucket>
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
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
<Error><Code>PreconditionFailed</Code><Message>At least one of the pre-conditions you specified did not hold</Message><Condition>If-None-Match</Condition><RequestId>test</RequestId></Error>`)
				return
			}
		}
		body := make([]byte, r.ContentLength)
		_, _ = io.ReadFull(r.Body, body)
		f.objects[key] = body
		f.contentType[key] = r.Header.Get("Content-Type")
		w.Header().Set("ETag", `"fake-etag"`)
		w.WriteHeader(http.StatusOK)

	case http.MethodGet:
		content, ok := f.objects[key]
		if !ok {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>
<Error><Code>NoSuchKey</Code><Message>The specified key does not exist.</Message><Key>`+key+`</Key><RequestId>test</RequestId></Error>`)
			return
		}
		w.Header().Set("Content-Type", f.contentType[key])
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(content)

	case http.MethodDelete:
		delete(f.objects, key)
		delete(f.contentType, key)
		w.WriteHeader(http.StatusNoContent)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func newTestStore(t *testing.T, fake *fakeS3) *Store {
	t.Helper()
	srv := fake.server()
	t.Cleanup(srv.Close)

	store, err := New(context.Background(), Config{
		Bucket:         "test-bucket",
		Region:         "us-east-1",
		Endpoint:       srv.URL,
		ForcePathStyle: true,
		// Dummy static credentials, purely so the SDK never falls through
		// to its default chain and picks up whatever real AWS config
		// happens to be on the machine running the test (e.g. a
		// developer's own AWS CLI login) - the fake server never checks
		// the signature these produce.
		AccessKeyID:     "test",
		SecretAccessKey: "test",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return store
}

func TestStore_PutGet_RoundTrip(t *testing.T) {
	fake := newFakeS3()
	store := newTestStore(t, fake)
	ctx := context.Background()
	id := uuid.New()

	content := []byte("hello beebase")
	if err := store.Put(ctx, id, content, "text/plain"); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := store.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("Get returned %q, want %q", got, content)
	}
}

func TestStore_Put_ContentType(t *testing.T) {
	fake := newFakeS3()
	store := newTestStore(t, fake)
	ctx := context.Background()
	id := uuid.New()

	if err := store.Put(ctx, id, []byte("data"), "image/png"); err != nil {
		t.Fatalf("Put: %v", err)
	}

	fake.mu.Lock()
	got := fake.contentType[ObjectKey(id)]
	fake.mu.Unlock()

	if got != "image/png" {
		t.Errorf("Content-Type sent to storage = %q, want %q", got, "image/png")
	}
}

func TestStore_Put_AlreadyExists(t *testing.T) {
	fake := newFakeS3()
	store := newTestStore(t, fake)
	ctx := context.Background()
	id := uuid.New()

	if err := store.Put(ctx, id, []byte("first"), "text/plain"); err != nil {
		t.Fatalf("first Put: %v", err)
	}

	err := store.Put(ctx, id, []byte("second"), "text/plain")
	if !errors.Is(err, media.ErrBlobAlreadyExists) {
		t.Fatalf("second Put error = %v, want media.ErrBlobAlreadyExists", err)
	}

	// The original content must be untouched by the rejected write.
	got, err := store.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "first" {
		t.Errorf("content after rejected overwrite = %q, want %q", got, "first")
	}
}

func TestStore_Get_NotFound(t *testing.T) {
	fake := newFakeS3()
	store := newTestStore(t, fake)
	ctx := context.Background()

	_, err := store.Get(ctx, uuid.New())
	if !errors.Is(err, media.ErrBlobNotFound) {
		t.Fatalf("Get error = %v, want media.ErrBlobNotFound", err)
	}
}

func TestStore_Delete_RemovesObject(t *testing.T) {
	fake := newFakeS3()
	store := newTestStore(t, fake)
	ctx := context.Background()
	id := uuid.New()

	if err := store.Put(ctx, id, []byte("data"), "text/plain"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := store.Delete(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := store.Get(ctx, id)
	if !errors.Is(err, media.ErrBlobNotFound) {
		t.Fatalf("Get after Delete error = %v, want media.ErrBlobNotFound", err)
	}
}

func TestStore_Delete_NonexistentIsNotAnError(t *testing.T) {
	fake := newFakeS3()
	store := newTestStore(t, fake)

	if err := store.Delete(context.Background(), uuid.New()); err != nil {
		t.Fatalf("Delete of nonexistent object should be idempotent, got: %v", err)
	}
}

func TestStore_ObjectKey_HasMediaPrefix(t *testing.T) {
	id := uuid.New()
	key := ObjectKey(id)
	want := "media/" + id.String()
	if key != want {
		t.Errorf("ObjectKey = %q, want %q (existing DB rows depend on this exact scheme)", key, want)
	}
}

func TestNew_UnreachableBucket_FailsFast(t *testing.T) {
	// No server listening on this endpoint at all.
	_, err := New(context.Background(), Config{
		Bucket:          "test-bucket",
		Region:          "us-east-1",
		Endpoint:        "http://127.0.0.1:1", // reserved, nothing listens here
		ForcePathStyle:  true,
		AccessKeyID:     "test",
		SecretAccessKey: "test",
	})
	if err == nil {
		t.Fatal("New with an unreachable endpoint should fail fast (HeadBucket), got nil error")
	}
}
