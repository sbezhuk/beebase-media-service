//go:build integration

package postgres_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/sbezhuk/beebase-media-service/internal/domain/media"
	"github.com/sbezhuk/beebase-media-service/internal/migration/blobmigrator"
	repopostgres "github.com/sbezhuk/beebase-media-service/internal/repository/postgres"
)

// inMemoryBlobStore is a minimal media.BlobStore stand-in for R2, used
// only to prove blobmigrator.Migrate and the real PostgreSQL
// LegacyBlobStore adapter work correctly together, without needing a
// real R2 bucket for this test.
type inMemoryBlobStore struct {
	objects map[uuid.UUID][]byte
}

func newInMemoryBlobStore() *inMemoryBlobStore {
	return &inMemoryBlobStore{objects: map[uuid.UUID][]byte{}}
}

func (s *inMemoryBlobStore) Put(_ context.Context, mediaID uuid.UUID, content []byte, _ string) error {
	if _, exists := s.objects[mediaID]; exists {
		return media.ErrBlobAlreadyExists
	}
	s.objects[mediaID] = append([]byte(nil), content...)
	return nil
}

func (s *inMemoryBlobStore) Get(_ context.Context, mediaID uuid.UUID) ([]byte, error) {
	c, ok := s.objects[mediaID]
	if !ok {
		return nil, media.ErrBlobNotFound
	}
	return c, nil
}

func (s *inMemoryBlobStore) Delete(_ context.Context, mediaID uuid.UUID) error {
	delete(s.objects, mediaID)
	return nil
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// seedLegacyMedia creates a media row plus a media_blobs row directly
// (bypassing the now blob-less MediaRepository.Create), the way real
// pre-R2 rows exist in production, so LegacyBlobStore has something to
// migrate.
func seedLegacyMedia(t *testing.T, tx pgx.Tx, userID uuid.UUID, filename, contentType string, content []byte) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	id := uuid.New()

	m := media.New(id, userID, filename, contentType, int64(len(content)))
	repo := repopostgres.NewMediaRepository(tx)
	if err := repo.Create(ctx, m); err != nil {
		t.Fatalf("seed media row: %v", err)
	}

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(content); err != nil {
		t.Fatalf("gzip legacy content: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip legacy content: %v", err)
	}
	compressed := buf.Len() < len(content)
	data := buf.Bytes()
	if !compressed {
		data = content
	}

	if _, err := tx.Exec(ctx, `INSERT INTO media_blobs (media_id, data, compressed) VALUES ($1, $2, $3)`, id, data, compressed); err != nil {
		t.Fatalf("seed media_blobs row: %v", err)
	}

	return id
}

func TestLegacyBlobStore_NextReturnsDecompressedBlobsWithContentType(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })

	userID := uuid.New()
	content := bytes.Repeat([]byte("beebase "), 1000) // compresses well
	id := seedLegacyMedia(t, tx, userID, "old.xml", "application/xml", content)

	store := repopostgres.NewLegacyBlobStore(tx)
	blobs, err := store.Next(ctx, 10)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if len(blobs) != 1 {
		t.Fatalf("Next returned %d blobs, want 1", len(blobs))
	}
	got := blobs[0]
	if got.MediaID != id {
		t.Errorf("MediaID = %s, want %s", got.MediaID, id)
	}
	if got.ContentType != "application/xml" {
		t.Errorf("ContentType = %q, want application/xml", got.ContentType)
	}
	if !bytes.Equal(got.Content, content) {
		t.Errorf("Content did not round-trip through (de)compression")
	}
}

func TestLegacyBlobStore_MarkMigrated_RemovesRowSoNextNeverReturnsItAgain(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })

	userID := uuid.New()
	id := seedLegacyMedia(t, tx, userID, "f.jpg", "image/jpeg", []byte("some bytes"))

	store := repopostgres.NewLegacyBlobStore(tx)
	if err := store.MarkMigrated(ctx, id); err != nil {
		t.Fatalf("MarkMigrated: %v", err)
	}

	blobs, err := store.Next(ctx, 10)
	if err != nil {
		t.Fatalf("Next after MarkMigrated: %v", err)
	}
	if len(blobs) != 0 {
		t.Fatalf("Next after MarkMigrated = %+v, want empty", blobs)
	}

	// The media row itself must survive: only its (now-migrated) blob
	// bookkeeping is removed.
	repo := repopostgres.NewMediaRepository(tx)
	if _, err := repo.GetByID(ctx, userID, id); err != nil {
		t.Fatalf("GetByID after MarkMigrated: %v", err)
	}
}

func TestLegacyBlobStore_MarkMigrated_UnknownIDIsNotAnError(t *testing.T) {
	pool := testPool(t)
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })

	store := repopostgres.NewLegacyBlobStore(tx)
	if err := store.MarkMigrated(context.Background(), uuid.New()); err != nil {
		t.Fatalf("MarkMigrated for an unknown id: %v", err)
	}
}

// TestMigrate_EndToEndAgainstPostgres exercises blobmigrator.Migrate
// against real PostgreSQL (LegacyBlobStore) with an in-memory blob
// store standing in for R2 - proving the postgres adapter and the
// migration algorithm work together correctly, without needing a real R2
// bucket for this test.
func TestMigrate_EndToEndAgainstPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })

	userID := uuid.New()
	var ids []uuid.UUID
	for i := 0; i < 5; i++ {
		ids = append(ids, seedLegacyMedia(t, tx, userID, "f.jpg", "image/jpeg", []byte("content")))
	}

	legacy := repopostgres.NewLegacyBlobStore(tx)
	blobs := newInMemoryBlobStore()

	migrated, err := blobmigrator.Migrate(ctx, legacy, blobs, 2, testLogger())
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if migrated != len(ids) {
		t.Fatalf("migrated = %d, want %d", migrated, len(ids))
	}

	for _, id := range ids {
		if _, ok := blobs.objects[id]; !ok {
			t.Errorf("media %s was not uploaded to the blob store", id)
		}
	}

	remaining, err := legacy.Next(ctx, 10)
	if err != nil {
		t.Fatalf("Next after Migrate: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("legacy rows remaining after Migrate = %+v, want none", remaining)
	}
}
