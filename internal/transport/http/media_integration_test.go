//go:build integration

package http_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	appmedia "github.com/sbezhuk/beebase-media-service/internal/application/media"
	domainmedia "github.com/sbezhuk/beebase-media-service/internal/domain/media"
	mediarepo "github.com/sbezhuk/beebase-media-service/internal/repository/media"
	repopostgres "github.com/sbezhuk/beebase-media-service/internal/repository/postgres"
	transporthttp "github.com/sbezhuk/beebase-media-service/internal/transport/http"
	mediahttp "github.com/sbezhuk/beebase-media-service/internal/transport/http/media"

	"github.com/sbezhuk/beebase-common/authmw"
	"github.com/sbezhuk/beebase-common/jwks"
	"github.com/sbezhuk/beebase-common/logger"
)

const testKID = "test-kid"
const testMaxUploadSizeBytes = 1 << 20 // 1MB

// fakeBlobStore is an in-memory domain/media.BlobStore standing in for
// Cloudflare R2: these tests exercise the full HTTP flow against a real
// PostgreSQL database (for metadata) without needing a real R2 bucket,
// mirroring Put's real conditional-create semantics so idempotent-upload
// behavior is exercised the same way it would be in production.
type fakeBlobStore struct {
	mu      sync.Mutex
	objects map[uuid.UUID][]byte
}

func newFakeBlobStore() *fakeBlobStore {
	return &fakeBlobStore{objects: map[uuid.UUID][]byte{}}
}

func (f *fakeBlobStore) Put(_ context.Context, mediaID uuid.UUID, content []byte, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.objects[mediaID]; exists {
		return domainmedia.ErrBlobAlreadyExists
	}
	f.objects[mediaID] = append([]byte(nil), content...)
	return nil
}

func (f *fakeBlobStore) Get(_ context.Context, mediaID uuid.UUID) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.objects[mediaID]
	if !ok {
		return nil, domainmedia.ErrBlobNotFound
	}
	return c, nil
}

func (f *fakeBlobStore) Delete(_ context.Context, mediaID uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, mediaID)
	return nil
}

type testStack struct {
	server *httptest.Server
	priv   ed25519.PrivateKey
}

// newTestStack wires a full router against a real PostgreSQL database
// (every write scoped to a transaction rolled back at the end of the
// test) and a real JWKS server. media-service has no other services to
// fake out against - it doesn't know apiaries or hives exist.
func newTestStack(t *testing.T) *testStack {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping HTTP media integration test")
	}

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect to test database: %v", err)
	}
	t.Cleanup(pool.Close)

	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	jwksHandler, err := jwks.NewHandler(pub, testKID)
	if err != nil {
		t.Fatalf("jwks.NewHandler: %v", err)
	}
	jwksServer := httptest.NewServer(jwksHandler)
	t.Cleanup(jwksServer.Close)

	verifier, err := authmw.NewVerifierFromJWKSURL(context.Background(), jwksServer.URL)
	if err != nil {
		t.Fatalf("NewVerifierFromJWKSURL: %v", err)
	}

	log := logger.New("development", "error")
	metadataRepo := repopostgres.NewMediaRepository(tx)
	mediaRepo := mediarepo.New(metadataRepo, metadataRepo, newFakeBlobStore(), log)
	mediaService := appmedia.NewService(mediaRepo, testMaxUploadSizeBytes)
	handler := mediahttp.NewHandler(mediaService, log, testMaxUploadSizeBytes, "http://localhost:8080")

	router := transporthttp.NewRouter(log, pool, handler, verifier)

	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	return &testStack{server: srv, priv: priv}
}

func (s *testStack) tokenFor(t *testing.T, userID uuid.UUID) string {
	t.Helper()

	claims := jwt.RegisteredClaims{
		Subject:   userID.String(),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	token.Header["kid"] = testKID

	signed, err := token.SignedString(s.priv)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signed
}

func (s *testStack) get(t *testing.T, path, token string) *http.Response {
	t.Helper()
	return s.do(t, http.MethodGet, path, token, "", nil)
}

func (s *testStack) delete(t *testing.T, path, token string) *http.Response {
	t.Helper()
	return s.do(t, http.MethodDelete, path, token, "", nil)
}

func (s *testStack) do(t *testing.T, method, path, token, contentType string, body io.Reader) *http.Response {
	t.Helper()

	req, err := http.NewRequest(method, s.server.URL+path, body)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

// uploadOpts customizes buildUpload; zero-value fields fall back to
// reasonable defaults for the common case.
type uploadOpts struct {
	mediaID  string // optional
	filename string
	content  []byte
}

func (s *testStack) upload(t *testing.T, token string, opts uploadOpts) *http.Response {
	t.Helper()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	if opts.mediaID != "" {
		_ = w.WriteField("media_id", opts.mediaID)
	}
	if opts.filename != "" {
		fw, err := w.CreateFormFile("file", opts.filename)
		if err != nil {
			t.Fatalf("CreateFormFile: %v", err)
		}
		if _, err := fw.Write(opts.content); err != nil {
			t.Fatalf("write file part: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	return s.do(t, http.MethodPost, "/api/v1/media", token, w.FormDataContentType(), &buf)
}

func decodeJSON(t *testing.T, resp *http.Response, dst any) {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()

	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		t.Fatalf("decode response body: %v", err)
	}
}

var jpegBytes = append([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F'}, make([]byte, 32)...)

func TestMediaFlow_UploadGetDownloadDelete(t *testing.T) {
	stack := newTestStack(t)
	token := stack.tokenFor(t, uuid.New())

	resp := stack.upload(t, token, uploadOpts{filename: "hive1.jpg", content: jpegBytes})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload: status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var created mediahttp.Response
	decodeJSON(t, resp, &created)

	resp = stack.get(t, "/api/v1/media/"+created.ID.String(), token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get: status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	resp = stack.get(t, "/api/v1/media/"+created.ID.String()+"/download", token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("download: status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	downloaded, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read download body: %v", err)
	}
	if !bytes.Equal(downloaded, jpegBytes) {
		t.Fatalf("download returned %d bytes, want %d bytes matching the upload", len(downloaded), len(jpegBytes))
	}
	if got := resp.Header.Get("Content-Type"); got != "image/jpeg" {
		t.Errorf("download Content-Type = %q, want image/jpeg", got)
	}

	resp = stack.delete(t, "/api/v1/media/"+created.ID.String(), token)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}

	resp = stack.get(t, "/api/v1/media/"+created.ID.String(), token)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get after delete: status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

func TestMediaFlow_UploadRejectedForUnsupportedType(t *testing.T) {
	stack := newTestStack(t)
	token := stack.tokenFor(t, uuid.New())

	resp := stack.upload(t, token, uploadOpts{filename: "malware.exe", content: []byte("not really an exe")})
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("upload .exe: status = %d, want %d", resp.StatusCode, http.StatusUnsupportedMediaType)
	}
}

func TestMediaFlow_UploadRejectedWhenTooLarge(t *testing.T) {
	stack := newTestStack(t)
	token := stack.tokenFor(t, uuid.New())

	resp := stack.upload(t, token, uploadOpts{filename: "big.jpg", content: make([]byte, testMaxUploadSizeBytes+1)})
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized upload: status = %d, want %d", resp.StatusCode, http.StatusRequestEntityTooLarge)
	}
}

func TestMediaFlow_UploadIdempotentRetry(t *testing.T) {
	stack := newTestStack(t)
	token := stack.tokenFor(t, uuid.New())
	mediaID := uuid.New().String()

	opts := uploadOpts{mediaID: mediaID, filename: "hive1.jpg", content: jpegBytes}

	resp := stack.upload(t, token, opts)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("first upload: status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var first mediahttp.Response
	decodeJSON(t, resp, &first)

	resp = stack.upload(t, token, opts)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("retried upload: status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var second mediahttp.Response
	decodeJSON(t, resp, &second)
	if second.ID != first.ID {
		t.Fatalf("retried upload returned a different media id")
	}
}

// TestMediaFlow_CannotAccessAnotherUsersMedia is the end-to-end proof of
// this module's central requirement, exercised over real HTTP with real
// JWT verification for two different users.
func TestMediaFlow_CannotAccessAnotherUsersMedia(t *testing.T) {
	stack := newTestStack(t)
	owner := uuid.New()
	other := uuid.New()
	ownerToken := stack.tokenFor(t, owner)
	otherToken := stack.tokenFor(t, other)

	resp := stack.upload(t, ownerToken, uploadOpts{filename: "owner.jpg", content: jpegBytes})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload: status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var created mediahttp.Response
	decodeJSON(t, resp, &created)

	if resp := stack.get(t, "/api/v1/media/"+created.ID.String(), otherToken); resp.StatusCode != http.StatusNotFound {
		t.Errorf("get as a different user: status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
	if resp := stack.get(t, "/api/v1/media/"+created.ID.String()+"/download", otherToken); resp.StatusCode != http.StatusNotFound {
		t.Errorf("download as a different user: status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
	if resp := stack.delete(t, "/api/v1/media/"+created.ID.String(), otherToken); resp.StatusCode != http.StatusNotFound {
		t.Errorf("delete as a different user: status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}

	resp = stack.get(t, "/api/v1/media?ids="+created.ID.String(), otherToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list as a different user: status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var list mediahttp.ListResponse
	decodeJSON(t, resp, &list)
	if len(list.Items) != 0 {
		t.Fatalf("other user's list of the owner's media id = %v, want empty", list.Items)
	}

	resp = stack.delete(t, "/api/v1/media?ids="+created.ID.String(), otherToken)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete-by-ids as a different user: status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}

	resp = stack.get(t, "/api/v1/media/"+created.ID.String(), ownerToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("owner get after other user's attempts (including delete-by-ids): status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestMediaFlow_ListByIDs(t *testing.T) {
	stack := newTestStack(t)
	token := stack.tokenFor(t, uuid.New())

	upload := func(filename string) mediahttp.Response {
		resp := stack.upload(t, token, uploadOpts{filename: filename, content: jpegBytes})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("upload %s: status = %d, want %d", filename, resp.StatusCode, http.StatusCreated)
		}
		var created mediahttp.Response
		decodeJSON(t, resp, &created)
		return created
	}

	first := upload("1.jpg")
	second := upload("2.jpg")
	upload("3.jpg") // deliberately not requested below

	unknown := uuid.New()
	resp := stack.get(t, "/api/v1/media?ids="+second.ID.String()+"&ids="+unknown.String()+"&ids="+first.ID.String()+"&ids="+second.ID.String(), token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var list mediahttp.ListResponse
	decodeJSON(t, resp, &list)
	if len(list.Items) != 2 || list.Items[0].ID != second.ID || list.Items[1].ID != first.ID {
		t.Fatalf("list by ids (with an unknown id and a duplicate mixed in) = %+v, want [%s, %s] in that order", list.Items, second.ID, first.ID)
	}
}

func TestMediaFlow_ListByIDs_EmptyIDsReturnsEmptyList(t *testing.T) {
	stack := newTestStack(t)
	token := stack.tokenFor(t, uuid.New())

	resp := stack.get(t, "/api/v1/media", token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list with no ids: status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var list mediahttp.ListResponse
	decodeJSON(t, resp, &list)
	if len(list.Items) != 0 {
		t.Fatalf("list with no ids = %+v, want empty", list.Items)
	}
}

// TestMediaFlow_DeleteByIDs is the end-to-end proof of the cascade
// primitive apiary-service/hive-service call when they delete an
// apiary/hive: every media id it names is hard-deleted, while media it
// doesn't name (even the same user's) survives.
func TestMediaFlow_DeleteByIDs(t *testing.T) {
	stack := newTestStack(t)
	token := stack.tokenFor(t, uuid.New())

	upload := func(filename string) mediahttp.Response {
		resp := stack.upload(t, token, uploadOpts{filename: filename, content: jpegBytes})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("upload %s: status = %d, want %d", filename, resp.StatusCode, http.StatusCreated)
		}
		var created mediahttp.Response
		decodeJSON(t, resp, &created)
		return created
	}

	toDelete1 := upload("a.jpg")
	toDelete2 := upload("b.jpg")
	keep := upload("c.jpg")

	resp := stack.delete(t, "/api/v1/media?ids="+toDelete1.ID.String()+"&ids="+toDelete2.ID.String(), token)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DeleteByIDs: status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}

	resp = stack.get(t, "/api/v1/media/"+toDelete1.ID.String(), token)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get toDelete1 after DeleteByIDs: status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
	resp = stack.get(t, "/api/v1/media/"+toDelete2.ID.String(), token)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get toDelete2 after DeleteByIDs: status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}

	resp = stack.get(t, "/api/v1/media/"+keep.ID.String(), token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get unrelated media after DeleteByIDs: status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	// Calling it again for the same (now-gone) ids is a no-op, not an
	// error.
	resp = stack.delete(t, "/api/v1/media?ids="+toDelete1.ID.String(), token)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DeleteByIDs again on already-gone ids: status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}
}

func TestMediaFlow_WithoutTokenIsUnauthorized(t *testing.T) {
	stack := newTestStack(t)

	resp := stack.get(t, "/api/v1/media?ids="+uuid.New().String(), "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("list without token: status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestMediaFlow_ValidationErrors(t *testing.T) {
	stack := newTestStack(t)
	token := stack.tokenFor(t, uuid.New())

	resp := stack.upload(t, token, uploadOpts{}) // no file part
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("upload with no file: status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}

	resp = stack.get(t, "/api/v1/media/not-a-uuid", token)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("get with malformed media id: status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}

	resp = stack.get(t, "/api/v1/media?ids=not-a-uuid", token)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("list with malformed id: status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}

	resp = stack.delete(t, "/api/v1/media?ids=not-a-uuid", token)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("delete-by-ids with malformed id: status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}
