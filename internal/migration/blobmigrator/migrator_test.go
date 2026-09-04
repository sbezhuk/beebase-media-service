package blobmigrator_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sort"
	"testing"

	"github.com/google/uuid"

	"github.com/sbezhuk/beebase-media-service/internal/domain/media"
	"github.com/sbezhuk/beebase-media-service/internal/migration/blobmigrator"
)

// --- fakes ---

// fakeLegacyStore is an in-memory blobmigrator.LegacyBlobStore: rows are
// present until MarkMigrated removes them, exactly like the real
// media_blobs table.
type fakeLegacyStore struct {
	rows []blobmigrator.LegacyBlob
}

func (f *fakeLegacyStore) Next(_ context.Context, limit int) ([]blobmigrator.LegacyBlob, error) {
	sort.Slice(f.rows, func(i, j int) bool {
		return f.rows[i].MediaID.String() < f.rows[j].MediaID.String()
	})
	if len(f.rows) > limit {
		return append([]blobmigrator.LegacyBlob(nil), f.rows[:limit]...), nil
	}
	return append([]blobmigrator.LegacyBlob(nil), f.rows...), nil
}

func (f *fakeLegacyStore) MarkMigrated(_ context.Context, mediaID uuid.UUID) error {
	for i, r := range f.rows {
		if r.MediaID == mediaID {
			f.rows = append(f.rows[:i], f.rows[i+1:]...)
			return nil
		}
	}
	return nil
}

// fakeBlobStore is an in-memory media.BlobStore, including Put's
// conditional-create semantics (ErrBlobAlreadyExists on a second Put for
// the same id) that the migrator's resume logic depends on.
type fakeBlobStore struct {
	objects map[uuid.UUID][]byte
	// fail, while set for an id, makes every Put for that id fail with
	// the given error instead of succeeding - simulating a failure (e.g.
	// a network outage) that lasts for the rest of a run, until cleared.
	fail map[uuid.UUID]error
}

func newFakeBlobStore() *fakeBlobStore {
	return &fakeBlobStore{objects: map[uuid.UUID][]byte{}, fail: map[uuid.UUID]error{}}
}

func (f *fakeBlobStore) Put(_ context.Context, mediaID uuid.UUID, content []byte, _ string) error {
	if err, ok := f.fail[mediaID]; ok {
		return err
	}
	if _, exists := f.objects[mediaID]; exists {
		return media.ErrBlobAlreadyExists
	}
	f.objects[mediaID] = append([]byte(nil), content...)
	return nil
}

func (f *fakeBlobStore) Get(_ context.Context, mediaID uuid.UUID) ([]byte, error) {
	c, ok := f.objects[mediaID]
	if !ok {
		return nil, media.ErrBlobNotFound
	}
	return c, nil
}

func (f *fakeBlobStore) Delete(_ context.Context, mediaID uuid.UUID) error {
	delete(f.objects, mediaID)
	return nil
}

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func legacyBlob(content string) blobmigrator.LegacyBlob {
	return blobmigrator.LegacyBlob{MediaID: uuid.New(), ContentType: "text/plain", Content: []byte(content)}
}

// --- tests ---

func TestMigrate_UploadsEveryBlobAndMarksItMigrated(t *testing.T) {
	a, b, c := legacyBlob("a"), legacyBlob("b"), legacyBlob("c")
	legacy := &fakeLegacyStore{rows: []blobmigrator.LegacyBlob{a, b, c}}
	blobs := newFakeBlobStore()

	migrated, err := blobmigrator.Migrate(context.Background(), legacy, blobs, 2, silentLogger())
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if migrated != 3 {
		t.Fatalf("migrated = %d, want 3", migrated)
	}
	if len(legacy.rows) != 0 {
		t.Fatalf("legacy rows remaining = %d, want 0 (all should be marked migrated)", len(legacy.rows))
	}
	for _, blob := range []blobmigrator.LegacyBlob{a, b, c} {
		got, err := blobs.Get(context.Background(), blob.MediaID)
		if err != nil {
			t.Fatalf("Get %s after migration: %v", blob.MediaID, err)
		}
		if string(got) != string(blob.Content) {
			t.Errorf("Get %s = %q, want %q", blob.MediaID, got, blob.Content)
		}
	}
}

func TestMigrate_NothingToDoIsNotAnError(t *testing.T) {
	legacy := &fakeLegacyStore{}
	blobs := newFakeBlobStore()

	migrated, err := blobmigrator.Migrate(context.Background(), legacy, blobs, 10, silentLogger())
	if err != nil {
		t.Fatalf("Migrate with nothing to do: %v", err)
	}
	if migrated != 0 {
		t.Fatalf("migrated = %d, want 0", migrated)
	}
}

// TestMigrate_IdempotentReRun proves running Migrate again after a
// successful run does nothing further and doesn't error, since every row
// was already removed from the legacy store the first time.
func TestMigrate_IdempotentReRun(t *testing.T) {
	blob := legacyBlob("once")
	legacy := &fakeLegacyStore{rows: []blobmigrator.LegacyBlob{blob}}
	blobs := newFakeBlobStore()

	first, err := blobmigrator.Migrate(context.Background(), legacy, blobs, 10, silentLogger())
	if err != nil || first != 1 {
		t.Fatalf("first Migrate: migrated=%d err=%v, want 1, nil", first, err)
	}

	second, err := blobmigrator.Migrate(context.Background(), legacy, blobs, 10, silentLogger())
	if err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if second != 0 {
		t.Fatalf("second Migrate migrated = %d, want 0 (already done)", second)
	}
}

// TestMigrate_ResumesAfterAFailureThatOutlastsOneRun simulates a blob
// whose R2 upload keeps failing for the duration of a run (e.g. an
// outage): that run reports the stuck item and stops once it can no
// longer make progress, but everything else still gets migrated. A
// second Migrate call, once the failure condition is cleared, finishes
// the job - proving Migrate is safe to just re-run after a failure,
// without needing any special "resume" mode.
func TestMigrate_ResumesAfterAFailureThatOutlastsOneRun(t *testing.T) {
	ok1, flaky, ok2 := legacyBlob("ok1"), legacyBlob("flaky"), legacyBlob("ok2")
	legacy := &fakeLegacyStore{rows: []blobmigrator.LegacyBlob{ok1, flaky, ok2}}
	blobs := newFakeBlobStore()
	blobs.fail[flaky.MediaID] = errors.New("simulated r2 outage")

	migrated, err := blobmigrator.Migrate(context.Background(), legacy, blobs, 10, silentLogger())
	if err == nil {
		t.Fatalf("first Migrate: got nil error, want one reporting no progress on the stuck item")
	}
	if migrated != 2 {
		t.Fatalf("first Migrate: migrated = %d, want 2 (the two that succeeded)", migrated)
	}
	if len(legacy.rows) != 1 || legacy.rows[0].MediaID != flaky.MediaID {
		t.Fatalf("legacy rows after first Migrate = %+v, want only the flaky blob left", legacy.rows)
	}

	// The outage is over now.
	delete(blobs.fail, flaky.MediaID)

	migrated, err = blobmigrator.Migrate(context.Background(), legacy, blobs, 10, silentLogger())
	if err != nil {
		t.Fatalf("resumed Migrate: %v", err)
	}
	if migrated != 1 {
		t.Fatalf("resumed Migrate: migrated = %d, want 1", migrated)
	}
	if len(legacy.rows) != 0 {
		t.Fatalf("legacy rows after resumed Migrate = %+v, want none left", legacy.rows)
	}
}

// TestMigrate_ResumesAfterCrashBetweenUploadAndMark simulates the
// narrower race: a previous run's blobs.Put for an id already succeeded,
// but the process died before the matching MarkMigrated call, so the
// legacy row is still there on the next run. Migrate must recognize
// blobs.Put's ErrBlobAlreadyExists as "already done" and mark it migrated
// without erroring or re-uploading.
func TestMigrate_ResumesAfterCrashBetweenUploadAndMark(t *testing.T) {
	blob := legacyBlob("uploaded but not yet marked")
	legacy := &fakeLegacyStore{rows: []blobmigrator.LegacyBlob{blob}}
	blobs := newFakeBlobStore()
	// Simulate the prior run's successful upload that never got marked.
	if err := blobs.Put(context.Background(), blob.MediaID, blob.Content, blob.ContentType); err != nil {
		t.Fatalf("seed prior upload: %v", err)
	}

	migrated, err := blobmigrator.Migrate(context.Background(), legacy, blobs, 10, silentLogger())
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if migrated != 1 {
		t.Fatalf("migrated = %d, want 1", migrated)
	}
	if len(legacy.rows) != 0 {
		t.Fatalf("legacy rows remaining = %d, want 0", len(legacy.rows))
	}
}
