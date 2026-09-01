//go:build integration

package postgres_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"testing"

	"github.com/google/uuid"

	"github.com/sbezhuk/beebase-common/pagination"
	"github.com/sbezhuk/beebase-media-service/internal/domain/media"
	repopostgres "github.com/sbezhuk/beebase-media-service/internal/repository/postgres"
)

// newTestRepo starts a transaction on the shared test pool and returns a
// repository backed by it, rolled back automatically at test end. Create
// and Delete open their own nested transaction (a PostgreSQL savepoint)
// via pgx.Tx.Begin, so they're exercised for real without leaving any row
// behind once the outer transaction rolls back.
func newTestRepo(t *testing.T) *repopostgres.MediaRepository {
	t.Helper()

	pool := testPool(t)

	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })

	return repopostgres.NewMediaRepository(tx)
}

func TestMediaRepository_CreateAndGetByID(t *testing.T) {
	repo := newTestRepo(t)
	userID := uuid.New()
	ownerID := uuid.New()
	content := []byte("hello media service")

	m := media.New(uuid.New(), userID, media.OwnerTypeApiary, ownerID, "note.txt", "text/plain", int64(len(content)))
	if err := repo.Create(context.Background(), m, content); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.GetByID(context.Background(), userID, m.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.OriginalFilename != "note.txt" || got.OwnerID != ownerID {
		t.Fatalf("GetByID returned %+v", got)
	}
}

func TestMediaRepository_GetByID_WrongOwnerReturnsNotFound(t *testing.T) {
	repo := newTestRepo(t)
	owner := uuid.New()
	other := uuid.New()
	content := []byte("secret")

	m := media.New(uuid.New(), owner, media.OwnerTypeHive, uuid.New(), "f.txt", "text/plain", int64(len(content)))
	if err := repo.Create(context.Background(), m, content); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := repo.GetByID(context.Background(), other, m.ID); err != media.ErrNotFound {
		t.Fatalf("GetByID by non-owner: got %v, want ErrNotFound", err)
	}
	if _, _, err := repo.GetContent(context.Background(), other, m.ID); err != media.ErrNotFound {
		t.Fatalf("GetContent by non-owner: got %v, want ErrNotFound", err)
	}
}

// TestMediaRepository_CompressionRoundTrip_HighlyCompressible proves a
// text file - which compresses well - round-trips byte-for-byte and is
// actually stored compressed.
func TestMediaRepository_CompressionRoundTrip_HighlyCompressible(t *testing.T) {
	repo := newTestRepo(t)
	userID := uuid.New()
	content := bytes.Repeat([]byte("beebase "), 10_000) // highly repetitive, compresses well

	m := media.New(uuid.New(), userID, media.OwnerTypeApiary, uuid.New(), "notes.xml", "application/xml", int64(len(content)))
	if err := repo.Create(context.Background(), m, content); err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, got, err := repo.GetContent(context.Background(), userID, m.ID)
	if err != nil {
		t.Fatalf("GetContent: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("GetContent returned %d bytes, want %d bytes matching the original", len(got), len(content))
	}
}

// TestMediaRepository_CompressionRoundTrip_Incompressible proves random
// (incompressible) bytes still round-trip correctly, stored uncompressed
// since gzip wouldn't shrink them.
func TestMediaRepository_CompressionRoundTrip_Incompressible(t *testing.T) {
	repo := newTestRepo(t)
	userID := uuid.New()
	content := make([]byte, 4096)
	if _, err := rand.Read(content); err != nil {
		t.Fatalf("generate random content: %v", err)
	}

	m := media.New(uuid.New(), userID, media.OwnerTypeHive, uuid.New(), "blob.pdf", "application/pdf", int64(len(content)))
	if err := repo.Create(context.Background(), m, content); err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, got, err := repo.GetContent(context.Background(), userID, m.ID)
	if err != nil {
		t.Fatalf("GetContent: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("GetContent returned content that doesn't match the original random bytes")
	}
}

func TestMediaRepository_ListByOwner_FiltersByOwnerTypeAndOwnerID(t *testing.T) {
	repo := newTestRepo(t)
	userID := uuid.New()
	apiaryID := uuid.New()
	hiveID := uuid.New()

	for _, ownerType := range []string{media.OwnerTypeApiary, media.OwnerTypeApiary, media.OwnerTypeHive} {
		ownerID := apiaryID
		if ownerType == media.OwnerTypeHive {
			ownerID = hiveID
		}
		m := media.New(uuid.New(), userID, ownerType, ownerID, "f.jpg", "image/jpeg", 3)
		if err := repo.Create(context.Background(), m, []byte("abc")); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	items, total, err := repo.ListByOwner(context.Background(), userID, media.OwnerTypeApiary, apiaryID, pagination.Params{Page: 1, Limit: pagination.DefaultLimit})
	if err != nil {
		t.Fatalf("ListByOwner: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("ListByOwner(apiary) total=%d len=%d, want 2 and 2", total, len(items))
	}
	for _, it := range items {
		if it.OwnerType != media.OwnerTypeApiary || it.OwnerID != apiaryID {
			t.Errorf("ListByOwner leaked %+v", it)
		}
	}
}

func TestMediaRepository_ListByOwner_Pagination(t *testing.T) {
	repo := newTestRepo(t)
	userID := uuid.New()
	ownerID := uuid.New()

	for i := 0; i < 5; i++ {
		m := media.New(uuid.New(), userID, media.OwnerTypeApiary, ownerID, "f.jpg", "image/jpeg", 3)
		if err := repo.Create(context.Background(), m, []byte("abc")); err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
	}

	page, total, err := repo.ListByOwner(context.Background(), userID, media.OwnerTypeApiary, ownerID, pagination.Params{Page: 1, Limit: 2})
	if err != nil {
		t.Fatalf("ListByOwner: %v", err)
	}
	if total != 5 || len(page) != 2 {
		t.Fatalf("ListByOwner page 1: total=%d len=%d, want 5 and 2", total, len(page))
	}
}

func TestMediaRepository_Delete_SoftDeletesAndRemovesBlob(t *testing.T) {
	repo := newTestRepo(t)
	userID := uuid.New()
	content := []byte("gone soon")

	m := media.New(uuid.New(), userID, media.OwnerTypeApiary, uuid.New(), "f.txt", "text/plain", int64(len(content)))
	if err := repo.Create(context.Background(), m, content); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := repo.Delete(context.Background(), userID, m.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := repo.GetByID(context.Background(), userID, m.ID); err != media.ErrNotFound {
		t.Fatalf("GetByID after Delete: got %v, want ErrNotFound", err)
	}
	if _, _, err := repo.GetContent(context.Background(), userID, m.ID); err != media.ErrNotFound {
		t.Fatalf("GetContent after Delete: got %v, want ErrNotFound", err)
	}
}

func TestMediaRepository_Delete_WrongOwnerReturnsNotFound(t *testing.T) {
	repo := newTestRepo(t)
	owner := uuid.New()
	other := uuid.New()
	content := []byte("owner's file")

	m := media.New(uuid.New(), owner, media.OwnerTypeApiary, uuid.New(), "f.txt", "text/plain", int64(len(content)))
	if err := repo.Create(context.Background(), m, content); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := repo.Delete(context.Background(), other, m.ID); err != media.ErrNotFound {
		t.Fatalf("Delete by non-owner: got %v, want ErrNotFound", err)
	}
	if _, err := repo.GetByID(context.Background(), owner, m.ID); err != nil {
		t.Fatalf("owner's media should survive a failed delete attempt by another user: %v", err)
	}
}

func TestMediaRepository_DeleteByOwner_HardDeletesOnlyThatOwnersMedia(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })

	repo := repopostgres.NewMediaRepository(tx)
	userID := uuid.New()
	hiveID := uuid.New()
	apiaryID := uuid.New()

	inHive1 := media.New(uuid.New(), userID, media.OwnerTypeHive, hiveID, "f1.jpg", "image/jpeg", 3)
	inHive2 := media.New(uuid.New(), userID, media.OwnerTypeHive, hiveID, "f2.jpg", "image/jpeg", 3)
	forApiary := media.New(uuid.New(), userID, media.OwnerTypeApiary, apiaryID, "f3.jpg", "image/jpeg", 3)
	for _, m := range []*media.Media{inHive1, inHive2, forApiary} {
		if err := repo.Create(ctx, m, []byte("abc")); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}
	// Already soft-deleted (via the single-item Delete) media under hiveID
	// must still be purged: DeleteByOwner has no deleted_at filter.
	alreadyGone := media.New(uuid.New(), userID, media.OwnerTypeHive, hiveID, "f4.jpg", "image/jpeg", 3)
	if err := repo.Create(ctx, alreadyGone, []byte("abc")); err != nil {
		t.Fatalf("Create already-gone: %v", err)
	}
	if err := repo.Delete(ctx, userID, alreadyGone.ID); err != nil {
		t.Fatalf("Delete already-gone: %v", err)
	}

	count, err := repo.DeleteByOwner(ctx, userID, media.OwnerTypeHive, hiveID)
	if err != nil {
		t.Fatalf("DeleteByOwner: %v", err)
	}
	if count != 3 {
		t.Fatalf("DeleteByOwner count = %d, want 3", count)
	}

	for _, id := range []uuid.UUID{inHive1.ID, inHive2.ID, alreadyGone.ID} {
		var n int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM media WHERE id = $1", id).Scan(&n); err != nil {
			t.Fatalf("raw count for media: %v", err)
		}
		if n != 0 {
			t.Errorf("media %s still present after DeleteByOwner; want fully removed", id)
		}
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM media_blobs WHERE media_id = $1", id).Scan(&n); err != nil {
			t.Fatalf("raw count for media_blobs: %v", err)
		}
		if n != 0 {
			t.Errorf("media_blobs %s still present after DeleteByOwner; want cascaded away", id)
		}
	}

	if _, err := repo.GetByID(ctx, userID, forApiary.ID); err != nil {
		t.Fatalf("unrelated apiary media should survive DeleteByOwner: %v", err)
	}
}

func TestMediaRepository_DeleteByOwner_ScopedToUser(t *testing.T) {
	repo := newTestRepo(t)
	owner := uuid.New()
	other := uuid.New()
	apiaryID := uuid.New()

	m := media.New(uuid.New(), owner, media.OwnerTypeApiary, apiaryID, "f.jpg", "image/jpeg", 3)
	if err := repo.Create(context.Background(), m, []byte("abc")); err != nil {
		t.Fatalf("Create: %v", err)
	}

	count, err := repo.DeleteByOwner(context.Background(), other, media.OwnerTypeApiary, apiaryID)
	if err != nil {
		t.Fatalf("DeleteByOwner by non-owner: %v", err)
	}
	if count != 0 {
		t.Fatalf("DeleteByOwner by non-owner count = %d, want 0", count)
	}

	if _, err := repo.GetByID(context.Background(), owner, m.ID); err != nil {
		t.Fatalf("owner's media should survive another user's DeleteByOwner: %v", err)
	}
}

func TestMediaRepository_DeleteByOwner_ZeroMatchesIsNotAnError(t *testing.T) {
	repo := newTestRepo(t)

	count, err := repo.DeleteByOwner(context.Background(), uuid.New(), media.OwnerTypeHive, uuid.New())
	if err != nil {
		t.Fatalf("DeleteByOwner with no matches: %v", err)
	}
	if count != 0 {
		t.Fatalf("count = %d, want 0", count)
	}
}

func TestMediaRepository_Create_IDConflict(t *testing.T) {
	repo := newTestRepo(t)
	id := uuid.New()
	owner := uuid.New()
	other := uuid.New()

	m1 := media.New(id, owner, media.OwnerTypeApiary, uuid.New(), "f.txt", "text/plain", 3)
	if err := repo.Create(context.Background(), m1, []byte("abc")); err != nil {
		t.Fatalf("first Create: %v", err)
	}

	m2 := media.New(id, other, media.OwnerTypeApiary, uuid.New(), "g.txt", "text/plain", 3)
	if err := repo.Create(context.Background(), m2, []byte("xyz")); err != media.ErrIDConflict {
		t.Fatalf("second Create with the same id: got %v, want ErrIDConflict", err)
	}
}
