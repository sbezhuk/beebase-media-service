package media_test

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/sbezhuk/beebase-common/pagination"
	appmedia "github.com/sbezhuk/beebase-media-service/internal/application/media"
	"github.com/sbezhuk/beebase-media-service/internal/domain/media"
)

// --- in-memory fake repository ---

type storedMedia struct {
	m       media.Media
	content []byte
}

type fakeRepo struct {
	mu   sync.Mutex
	byID map[uuid.UUID]*storedMedia
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{byID: map[uuid.UUID]*storedMedia{}}
}

func (f *fakeRepo) Create(_ context.Context, m *media.Media, content []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.byID[m.ID]; exists {
		return media.ErrIDConflict
	}
	cp := *m
	cc := append([]byte(nil), content...)
	f.byID[m.ID] = &storedMedia{m: cp, content: cc}
	return nil
}

func (f *fakeRepo) GetByID(_ context.Context, userID, mediaID uuid.UUID) (*media.Media, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.byID[mediaID]
	if !ok || s.m.UserID != userID || s.m.DeletedAt != nil {
		return nil, media.ErrNotFound
	}
	cp := s.m
	return &cp, nil
}

func (f *fakeRepo) Attach(_ context.Context, userID, mediaID uuid.UUID, ownerType string, ownerID uuid.UUID) (*media.Media, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.byID[mediaID]
	if !ok || s.m.UserID != userID || s.m.DeletedAt != nil {
		return nil, media.ErrNotFound
	}
	if s.m.OwnerType != nil {
		if *s.m.OwnerType == ownerType && s.m.OwnerID != nil && *s.m.OwnerID == ownerID {
			cp := s.m
			return &cp, nil // idempotent replay
		}
		return nil, media.ErrAlreadyAttached
	}
	ot, oid := ownerType, ownerID
	s.m.OwnerType = &ot
	s.m.OwnerID = &oid
	cp := s.m
	return &cp, nil
}

func (f *fakeRepo) GetContent(_ context.Context, userID, mediaID uuid.UUID) (*media.Media, []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.byID[mediaID]
	if !ok || s.m.UserID != userID || s.m.DeletedAt != nil {
		return nil, nil, media.ErrNotFound
	}
	cp := s.m
	cc := append([]byte(nil), s.content...)
	return &cp, cc, nil
}

func (f *fakeRepo) ListByOwner(_ context.Context, userID uuid.UUID, ownerType string, ownerID uuid.UUID, p pagination.Params) ([]*media.Media, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var all []*media.Media
	for _, s := range f.byID {
		if s.m.UserID == userID && s.m.OwnerType != nil && *s.m.OwnerType == ownerType &&
			s.m.OwnerID != nil && *s.m.OwnerID == ownerID && s.m.DeletedAt == nil {
			cp := s.m
			all = append(all, &cp)
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if !all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].CreatedAt.Before(all[j].CreatedAt)
		}
		return all[i].ID.String() < all[j].ID.String()
	})

	total := len(all)
	start := p.Offset()
	if start > total {
		start = total
	}
	end := start + p.Limit
	if end > total {
		end = total
	}

	return all[start:end], total, nil
}

func (f *fakeRepo) Delete(_ context.Context, userID, mediaID uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.byID[mediaID]
	if !ok || s.m.UserID != userID || s.m.DeletedAt != nil {
		return media.ErrNotFound
	}
	now := s.m.UpdatedAt
	s.m.DeletedAt = &now
	s.content = nil
	return nil
}

func (f *fakeRepo) DeleteByOwner(_ context.Context, userID uuid.UUID, ownerType string, ownerID uuid.UUID) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var count int64
	for id, s := range f.byID {
		if s.m.UserID == userID && s.m.OwnerType != nil && *s.m.OwnerType == ownerType &&
			s.m.OwnerID != nil && *s.m.OwnerID == ownerID {
			delete(f.byID, id)
			count++
		}
	}
	return count, nil
}

// --- fake apiary/hive verifiers ---

// fakeApiaryVerifier simulates apiary-service: a set of (token, apiaryID)
// pairs are "owned", everything else is rejected exactly like a 404 from
// the real service would be.
type fakeApiaryVerifier struct {
	owned map[string]uuid.UUID
}

func newFakeApiaryVerifier() *fakeApiaryVerifier {
	return &fakeApiaryVerifier{owned: map[string]uuid.UUID{}}
}

func (f *fakeApiaryVerifier) allow(token string, apiaryID uuid.UUID) {
	f.owned[token] = apiaryID
}

func (f *fakeApiaryVerifier) Verify(_ context.Context, accessToken string, apiaryID uuid.UUID) error {
	if owned, ok := f.owned[accessToken]; ok && owned == apiaryID {
		return nil
	}
	return appmedia.ErrApiaryNotFound
}

// fakeHiveVerifier is the hive equivalent of fakeApiaryVerifier.
type fakeHiveVerifier struct {
	owned map[string]uuid.UUID
}

func newFakeHiveVerifier() *fakeHiveVerifier {
	return &fakeHiveVerifier{owned: map[string]uuid.UUID{}}
}

func (f *fakeHiveVerifier) allow(token string, hiveID uuid.UUID) {
	f.owned[token] = hiveID
}

func (f *fakeHiveVerifier) Verify(_ context.Context, accessToken string, hiveID uuid.UUID) error {
	if owned, ok := f.owned[accessToken]; ok && owned == hiveID {
		return nil
	}
	return appmedia.ErrHiveNotFound
}

// --- test fixtures ---

const maxUploadSizeBytes = 1 << 20 // 1MB, plenty for these tests

var jpegBytes = append([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F'}, make([]byte, 32)...)

var pdfBytes = append([]byte("%PDF-1.4\n"), make([]byte, 32)...)

func newService(repo *fakeRepo, apiaries *fakeApiaryVerifier, hives *fakeHiveVerifier) *appmedia.Service {
	return appmedia.NewService(repo, apiaries, hives, maxUploadSizeBytes)
}

// upload is a small helper for the many tests that just need a fresh,
// unattached media row belonging to userID.
func upload(t *testing.T, svc *appmedia.Service, userID uuid.UUID, filename string, content []byte) *appmedia.UploadResult {
	t.Helper()
	result, err := svc.Upload(context.Background(), appmedia.UploadInput{
		UserID: userID, OriginalFilename: filename, Content: content,
	})
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	return result
}

// uploadAndAttach uploads a fresh media row and immediately attaches it,
// for the tests that need already-attached media as a fixture.
func uploadAndAttach(t *testing.T, svc *appmedia.Service, userID uuid.UUID, token, ownerType string, ownerID uuid.UUID, filename string, content []byte) *media.Media {
	t.Helper()
	result := upload(t, svc, userID, filename, content)
	m, err := svc.Attach(context.Background(), appmedia.AttachInput{
		UserID: userID, AccessToken: token, MediaID: result.Media.ID, OwnerType: ownerType, OwnerID: ownerID,
	})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	return m
}

// --- Upload tests ---

func TestUpload_Success(t *testing.T) {
	svc := newService(newFakeRepo(), newFakeApiaryVerifier(), newFakeHiveVerifier())
	userID := uuid.New()

	result := upload(t, svc, userID, "hive1.jpg", jpegBytes)
	if result.AlreadyExisted {
		t.Errorf("AlreadyExisted = true on a fresh upload")
	}
	if result.Media.IsAttached() {
		t.Errorf("a fresh upload should be unattached, got owner_type=%v owner_id=%v", result.Media.OwnerType, result.Media.OwnerID)
	}
	if result.Media.ContentType != "image/jpeg" {
		t.Errorf("ContentType = %q, want image/jpeg", result.Media.ContentType)
	}
}

func TestUpload_UnsupportedExtension(t *testing.T) {
	svc := newService(newFakeRepo(), newFakeApiaryVerifier(), newFakeHiveVerifier())

	_, err := svc.Upload(context.Background(), appmedia.UploadInput{
		UserID:           uuid.New(),
		OriginalFilename: "malware.exe",
		Content:          []byte("not really an exe"),
	})
	if !errors.Is(err, appmedia.ErrUnsupportedMIME) {
		t.Fatalf("Upload with .exe: got %v, want ErrUnsupportedMIME", err)
	}
}

// TestUpload_ExtensionContentTypeMismatchIsRejected proves the service
// doesn't trust a file's extension alone: content whose magic bytes don't
// match what the extension implies is rejected, exactly as if the client
// had spoofed it.
func TestUpload_ExtensionContentTypeMismatchIsRejected(t *testing.T) {
	svc := newService(newFakeRepo(), newFakeApiaryVerifier(), newFakeHiveVerifier())

	_, err := svc.Upload(context.Background(), appmedia.UploadInput{
		UserID:           uuid.New(),
		OriginalFilename: "totally-a.pdf",
		Content:          jpegBytes, // JPEG magic bytes behind a .pdf filename
	})
	if !errors.Is(err, appmedia.ErrUnsupportedMIME) {
		t.Fatalf("Upload with spoofed extension: got %v, want ErrUnsupportedMIME", err)
	}
}

func TestUpload_ExceedsMaxSize(t *testing.T) {
	svc := newService(newFakeRepo(), newFakeApiaryVerifier(), newFakeHiveVerifier())

	_, err := svc.Upload(context.Background(), appmedia.UploadInput{
		UserID:           uuid.New(),
		OriginalFilename: "big.jpg",
		Content:          make([]byte, maxUploadSizeBytes+1),
	})
	if !errors.Is(err, appmedia.ErrFileTooLarge) {
		t.Fatalf("Upload over the size limit: got %v, want ErrFileTooLarge", err)
	}
}

func TestUpload_IdempotentReplay_SameMediaID(t *testing.T) {
	svc := newService(newFakeRepo(), newFakeApiaryVerifier(), newFakeHiveVerifier())
	userID := uuid.New()
	clientID := uuid.New()

	in := appmedia.UploadInput{
		UserID:           userID,
		ClientMediaID:    &clientID,
		OriginalFilename: "hive1.jpg",
		Content:          jpegBytes,
	}

	first, err := svc.Upload(context.Background(), in)
	if err != nil {
		t.Fatalf("first Upload: %v", err)
	}
	if first.AlreadyExisted {
		t.Fatalf("first Upload: AlreadyExisted = true, want false")
	}

	second, err := svc.Upload(context.Background(), in)
	if err != nil {
		t.Fatalf("retried Upload: %v", err)
	}
	if !second.AlreadyExisted {
		t.Fatalf("retried Upload: AlreadyExisted = false, want true")
	}
	if second.Media.ID != first.Media.ID {
		t.Fatalf("retried Upload returned a different media id")
	}
}

func TestUpload_ClientMediaIDConflict_BelongsToAnotherUser(t *testing.T) {
	svc := newService(newFakeRepo(), newFakeApiaryVerifier(), newFakeHiveVerifier())
	owner := uuid.New()
	attacker := uuid.New()
	clientID := uuid.New()

	_, err := svc.Upload(context.Background(), appmedia.UploadInput{
		UserID: owner, ClientMediaID: &clientID, OriginalFilename: "owner.jpg", Content: jpegBytes,
	})
	if err != nil {
		t.Fatalf("owner Upload: %v", err)
	}

	_, err = svc.Upload(context.Background(), appmedia.UploadInput{
		UserID: attacker, ClientMediaID: &clientID, OriginalFilename: "attacker.jpg", Content: jpegBytes,
	})
	if !errors.Is(err, appmedia.ErrMediaIDConflict) {
		t.Fatalf("Upload reusing another user's media_id: got %v, want ErrMediaIDConflict", err)
	}
}

// --- Attach tests ---

func TestAttach_Success_Apiary(t *testing.T) {
	apiaries := newFakeApiaryVerifier()
	svc := newService(newFakeRepo(), apiaries, newFakeHiveVerifier())
	userID := uuid.New()
	apiaryID := uuid.New()
	token := "user-token"
	apiaries.allow(token, apiaryID)

	result := upload(t, svc, userID, "f.jpg", jpegBytes)

	m, err := svc.Attach(context.Background(), appmedia.AttachInput{
		UserID: userID, AccessToken: token, MediaID: result.Media.ID,
		OwnerType: media.OwnerTypeApiary, OwnerID: apiaryID,
	})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if m.OwnerType == nil || *m.OwnerType != media.OwnerTypeApiary || m.OwnerID == nil || *m.OwnerID != apiaryID {
		t.Errorf("Attach result owner = %v/%v, want apiary/%s", m.OwnerType, m.OwnerID, apiaryID)
	}
}

func TestAttach_Success_Hive(t *testing.T) {
	hives := newFakeHiveVerifier()
	svc := newService(newFakeRepo(), newFakeApiaryVerifier(), hives)
	userID := uuid.New()
	hiveID := uuid.New()
	token := "user-token"
	hives.allow(token, hiveID)

	result := upload(t, svc, userID, "inspection.pdf", pdfBytes)

	m, err := svc.Attach(context.Background(), appmedia.AttachInput{
		UserID: userID, AccessToken: token, MediaID: result.Media.ID,
		OwnerType: media.OwnerTypeHive, OwnerID: hiveID,
	})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if m.OwnerType == nil || *m.OwnerType != media.OwnerTypeHive || m.OwnerID == nil || *m.OwnerID != hiveID {
		t.Errorf("Attach result owner = %v/%v, want hive/%s", m.OwnerType, m.OwnerID, hiveID)
	}
}

func TestAttach_InvalidOwnerType(t *testing.T) {
	svc := newService(newFakeRepo(), newFakeApiaryVerifier(), newFakeHiveVerifier())
	userID := uuid.New()
	result := upload(t, svc, userID, "f.jpg", jpegBytes)

	_, err := svc.Attach(context.Background(), appmedia.AttachInput{
		UserID: userID, MediaID: result.Media.ID, OwnerType: "hive_box", OwnerID: uuid.New(),
	})
	if !errors.Is(err, appmedia.ErrInvalidOwnerType) {
		t.Fatalf("Attach with bad owner_type: got %v, want ErrInvalidOwnerType", err)
	}
}

// TestAttach_ApiaryNotOwnedByCaller is the core cross-service security
// guarantee: a file can't be attached to an apiary the caller doesn't
// own, even if they know its ID.
func TestAttach_ApiaryNotOwnedByCaller(t *testing.T) {
	svc := newService(newFakeRepo(), newFakeApiaryVerifier(), newFakeHiveVerifier())
	userID := uuid.New()
	result := upload(t, svc, userID, "f.jpg", jpegBytes)

	_, err := svc.Attach(context.Background(), appmedia.AttachInput{
		UserID: userID, AccessToken: "attacker-token", MediaID: result.Media.ID,
		OwnerType: media.OwnerTypeApiary, OwnerID: uuid.New(),
	})
	if !errors.Is(err, appmedia.ErrApiaryNotFound) {
		t.Fatalf("Attach to unowned apiary: got %v, want ErrApiaryNotFound", err)
	}
}

func TestAttach_HiveNotOwnedByCaller(t *testing.T) {
	svc := newService(newFakeRepo(), newFakeApiaryVerifier(), newFakeHiveVerifier())
	userID := uuid.New()
	result := upload(t, svc, userID, "f.pdf", pdfBytes)

	_, err := svc.Attach(context.Background(), appmedia.AttachInput{
		UserID: userID, AccessToken: "attacker-token", MediaID: result.Media.ID,
		OwnerType: media.OwnerTypeHive, OwnerID: uuid.New(),
	})
	if !errors.Is(err, appmedia.ErrHiveNotFound) {
		t.Fatalf("Attach to unowned hive: got %v, want ErrHiveNotFound", err)
	}
}

func TestAttach_MediaNotFound(t *testing.T) {
	apiaries := newFakeApiaryVerifier()
	svc := newService(newFakeRepo(), apiaries, newFakeHiveVerifier())
	apiaryID := uuid.New()
	token := "token"
	apiaries.allow(token, apiaryID)

	_, err := svc.Attach(context.Background(), appmedia.AttachInput{
		UserID: uuid.New(), AccessToken: token, MediaID: uuid.New(),
		OwnerType: media.OwnerTypeApiary, OwnerID: apiaryID,
	})
	if !errors.Is(err, media.ErrNotFound) {
		t.Fatalf("Attach with unknown media id: got %v, want ErrNotFound", err)
	}
}

func TestAttach_WrongUser_ReturnsNotFound(t *testing.T) {
	apiaries := newFakeApiaryVerifier()
	svc := newService(newFakeRepo(), apiaries, newFakeHiveVerifier())
	owner := uuid.New()
	other := uuid.New()
	apiaryID := uuid.New()
	token := "token"
	apiaries.allow(token, apiaryID)

	result := upload(t, svc, owner, "f.jpg", jpegBytes)

	_, err := svc.Attach(context.Background(), appmedia.AttachInput{
		UserID: other, AccessToken: token, MediaID: result.Media.ID,
		OwnerType: media.OwnerTypeApiary, OwnerID: apiaryID,
	})
	if !errors.Is(err, media.ErrNotFound) {
		t.Fatalf("Attach by non-owner: got %v, want ErrNotFound", err)
	}
}

// TestAttach_IdempotentReplay_SameOwner proves that retrying an Attach
// call with the same owner (e.g. after a network failure) succeeds
// without error, rather than being rejected as already-attached.
func TestAttach_IdempotentReplay_SameOwner(t *testing.T) {
	apiaries := newFakeApiaryVerifier()
	svc := newService(newFakeRepo(), apiaries, newFakeHiveVerifier())
	userID := uuid.New()
	apiaryID := uuid.New()
	token := "token"
	apiaries.allow(token, apiaryID)

	result := upload(t, svc, userID, "f.jpg", jpegBytes)
	in := appmedia.AttachInput{
		UserID: userID, AccessToken: token, MediaID: result.Media.ID,
		OwnerType: media.OwnerTypeApiary, OwnerID: apiaryID,
	}

	if _, err := svc.Attach(context.Background(), in); err != nil {
		t.Fatalf("first Attach: %v", err)
	}
	if _, err := svc.Attach(context.Background(), in); err != nil {
		t.Fatalf("retried Attach with the same owner: %v", err)
	}
}

// TestAttach_AlreadyAttachedToDifferentOwner proves a media item's owner
// is fixed the first time it's attached: a second Attach naming a
// different owner is rejected, not silently moved.
func TestAttach_AlreadyAttachedToDifferentOwner(t *testing.T) {
	apiaries := newFakeApiaryVerifier()
	svc := newService(newFakeRepo(), apiaries, newFakeHiveVerifier())
	userID := uuid.New()
	firstApiary := uuid.New()
	secondApiary := uuid.New()
	token := "token"
	apiaries.allow(token, firstApiary)

	result := upload(t, svc, userID, "f.jpg", jpegBytes)
	if _, err := svc.Attach(context.Background(), appmedia.AttachInput{
		UserID: userID, AccessToken: token, MediaID: result.Media.ID,
		OwnerType: media.OwnerTypeApiary, OwnerID: firstApiary,
	}); err != nil {
		t.Fatalf("first Attach: %v", err)
	}

	apiaries.allow(token, secondApiary)
	_, err := svc.Attach(context.Background(), appmedia.AttachInput{
		UserID: userID, AccessToken: token, MediaID: result.Media.ID,
		OwnerType: media.OwnerTypeApiary, OwnerID: secondApiary,
	})
	if !errors.Is(err, media.ErrAlreadyAttached) {
		t.Fatalf("re-Attach to a different apiary: got %v, want ErrAlreadyAttached", err)
	}
}

// --- Get/Download tests ---

func TestGet_Success(t *testing.T) {
	svc := newService(newFakeRepo(), newFakeApiaryVerifier(), newFakeHiveVerifier())
	userID := uuid.New()
	created := upload(t, svc, userID, "f.jpg", jpegBytes)

	got, err := svc.Get(context.Background(), userID, created.Media.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != created.Media.ID {
		t.Errorf("Get returned %s, want %s", got.ID, created.Media.ID)
	}
}

func TestGet_NotFound(t *testing.T) {
	svc := newService(newFakeRepo(), newFakeApiaryVerifier(), newFakeHiveVerifier())

	_, err := svc.Get(context.Background(), uuid.New(), uuid.New())
	if !errors.Is(err, media.ErrNotFound) {
		t.Fatalf("Get with unknown id: got %v, want ErrNotFound", err)
	}
}

func TestGet_WrongOwner_ReturnsNotFound(t *testing.T) {
	svc := newService(newFakeRepo(), newFakeApiaryVerifier(), newFakeHiveVerifier())
	owner := uuid.New()
	other := uuid.New()
	created := upload(t, svc, owner, "f.jpg", jpegBytes)

	if _, err := svc.Get(context.Background(), other, created.Media.ID); !errors.Is(err, media.ErrNotFound) {
		t.Fatalf("Get by non-owner: got %v, want ErrNotFound", err)
	}
}

func TestDownload_Success(t *testing.T) {
	svc := newService(newFakeRepo(), newFakeApiaryVerifier(), newFakeHiveVerifier())
	userID := uuid.New()
	created := upload(t, svc, userID, "f.jpg", jpegBytes)

	_, content, err := svc.Download(context.Background(), userID, created.Media.ID)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if string(content) != string(jpegBytes) {
		t.Fatalf("Download returned different content than uploaded")
	}
}

func TestDownload_WrongOwner_ReturnsNotFound(t *testing.T) {
	svc := newService(newFakeRepo(), newFakeApiaryVerifier(), newFakeHiveVerifier())
	owner := uuid.New()
	other := uuid.New()
	created := upload(t, svc, owner, "f.jpg", jpegBytes)

	if _, _, err := svc.Download(context.Background(), other, created.Media.ID); !errors.Is(err, media.ErrNotFound) {
		t.Fatalf("Download by non-owner: got %v, want ErrNotFound", err)
	}
}

// --- List tests ---

func TestList_ReturnsOnlyMatchingOwnerAndCaller(t *testing.T) {
	apiaries := newFakeApiaryVerifier()
	svc := newService(newFakeRepo(), apiaries, newFakeHiveVerifier())
	userA := uuid.New()
	userB := uuid.New()
	apiaryA := uuid.New()
	apiaryB := uuid.New()
	tokenA := "token-a"
	tokenB := "token-b"
	apiaries.allow(tokenA, apiaryA)
	apiaries.allow(tokenB, apiaryB)

	for i := 0; i < 2; i++ {
		uploadAndAttach(t, svc, userA, tokenA, media.OwnerTypeApiary, apiaryA, "a.jpg", jpegBytes)
	}
	uploadAndAttach(t, svc, userB, tokenB, media.OwnerTypeApiary, apiaryB, "b.jpg", jpegBytes)

	items, total, err := svc.List(context.Background(), userA, media.OwnerTypeApiary, apiaryA, pagination.Params{Page: 1, Limit: pagination.DefaultLimit})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("List total=%d len=%d, want 2 and 2", total, len(items))
	}
	for _, m := range items {
		if m.UserID != userA || m.OwnerID == nil || *m.OwnerID != apiaryA {
			t.Errorf("List leaked %+v into userA's apiaryA list", m)
		}
	}
}

func TestList_Pagination(t *testing.T) {
	apiaries := newFakeApiaryVerifier()
	svc := newService(newFakeRepo(), apiaries, newFakeHiveVerifier())
	userID := uuid.New()
	apiaryID := uuid.New()
	token := "token"
	apiaries.allow(token, apiaryID)

	for i := 0; i < 5; i++ {
		uploadAndAttach(t, svc, userID, token, media.OwnerTypeApiary, apiaryID, "f.jpg", jpegBytes)
	}

	page, total, err := svc.List(context.Background(), userID, media.OwnerTypeApiary, apiaryID, pagination.Params{Page: 1, Limit: 2})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 5 || len(page) != 2 {
		t.Fatalf("List page 1: total=%d len=%d, want 5 and 2", total, len(page))
	}
}

// --- Delete tests ---

func TestDelete_Success(t *testing.T) {
	svc := newService(newFakeRepo(), newFakeApiaryVerifier(), newFakeHiveVerifier())
	userID := uuid.New()
	created := upload(t, svc, userID, "f.jpg", jpegBytes)

	if err := svc.Delete(context.Background(), userID, created.Media.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := svc.Get(context.Background(), userID, created.Media.ID); !errors.Is(err, media.ErrNotFound) {
		t.Fatalf("Get after Delete: got %v, want ErrNotFound", err)
	}
}

func TestDelete_WrongOwner_ReturnsNotFoundAndDoesNotDelete(t *testing.T) {
	svc := newService(newFakeRepo(), newFakeApiaryVerifier(), newFakeHiveVerifier())
	owner := uuid.New()
	other := uuid.New()
	created := upload(t, svc, owner, "f.jpg", jpegBytes)

	if err := svc.Delete(context.Background(), other, created.Media.ID); !errors.Is(err, media.ErrNotFound) {
		t.Fatalf("Delete by non-owner: got %v, want ErrNotFound", err)
	}

	if _, err := svc.Get(context.Background(), owner, created.Media.ID); err != nil {
		t.Fatalf("owner's media should survive a failed delete attempt by another user: %v", err)
	}
}

func TestDeleteByOwner_DeletesOnlyThatOwnersMedia(t *testing.T) {
	apiaries := newFakeApiaryVerifier()
	hives := newFakeHiveVerifier()
	svc := newService(newFakeRepo(), apiaries, hives)
	userID := uuid.New()
	hiveID := uuid.New()
	otherHiveID := uuid.New()
	apiaryID := uuid.New()
	token := "token"
	otherHiveToken := "other-hive-token"
	hives.allow(token, hiveID)
	hives.allow(otherHiveToken, otherHiveID)
	apiaries.allow(token, apiaryID)

	for i := 0; i < 2; i++ {
		uploadAndAttach(t, svc, userID, token, media.OwnerTypeHive, hiveID, "f.jpg", jpegBytes)
	}
	// Same owner_id value reused as a different owner_type must survive
	// (owner_type is part of the scoping key, not just owner_id).
	keepDifferentOwnerType := uploadAndAttach(t, svc, userID, token, media.OwnerTypeApiary, apiaryID, "f.jpg", jpegBytes)
	keepOtherOwner := uploadAndAttach(t, svc, userID, otherHiveToken, media.OwnerTypeHive, otherHiveID, "f.jpg", jpegBytes)

	count, err := svc.DeleteByOwner(context.Background(), userID, media.OwnerTypeHive, hiveID)
	if err != nil {
		t.Fatalf("DeleteByOwner: %v", err)
	}
	if count != 2 {
		t.Fatalf("DeleteByOwner count = %d, want 2", count)
	}

	items, total, err := svc.List(context.Background(), userID, media.OwnerTypeHive, hiveID, pagination.Params{Page: 1, Limit: pagination.DefaultLimit})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 0 || len(items) != 0 {
		t.Fatalf("hiveID's media survived DeleteByOwner: total=%d items=%v", total, items)
	}

	for _, keep := range []*media.Media{keepDifferentOwnerType, keepOtherOwner} {
		if _, err := svc.Get(context.Background(), userID, keep.ID); err != nil {
			t.Fatalf("unrelated media %s should survive DeleteByOwner: %v", keep.ID, err)
		}
	}
}

func TestDeleteByOwner_ScopedToUser(t *testing.T) {
	apiaries := newFakeApiaryVerifier()
	svc := newService(newFakeRepo(), apiaries, newFakeHiveVerifier())
	owner := uuid.New()
	other := uuid.New()
	apiaryID := uuid.New()
	token := "owner-token"
	apiaries.allow(token, apiaryID)

	created := uploadAndAttach(t, svc, owner, token, media.OwnerTypeApiary, apiaryID, "f.jpg", jpegBytes)

	count, err := svc.DeleteByOwner(context.Background(), other, media.OwnerTypeApiary, apiaryID)
	if err != nil {
		t.Fatalf("DeleteByOwner by non-owner: %v", err)
	}
	if count != 0 {
		t.Fatalf("DeleteByOwner by non-owner count = %d, want 0", count)
	}

	if _, err := svc.Get(context.Background(), owner, created.ID); err != nil {
		t.Fatalf("owner's media should survive another user's DeleteByOwner: %v", err)
	}
}

func TestDeleteByOwner_ZeroMatchesIsNotAnError(t *testing.T) {
	svc := newService(newFakeRepo(), newFakeApiaryVerifier(), newFakeHiveVerifier())

	count, err := svc.DeleteByOwner(context.Background(), uuid.New(), media.OwnerTypeHive, uuid.New())
	if err != nil {
		t.Fatalf("DeleteByOwner with no matches: %v", err)
	}
	if count != 0 {
		t.Fatalf("count = %d, want 0", count)
	}
}
