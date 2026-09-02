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

func (f *fakeRepo) ListByIDs(_ context.Context, userID uuid.UUID, ids []uuid.UUID) ([]*media.Media, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	idSet := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		idSet[id] = true
	}
	var all []*media.Media
	for _, s := range f.byID {
		if idSet[s.m.ID] && s.m.UserID == userID && s.m.DeletedAt == nil {
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

	return all, nil
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

func (f *fakeRepo) DeleteByIDs(_ context.Context, userID uuid.UUID, ids []uuid.UUID) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	idSet := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		idSet[id] = true
	}
	var count int64
	for id, s := range f.byID {
		if idSet[id] && s.m.UserID == userID {
			delete(f.byID, id)
			count++
		}
	}
	return count, nil
}

// --- test fixtures ---

const maxUploadSizeBytes = 1 << 20 // 1MB, plenty for these tests

var jpegBytes = append([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F'}, make([]byte, 32)...)

func newService(repo *fakeRepo) *appmedia.Service {
	return appmedia.NewService(repo, maxUploadSizeBytes)
}

// upload is a small helper for the many tests that just need a fresh
// media row belonging to userID.
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

// --- Upload tests ---

func TestUpload_Success(t *testing.T) {
	svc := newService(newFakeRepo())
	userID := uuid.New()

	result := upload(t, svc, userID, "hive1.jpg", jpegBytes)
	if result.AlreadyExisted {
		t.Errorf("AlreadyExisted = true on a fresh upload")
	}
	if result.Media.ContentType != "image/jpeg" {
		t.Errorf("ContentType = %q, want image/jpeg", result.Media.ContentType)
	}
}

func TestUpload_UnsupportedExtension(t *testing.T) {
	svc := newService(newFakeRepo())

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
	svc := newService(newFakeRepo())

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
	svc := newService(newFakeRepo())

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
	svc := newService(newFakeRepo())
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
	svc := newService(newFakeRepo())
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

// --- Get/Download tests ---

func TestGet_Success(t *testing.T) {
	svc := newService(newFakeRepo())
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
	svc := newService(newFakeRepo())

	_, err := svc.Get(context.Background(), uuid.New(), uuid.New())
	if !errors.Is(err, media.ErrNotFound) {
		t.Fatalf("Get with unknown id: got %v, want ErrNotFound", err)
	}
}

func TestGet_WrongOwner_ReturnsNotFound(t *testing.T) {
	svc := newService(newFakeRepo())
	owner := uuid.New()
	other := uuid.New()
	created := upload(t, svc, owner, "f.jpg", jpegBytes)

	if _, err := svc.Get(context.Background(), other, created.Media.ID); !errors.Is(err, media.ErrNotFound) {
		t.Fatalf("Get by non-owner: got %v, want ErrNotFound", err)
	}
}

func TestDownload_Success(t *testing.T) {
	svc := newService(newFakeRepo())
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
	svc := newService(newFakeRepo())
	owner := uuid.New()
	other := uuid.New()
	created := upload(t, svc, owner, "f.jpg", jpegBytes)

	if _, _, err := svc.Download(context.Background(), other, created.Media.ID); !errors.Is(err, media.ErrNotFound) {
		t.Fatalf("Download by non-owner: got %v, want ErrNotFound", err)
	}
}

// --- List tests ---

func TestList_ReturnsOnlyCallersOwnMedia(t *testing.T) {
	svc := newService(newFakeRepo())
	userA := uuid.New()
	userB := uuid.New()

	a1 := upload(t, svc, userA, "a1.jpg", jpegBytes).Media
	a2 := upload(t, svc, userA, "a2.jpg", jpegBytes).Media
	b1 := upload(t, svc, userB, "b1.jpg", jpegBytes).Media

	items, err := svc.List(context.Background(), userA, []uuid.UUID{a1.ID, a2.ID, b1.ID})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("List len=%d, want 2 (userB's media must not leak into userA's result)", len(items))
	}
	for _, m := range items {
		if m.UserID != userA {
			t.Errorf("List leaked %+v into userA's result", m)
		}
	}
}

func TestList_PreservesRequestOrder(t *testing.T) {
	svc := newService(newFakeRepo())
	userID := uuid.New()

	first := upload(t, svc, userID, "1.jpg", jpegBytes).Media
	second := upload(t, svc, userID, "2.jpg", jpegBytes).Media
	third := upload(t, svc, userID, "3.jpg", jpegBytes).Media

	items, err := svc.List(context.Background(), userID, []uuid.UUID{third.ID, first.ID, second.ID})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 3 || items[0].ID != third.ID || items[1].ID != first.ID || items[2].ID != second.ID {
		t.Fatalf("List did not preserve request order: %+v", items)
	}
}

func TestList_UnknownAndDuplicateIDsAreOmittedOrCollapsed(t *testing.T) {
	svc := newService(newFakeRepo())
	userID := uuid.New()
	m := upload(t, svc, userID, "f.jpg", jpegBytes).Media
	unknown := uuid.New()

	items, err := svc.List(context.Background(), userID, []uuid.UUID{m.ID, unknown, m.ID})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 || items[0].ID != m.ID {
		t.Fatalf("List with an unknown id and a duplicate = %+v, want exactly one item (%s)", items, m.ID)
	}
}

func TestList_EmptyIDsReturnsEmptySlice(t *testing.T) {
	svc := newService(newFakeRepo())

	items, err := svc.List(context.Background(), uuid.New(), nil)
	if err != nil {
		t.Fatalf("List with no ids: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("List with no ids = %+v, want empty", items)
	}
}

func TestList_TooManyIDs(t *testing.T) {
	svc := newService(newFakeRepo())
	ids := make([]uuid.UUID, pagination.MaxLimit+1)
	for i := range ids {
		ids[i] = uuid.New()
	}

	if _, err := svc.List(context.Background(), uuid.New(), ids); !errors.Is(err, appmedia.ErrTooManyIDs) {
		t.Fatalf("List with more than %d distinct ids: got %v, want ErrTooManyIDs", pagination.MaxLimit, err)
	}
}

// --- Delete tests ---

func TestDelete_Success(t *testing.T) {
	svc := newService(newFakeRepo())
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
	svc := newService(newFakeRepo())
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

// --- DeleteByIDs tests ---

func TestDeleteByIDs_DeletesOnlyTheGivenIDs(t *testing.T) {
	svc := newService(newFakeRepo())
	userID := uuid.New()

	toDelete1 := upload(t, svc, userID, "1.jpg", jpegBytes).Media
	toDelete2 := upload(t, svc, userID, "2.jpg", jpegBytes).Media
	keep := upload(t, svc, userID, "3.jpg", jpegBytes).Media

	count, err := svc.DeleteByIDs(context.Background(), userID, []uuid.UUID{toDelete1.ID, toDelete2.ID, toDelete1.ID})
	if err != nil {
		t.Fatalf("DeleteByIDs: %v", err)
	}
	if count != 2 {
		t.Fatalf("DeleteByIDs count = %d, want 2 (duplicate id counted once)", count)
	}

	for _, gone := range []*media.Media{toDelete1, toDelete2} {
		if _, err := svc.Get(context.Background(), userID, gone.ID); !errors.Is(err, media.ErrNotFound) {
			t.Fatalf("media %s survived DeleteByIDs: %v", gone.ID, err)
		}
	}
	if _, err := svc.Get(context.Background(), userID, keep.ID); err != nil {
		t.Fatalf("unrelated media %s should survive DeleteByIDs: %v", keep.ID, err)
	}
}

func TestDeleteByIDs_ScopedToUser(t *testing.T) {
	svc := newService(newFakeRepo())
	owner := uuid.New()
	other := uuid.New()
	created := upload(t, svc, owner, "f.jpg", jpegBytes).Media

	count, err := svc.DeleteByIDs(context.Background(), other, []uuid.UUID{created.ID})
	if err != nil {
		t.Fatalf("DeleteByIDs by non-owner: %v", err)
	}
	if count != 0 {
		t.Fatalf("DeleteByIDs by non-owner count = %d, want 0", count)
	}

	if _, err := svc.Get(context.Background(), owner, created.ID); err != nil {
		t.Fatalf("owner's media should survive another user's DeleteByIDs: %v", err)
	}
}

func TestDeleteByIDs_EmptyIDsIsNotAnError(t *testing.T) {
	svc := newService(newFakeRepo())

	count, err := svc.DeleteByIDs(context.Background(), uuid.New(), nil)
	if err != nil {
		t.Fatalf("DeleteByIDs with no ids: %v", err)
	}
	if count != 0 {
		t.Fatalf("count = %d, want 0", count)
	}
}

func TestDeleteByIDs_UnknownIDsAreNotAnError(t *testing.T) {
	svc := newService(newFakeRepo())

	count, err := svc.DeleteByIDs(context.Background(), uuid.New(), []uuid.UUID{uuid.New(), uuid.New()})
	if err != nil {
		t.Fatalf("DeleteByIDs with unknown ids: %v", err)
	}
	if count != 0 {
		t.Fatalf("count = %d, want 0", count)
	}
}

func TestDeleteByIDs_TooManyIDs(t *testing.T) {
	svc := newService(newFakeRepo())
	ids := make([]uuid.UUID, pagination.MaxLimit+1)
	for i := range ids {
		ids[i] = uuid.New()
	}

	if _, err := svc.DeleteByIDs(context.Background(), uuid.New(), ids); !errors.Is(err, appmedia.ErrTooManyIDs) {
		t.Fatalf("DeleteByIDs with more than %d distinct ids: got %v, want ErrTooManyIDs", pagination.MaxLimit, err)
	}
}
