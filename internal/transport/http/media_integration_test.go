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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	appmedia "github.com/sbezhuk/beebase-media-service/internal/application/media"
	"github.com/sbezhuk/beebase-media-service/internal/platform/apiaryclient"
	"github.com/sbezhuk/beebase-media-service/internal/platform/hiveclient"
	repopostgres "github.com/sbezhuk/beebase-media-service/internal/repository/postgres"
	transporthttp "github.com/sbezhuk/beebase-media-service/internal/transport/http"
	mediahttp "github.com/sbezhuk/beebase-media-service/internal/transport/http/media"

	"github.com/sbezhuk/beebase-common/authmw"
	"github.com/sbezhuk/beebase-common/jwks"
	"github.com/sbezhuk/beebase-common/logger"
	"github.com/sbezhuk/beebase-common/pagination"
)

const testKID = "test-kid"
const testMaxUploadSizeBytes = 1 << 20 // 1MB

// fakeOwnerService stands in for apiary-service or hive-service: it owns
// exactly one entity per bearer token registered via allow, and answers
// GET <pathPrefix>/{id} exactly like the real service would - 200 if the
// presented token's owner owns that entity, 404 otherwise - so this test
// exercises media-service's real cross-service HTTP calls without
// needing two full services running.
type fakeOwnerService struct {
	mu         sync.Mutex
	owned      map[string]uuid.UUID // "Bearer <token>" -> the one entity it owns
	pathPrefix string
}

func newFakeOwnerService(pathPrefix string) *fakeOwnerService {
	return &fakeOwnerService{owned: map[string]uuid.UUID{}, pathPrefix: pathPrefix}
}

func (f *fakeOwnerService) allow(token string, id uuid.UUID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.owned["Bearer "+token] = id
}

func (f *fakeOwnerService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	owned, ok := f.owned[r.Header.Get("Authorization")]
	f.mu.Unlock()

	id, err := uuid.Parse(strings.TrimPrefix(r.URL.Path, f.pathPrefix))
	if err != nil || !ok || owned != id {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

type testStack struct {
	server *httptest.Server
	apiary *fakeOwnerService
	hive   *fakeOwnerService
	priv   ed25519.PrivateKey
}

// newTestStack wires a full router against a real PostgreSQL database
// (every write scoped to a transaction rolled back at the end of the
// test), a real JWKS server, and fake apiary-service/hive-service - just
// like TestHiveFlow_* does in hive-service, extended with a second fake
// upstream.
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

	apiary := newFakeOwnerService("/api/v1/apiaries/")
	apiaryServer := httptest.NewServer(apiary)
	t.Cleanup(apiaryServer.Close)

	hive := newFakeOwnerService("/api/v1/hives/")
	hiveServer := httptest.NewServer(hive)
	t.Cleanup(hiveServer.Close)

	mediaRepo := repopostgres.NewMediaRepository(tx)
	apiaryVerifier := apiaryclient.New(apiaryServer.URL)
	hiveVerifier := hiveclient.New(hiveServer.URL)
	mediaService := appmedia.NewService(mediaRepo, apiaryVerifier, hiveVerifier, testMaxUploadSizeBytes)
	log := logger.New("development", "error")
	handler := mediahttp.NewHandler(mediaService, log, testMaxUploadSizeBytes)

	router := transporthttp.NewRouter(log, pool, handler, verifier)

	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	return &testStack{server: srv, apiary: apiary, hive: hive, priv: priv}
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
	ownerType string
	ownerID   string
	mediaID   string // optional
	filename  string
	content   []byte
}

func (s *testStack) upload(t *testing.T, token string, opts uploadOpts) *http.Response {
	t.Helper()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	if opts.ownerType != "" {
		_ = w.WriteField("owner_type", opts.ownerType)
	}
	if opts.ownerID != "" {
		_ = w.WriteField("owner_id", opts.ownerID)
	}
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

func TestMediaFlow_UploadGetDownloadDelete_Apiary(t *testing.T) {
	stack := newTestStack(t)
	userID := uuid.New()
	apiaryID := uuid.New()
	token := stack.tokenFor(t, userID)
	stack.apiary.allow(token, apiaryID)

	resp := stack.upload(t, token, uploadOpts{ownerType: "APIARY", ownerID: apiaryID.String(), filename: "hive1.jpg", content: jpegBytes})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload: status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var created mediahttp.Response
	decodeJSON(t, resp, &created)
	if created.OwnerType != "APIARY" || created.OwnerID != apiaryID {
		t.Fatalf("upload: owner = %s/%s, want apiary/%s", created.OwnerType, created.OwnerID, apiaryID)
	}

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

func TestMediaFlow_UploadGetDownloadDelete_Hive(t *testing.T) {
	stack := newTestStack(t)
	userID := uuid.New()
	hiveID := uuid.New()
	token := stack.tokenFor(t, userID)
	stack.hive.allow(token, hiveID)

	resp := stack.upload(t, token, uploadOpts{ownerType: "HIVE", ownerID: hiveID.String(), filename: "hive1.jpg", content: jpegBytes})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload: status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var created mediahttp.Response
	decodeJSON(t, resp, &created)
	if created.OwnerType != "HIVE" || created.OwnerID != hiveID {
		t.Fatalf("upload: owner = %s/%s, want hive/%s", created.OwnerType, created.OwnerID, hiveID)
	}
}

func TestMediaFlow_UploadRejectedWhenOwnerNotOwned(t *testing.T) {
	stack := newTestStack(t)
	token := stack.tokenFor(t, uuid.New())
	someoneElsesApiary := uuid.New()
	// Deliberately not calling stack.apiary.allow for this token/apiary pair.

	resp := stack.upload(t, token, uploadOpts{ownerType: "APIARY", ownerID: someoneElsesApiary.String(), filename: "hive1.jpg", content: jpegBytes})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("upload under unowned apiary: status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
	var body map[string]any
	decodeJSON(t, resp, &body)
	errBody, _ := body["error"].(map[string]any)
	if errBody["code"] != "apiary_not_found" {
		t.Fatalf("error code = %v, want apiary_not_found", errBody["code"])
	}
}

func TestMediaFlow_UploadRejectedForUnsupportedType(t *testing.T) {
	stack := newTestStack(t)
	userID := uuid.New()
	apiaryID := uuid.New()
	token := stack.tokenFor(t, userID)
	stack.apiary.allow(token, apiaryID)

	resp := stack.upload(t, token, uploadOpts{ownerType: "APIARY", ownerID: apiaryID.String(), filename: "malware.exe", content: []byte("not really an exe")})
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("upload .exe: status = %d, want %d", resp.StatusCode, http.StatusUnsupportedMediaType)
	}
}

func TestMediaFlow_UploadRejectedWhenTooLarge(t *testing.T) {
	stack := newTestStack(t)
	userID := uuid.New()
	apiaryID := uuid.New()
	token := stack.tokenFor(t, userID)
	stack.apiary.allow(token, apiaryID)

	resp := stack.upload(t, token, uploadOpts{ownerType: "APIARY", ownerID: apiaryID.String(), filename: "big.jpg", content: make([]byte, testMaxUploadSizeBytes+1)})
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized upload: status = %d, want %d", resp.StatusCode, http.StatusRequestEntityTooLarge)
	}
}

func TestMediaFlow_UploadIdempotentRetry(t *testing.T) {
	stack := newTestStack(t)
	userID := uuid.New()
	apiaryID := uuid.New()
	token := stack.tokenFor(t, userID)
	stack.apiary.allow(token, apiaryID)
	mediaID := uuid.New().String()

	opts := uploadOpts{ownerType: "APIARY", ownerID: apiaryID.String(), mediaID: mediaID, filename: "hive1.jpg", content: jpegBytes}

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
	apiaryID := uuid.New()
	ownerToken := stack.tokenFor(t, owner)
	otherToken := stack.tokenFor(t, other)
	stack.apiary.allow(ownerToken, apiaryID)

	resp := stack.upload(t, ownerToken, uploadOpts{ownerType: "APIARY", ownerID: apiaryID.String(), filename: "owner.jpg", content: jpegBytes})
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

	resp = stack.get(t, "/api/v1/media?owner_type=APIARY&owner_id="+apiaryID.String(), otherToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list as a different user: status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var list pagination.Response[mediahttp.Response]
	decodeJSON(t, resp, &list)
	if len(list.Items) != 0 {
		t.Fatalf("other user's list = %v, want empty", list.Items)
	}

	resp = stack.get(t, "/api/v1/media/"+created.ID.String(), ownerToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("owner get after other user's attempts: status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestMediaFlow_ListFiltersByOwner(t *testing.T) {
	stack := newTestStack(t)
	userID := uuid.New()
	apiaryID := uuid.New()
	hiveID := uuid.New()
	token := stack.tokenFor(t, userID)
	stack.apiary.allow(token, apiaryID)
	stack.hive.allow(token, hiveID)

	for i := 0; i < 2; i++ {
		resp := stack.upload(t, token, uploadOpts{ownerType: "APIARY", ownerID: apiaryID.String(), filename: "a.jpg", content: jpegBytes})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("upload apiary %d: status = %d, want %d", i, resp.StatusCode, http.StatusCreated)
		}
	}
	resp := stack.upload(t, token, uploadOpts{ownerType: "HIVE", ownerID: hiveID.String(), filename: "h.jpg", content: jpegBytes})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload hive: status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}

	resp = stack.get(t, "/api/v1/media?owner_type=APIARY&owner_id="+apiaryID.String(), token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var list pagination.Response[mediahttp.Response]
	decodeJSON(t, resp, &list)
	if len(list.Items) != 2 || list.Pagination.Total != 2 {
		t.Fatalf("list apiary media: got %d items (total %d), want 2", len(list.Items), list.Pagination.Total)
	}
}

// TestMediaFlow_DeleteByOwner is the end-to-end proof of the cascade
// primitive apiary-service/hive-service call when they delete an
// apiary/hive: every media item attached to that owner is hard-deleted,
// while media under a different owner (even the same user's) survives.
func TestMediaFlow_DeleteByOwner(t *testing.T) {
	stack := newTestStack(t)
	userID := uuid.New()
	hiveA := uuid.New()
	hiveB := uuid.New()
	token := stack.tokenFor(t, userID)
	stack.hive.allow(token, hiveA)

	resp := stack.upload(t, token, uploadOpts{ownerType: "HIVE", ownerID: hiveA.String(), filename: "a.jpg", content: jpegBytes})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload for hiveA: status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var inHiveA mediahttp.Response
	decodeJSON(t, resp, &inHiveA)

	stack.hive.allow(token, hiveB)
	resp = stack.upload(t, token, uploadOpts{ownerType: "HIVE", ownerID: hiveB.String(), filename: "b.jpg", content: jpegBytes})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload for hiveB: status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var inHiveB mediahttp.Response
	decodeJSON(t, resp, &inHiveB)

	resp = stack.delete(t, "/api/v1/media?owner_type=HIVE&owner_id="+hiveA.String(), token)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DeleteByOwner: status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}

	resp = stack.get(t, "/api/v1/media/"+inHiveA.ID.String(), token)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get hiveA media after DeleteByOwner: status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}

	resp = stack.get(t, "/api/v1/media/"+inHiveB.ID.String(), token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get hiveB media after DeleteByOwner on hiveA: status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	// Calling it again for the same (now-empty) owner is a no-op, not an
	// error.
	resp = stack.delete(t, "/api/v1/media?owner_type=HIVE&owner_id="+hiveA.String(), token)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DeleteByOwner again on empty owner: status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}
}

func TestMediaFlow_WithoutTokenIsUnauthorized(t *testing.T) {
	stack := newTestStack(t)

	resp := stack.get(t, "/api/v1/media?owner_type=APIARY&owner_id="+uuid.New().String(), "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("list without token: status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestMediaFlow_ValidationErrors(t *testing.T) {
	stack := newTestStack(t)
	token := stack.tokenFor(t, uuid.New())

	resp := stack.upload(t, token, uploadOpts{ownerType: "not_a_type", ownerID: uuid.New().String(), filename: "f.jpg", content: jpegBytes})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("upload with invalid owner_type: status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}

	resp = stack.upload(t, token, uploadOpts{ownerType: "APIARY", ownerID: "not-a-uuid", filename: "f.jpg", content: jpegBytes})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("upload with malformed owner_id: status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}

	resp = stack.upload(t, token, uploadOpts{ownerType: "APIARY", ownerID: uuid.New().String()}) // no file part
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("upload with no file: status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}

	resp = stack.get(t, "/api/v1/media/not-a-uuid", token)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("get with malformed media id: status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}

	resp = stack.get(t, "/api/v1/media", token) // missing owner_type/owner_id
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("list without owner filter: status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}
