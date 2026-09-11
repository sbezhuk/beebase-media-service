package media_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	domainmedia "github.com/sbezhuk/beebase-media-service/internal/domain/media"
	mediarepo "github.com/sbezhuk/beebase-media-service/internal/repository/media"
)

// --- fakes ---

type storedRow struct {
	m domainmedia.Media
}

// fakeMetadata is an in-memory stand-in for postgres.MediaRepository's
// metadata half.
type fakeMetadata struct {
	byID      map[uuid.UUID]storedRow
	failNext  error // if set, the next Create fails with this error once
	failByIDs error // if set, DeleteByIDs always fails with this error
}

func newFakeMetadata() *fakeMetadata {
	return &fakeMetadata{byID: map[uuid.UUID]storedRow{}}
}

func (f *fakeMetadata) Create(_ context.Context, m *domainmedia.Media) error {
	if f.failNext != nil {
		err := f.failNext
		f.failNext = nil
		return err
	}
	if _, exists := f.byID[m.ID]; exists {
		return domainmedia.ErrIDConflict
	}
	f.byID[m.ID] = storedRow{m: *m}
	return nil
}

func (f *fakeMetadata) GetByID(_ context.Context, userID, mediaID uuid.UUID) (*domainmedia.Media, error) {
	row, ok := f.byID[mediaID]
	if !ok || row.m.UserID != userID {
		return nil, domainmedia.ErrNotFound
	}
	cp := row.m
	return &cp, nil
}

func (f *fakeMetadata) ListByIDs(_ context.Context, userID uuid.UUID, ids []uuid.UUID) ([]*domainmedia.Media, error) {
	idSet := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		idSet[id] = true
	}
	var out []*domainmedia.Media
	for _, row := range f.byID {
		if idSet[row.m.ID] && row.m.UserID == userID {
			cp := row.m
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (f *fakeMetadata) Delete(_ context.Context, userID, mediaID uuid.UUID) error {
	row, ok := f.byID[mediaID]
	if !ok || row.m.UserID != userID {
		return domainmedia.ErrNotFound
	}
	delete(f.byID, mediaID)
	return nil
}

func (f *fakeMetadata) DeleteByIDs(_ context.Context, userID uuid.UUID, ids []uuid.UUID) ([]uuid.UUID, error) {
	if f.failByIDs != nil {
		return nil, f.failByIDs
	}
	var deleted []uuid.UUID
	for _, id := range ids {
		row, ok := f.byID[id]
		if ok && row.m.UserID == userID {
			delete(f.byID, id)
			deleted = append(deleted, id)
		}
	}
	return deleted, nil
}

func (f *fakeMetadata) DeleteAllByUser(_ context.Context, userID uuid.UUID) ([]uuid.UUID, error) {
	if f.failByIDs != nil {
		return nil, f.failByIDs
	}
	var deleted []uuid.UUID
	for id, row := range f.byID {
		if row.m.UserID == userID {
			delete(f.byID, id)
			deleted = append(deleted, id)
		}
	}
	return deleted, nil
}

// fakeLegacy is an in-memory stand-in for the pre-object-storage media_blobs fallback
// reader.
type fakeLegacy struct {
	byID map[uuid.UUID][]byte
}

func (f *fakeLegacy) GetLegacyContent(_ context.Context, mediaID uuid.UUID) ([]byte, error) {
	c, ok := f.byID[mediaID]
	if !ok {
		return nil, domainmedia.ErrBlobNotFound
	}
	return c, nil
}

// fakeBlobStore is an in-memory media.BlobStore with Put's
// conditional-create semantics and injectable failures, for exercising
// Repository's failure-handling paths.
type fakeBlobStore struct {
	objects     map[uuid.UUID][]byte
	failPut     error // if set, every Put fails with this error
	failGet     error // if set, every Get fails with this error
	failDelete  error // if set, every Delete fails with this error
	deleteCalls []uuid.UUID
}

func newFakeBlobStore() *fakeBlobStore {
	return &fakeBlobStore{objects: map[uuid.UUID][]byte{}}
}

func (f *fakeBlobStore) Put(_ context.Context, mediaID uuid.UUID, content []byte, _ string) error {
	if f.failPut != nil {
		return f.failPut
	}
	if _, exists := f.objects[mediaID]; exists {
		return domainmedia.ErrBlobAlreadyExists
	}
	f.objects[mediaID] = append([]byte(nil), content...)
	return nil
}

func (f *fakeBlobStore) Get(_ context.Context, mediaID uuid.UUID) ([]byte, error) {
	if f.failGet != nil {
		return nil, f.failGet
	}
	c, ok := f.objects[mediaID]
	if !ok {
		return nil, domainmedia.ErrBlobNotFound
	}
	return c, nil
}

func (f *fakeBlobStore) Delete(_ context.Context, mediaID uuid.UUID) error {
	f.deleteCalls = append(f.deleteCalls, mediaID)
	if f.failDelete != nil {
		return f.failDelete
	}
	delete(f.objects, mediaID)
	return nil
}

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newMedia(userID uuid.UUID) *domainmedia.Media {
	return domainmedia.New(uuid.New(), userID, "f.jpg", "image/jpeg", 3)
}

// --- Create tests ---

func TestCreate_UploadsBlobThenMetadata(t *testing.T) {
	metadata, blobs := newFakeMetadata(), newFakeBlobStore()
	repo := mediarepo.New(metadata, nil, blobs, silentLogger())
	userID := uuid.New()
	m := newMedia(userID)

	if err := repo.Create(context.Background(), m, []byte("abc")); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, ok := blobs.objects[m.ID]; !ok {
		t.Errorf("blob not stored for %s", m.ID)
	}
	if _, ok := metadata.byID[m.ID]; !ok {
		t.Errorf("metadata not stored for %s", m.ID)
	}
}

// TestCreate_BlobUploadFailure_MetadataNeverPersisted proves the storage
// contract from BEEB-32: if the blob upload fails, no metadata row is ever
// created.
func TestCreate_BlobUploadFailure_MetadataNeverPersisted(t *testing.T) {
	metadata, blobs := newFakeMetadata(), newFakeBlobStore()
	blobs.failPut = errors.New("simulated storage outage")
	repo := mediarepo.New(metadata, nil, blobs, silentLogger())
	userID := uuid.New()
	m := newMedia(userID)

	if err := repo.Create(context.Background(), m, []byte("abc")); err == nil {
		t.Fatalf("Create with a failing blob store: got nil error")
	}

	if _, ok := metadata.byID[m.ID]; ok {
		t.Errorf("metadata was persisted despite the blob upload failing")
	}
}

// TestCreate_MetadataFailure_CleansUpOrphanedBlob proves the other half
// of the contract: if the blob upload succeeds but the metadata write
// then fails (e.g. a database outage), the orphaned blob is cleaned up
// rather than left stranded and unreferenced forever.
func TestCreate_MetadataFailure_CleansUpOrphanedBlob(t *testing.T) {
	metadata, blobs := newFakeMetadata(), newFakeBlobStore()
	metadata.failNext = errors.New("simulated database outage")
	repo := mediarepo.New(metadata, nil, blobs, silentLogger())
	userID := uuid.New()
	m := newMedia(userID)

	if err := repo.Create(context.Background(), m, []byte("abc")); err == nil {
		t.Fatalf("Create with a failing metadata store: got nil error")
	}

	if _, ok := blobs.objects[m.ID]; ok {
		t.Errorf("blob %s still present after Create's metadata write failed; want cleaned up", m.ID)
	}
}

// TestCreate_ClientIDConflict_NeverOverwritesExistingBlob proves a
// colliding client-supplied media id can't silently overwrite another
// upload's bytes: the blob store's conditional Put rejects it outright.
func TestCreate_ClientIDConflict_NeverOverwritesExistingBlob(t *testing.T) {
	metadata, blobs := newFakeMetadata(), newFakeBlobStore()
	repo := mediarepo.New(metadata, nil, blobs, silentLogger())
	owner := uuid.New()
	attacker := uuid.New()

	id := uuid.New()
	original := domainmedia.New(id, owner, "owner.jpg", "image/jpeg", 5)
	if err := repo.Create(context.Background(), original, []byte("owner")); err != nil {
		t.Fatalf("owner Create: %v", err)
	}

	colliding := domainmedia.New(id, attacker, "attacker.jpg", "image/jpeg", 8)
	err := repo.Create(context.Background(), colliding, []byte("attacker"))
	if !errors.Is(err, domainmedia.ErrIDConflict) {
		t.Fatalf("colliding Create: got %v, want ErrIDConflict", err)
	}

	got := blobs.objects[id]
	if string(got) != "owner" {
		t.Fatalf("blob content for %s = %q, want %q (must not be overwritten by the conflicting upload)", id, got, "owner")
	}
}

// --- GetContent tests ---

func TestGetContent_ReadsFromBlobStore(t *testing.T) {
	metadata, blobs := newFakeMetadata(), newFakeBlobStore()
	repo := mediarepo.New(metadata, nil, blobs, silentLogger())
	userID := uuid.New()
	m := newMedia(userID)
	if err := repo.Create(context.Background(), m, []byte("hello")); err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, content, err := repo.GetContent(context.Background(), userID, m.ID)
	if err != nil {
		t.Fatalf("GetContent: %v", err)
	}
	if string(content) != "hello" {
		t.Fatalf("GetContent = %q, want %q", content, "hello")
	}
}

// TestGetContent_FallsBackToLegacyStoreDuringMigration proves retrieval
// keeps working, uninterrupted, for media the background object-storage migration
// hasn't reached yet.
func TestGetContent_FallsBackToLegacyStoreDuringMigration(t *testing.T) {
	metadata, blobs := newFakeMetadata(), newFakeBlobStore()
	userID := uuid.New()
	m := newMedia(userID)
	metadata.byID[m.ID] = storedRow{m: *m} // metadata exists, but no blob in the store yet

	legacy := &fakeLegacy{byID: map[uuid.UUID][]byte{m.ID: []byte("still in postgres")}}
	repo := mediarepo.New(metadata, legacy, blobs, silentLogger())

	_, content, err := repo.GetContent(context.Background(), userID, m.ID)
	if err != nil {
		t.Fatalf("GetContent: %v", err)
	}
	if string(content) != "still in postgres" {
		t.Fatalf("GetContent = %q, want the legacy fallback content", content)
	}
}

func TestGetContent_MissingEverywhereIsAnError(t *testing.T) {
	metadata, blobs := newFakeMetadata(), newFakeBlobStore()
	userID := uuid.New()
	m := newMedia(userID)
	metadata.byID[m.ID] = storedRow{m: *m}

	legacy := &fakeLegacy{byID: map[uuid.UUID][]byte{}}
	repo := mediarepo.New(metadata, legacy, blobs, silentLogger())

	if _, _, err := repo.GetContent(context.Background(), userID, m.ID); err == nil {
		t.Fatalf("GetContent with no content anywhere: got nil error")
	}
}

// --- Delete tests ---

func TestDelete_RemovesMetadataAndBlob(t *testing.T) {
	metadata, blobs := newFakeMetadata(), newFakeBlobStore()
	repo := mediarepo.New(metadata, nil, blobs, silentLogger())
	userID := uuid.New()
	m := newMedia(userID)
	if err := repo.Create(context.Background(), m, []byte("bye")); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := repo.Delete(context.Background(), userID, m.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, ok := metadata.byID[m.ID]; ok {
		t.Errorf("metadata still present after Delete")
	}
	if _, ok := blobs.objects[m.ID]; ok {
		t.Errorf("blob still present after Delete")
	}
}

// TestDelete_BlobDeleteFailure_MetadataStillGone proves the deliberate
// ordering: even if removing the blob fails (e.g. the blob store briefly
// unreachable), the media is already gone from every caller's point of
// view because metadata is deleted first - Delete itself doesn't report
// an error to the caller for a failure it can't do anything about
// synchronously.
func TestDelete_BlobDeleteFailure_MetadataStillGone(t *testing.T) {
	metadata, blobs := newFakeMetadata(), newFakeBlobStore()
	repo := mediarepo.New(metadata, nil, blobs, silentLogger())
	userID := uuid.New()
	m := newMedia(userID)
	if err := repo.Create(context.Background(), m, []byte("bye")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	blobs.failDelete = errors.New("simulated storage outage")

	if err := repo.Delete(context.Background(), userID, m.ID); err != nil {
		t.Fatalf("Delete with a failing blob store: %v, want nil (metadata delete already succeeded)", err)
	}
	if _, ok := metadata.byID[m.ID]; ok {
		t.Errorf("metadata still present after Delete")
	}
	if _, err := repo.GetByID(context.Background(), userID, m.ID); !errors.Is(err, domainmedia.ErrNotFound) {
		t.Errorf("GetByID after Delete: got %v, want ErrNotFound", err)
	}
}

func TestDelete_WrongOwner_NeverTouchesBlob(t *testing.T) {
	metadata, blobs := newFakeMetadata(), newFakeBlobStore()
	repo := mediarepo.New(metadata, nil, blobs, silentLogger())
	owner, other := uuid.New(), uuid.New()
	m := newMedia(owner)
	if err := repo.Create(context.Background(), m, []byte("mine")); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := repo.Delete(context.Background(), other, m.ID); !errors.Is(err, domainmedia.ErrNotFound) {
		t.Fatalf("Delete by non-owner: got %v, want ErrNotFound", err)
	}
	if _, ok := blobs.objects[m.ID]; !ok {
		t.Errorf("owner's blob was deleted by a non-owner's failed Delete attempt")
	}
}

// --- DeleteByIDs tests ---

func TestDeleteByIDs_DeletesOnlyTheBlobsActuallyRemoved(t *testing.T) {
	metadata, blobs := newFakeMetadata(), newFakeBlobStore()
	repo := mediarepo.New(metadata, nil, blobs, silentLogger())
	userID := uuid.New()

	toDelete := newMedia(userID)
	keep := newMedia(userID)
	for _, m := range []*domainmedia.Media{toDelete, keep} {
		if err := repo.Create(context.Background(), m, []byte("x")); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	count, err := repo.DeleteByIDs(context.Background(), userID, []uuid.UUID{toDelete.ID, uuid.New()})
	if err != nil {
		t.Fatalf("DeleteByIDs: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	if _, ok := blobs.objects[toDelete.ID]; ok {
		t.Errorf("blob for deleted media still present")
	}
	if _, ok := blobs.objects[keep.ID]; !ok {
		t.Errorf("unrelated media's blob was deleted")
	}
}

// TestDeleteByIDs_BlobDeleteFailure_MetadataStillGone mirrors
// TestDelete_BlobDeleteFailure_MetadataStillGone for DeleteByIDs: even if
// removing the blob fails, the media row is already gone from the metadata
// store, leaving the database consistent.
func TestDeleteByIDs_BlobDeleteFailure_MetadataStillGone(t *testing.T) {
	metadata, blobs := newFakeMetadata(), newFakeBlobStore()
	repo := mediarepo.New(metadata, nil, blobs, silentLogger())
	userID := uuid.New()
	m := newMedia(userID)
	if err := repo.Create(context.Background(), m, []byte("bye")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	blobs.failDelete = errors.New("simulated storage outage")

	count, err := repo.DeleteByIDs(context.Background(), userID, []uuid.UUID{m.ID})
	if err != nil {
		t.Fatalf("DeleteByIDs with a failing blob store: %v, want nil (metadata delete already succeeded)", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	if _, ok := metadata.byID[m.ID]; ok {
		t.Errorf("metadata still present after DeleteByIDs")
	}
	if _, err := repo.GetByID(context.Background(), userID, m.ID); !errors.Is(err, domainmedia.ErrNotFound) {
		t.Errorf("GetByID after DeleteByIDs: got %v, want ErrNotFound", err)
	}
}

// TestDeleteAllByUser_DeletesEveryBlobScopedToUser mirrors
// TestDeleteByIDs_DeletesOnlyTheBlobsActuallyRemoved for the
// account-deletion sweep: every blob belonging to userID is removed, and
// another user's blob is left untouched.
func TestDeleteAllByUser_DeletesEveryBlobScopedToUser(t *testing.T) {
	metadata, blobs := newFakeMetadata(), newFakeBlobStore()
	repo := mediarepo.New(metadata, nil, blobs, silentLogger())
	owner := uuid.New()
	other := uuid.New()

	mine := newMedia(owner)
	theirs := newMedia(other)
	for _, m := range []*domainmedia.Media{mine, theirs} {
		if err := repo.Create(context.Background(), m, []byte("x")); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	count, err := repo.DeleteAllByUser(context.Background(), owner)
	if err != nil {
		t.Fatalf("DeleteAllByUser: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	if _, ok := blobs.objects[mine.ID]; ok {
		t.Errorf("blob for deleted media still present")
	}
	if _, ok := blobs.objects[theirs.ID]; !ok {
		t.Errorf("another user's blob was deleted")
	}
}
