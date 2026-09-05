//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/sbezhuk/beebase-media-service/internal/domain/media"
	repopostgres "github.com/sbezhuk/beebase-media-service/internal/repository/postgres"
)

// newTestRepo starts a transaction on the shared test pool and returns a
// repository backed by it, rolled back automatically at test end.
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

	m := media.New(uuid.New(), userID, "note.txt", "text/plain", 20)
	if err := repo.Create(context.Background(), m); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.GetByID(context.Background(), userID, m.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.OriginalFilename != "note.txt" {
		t.Fatalf("GetByID returned %+v", got)
	}
}

func TestMediaRepository_GetByID_WrongOwnerReturnsNotFound(t *testing.T) {
	repo := newTestRepo(t)
	owner := uuid.New()
	other := uuid.New()

	m := media.New(uuid.New(), owner, "f.txt", "text/plain", 6)
	if err := repo.Create(context.Background(), m); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := repo.GetByID(context.Background(), other, m.ID); err != media.ErrNotFound {
		t.Fatalf("GetByID by non-owner: got %v, want ErrNotFound", err)
	}
}

func TestMediaRepository_ListByIDs_ReturnsOnlyCallersOwnMatchingIDs(t *testing.T) {
	repo := newTestRepo(t)
	userID := uuid.New()
	otherUserID := uuid.New()

	mine1 := media.New(uuid.New(), userID, "1.jpg", "image/jpeg", 3)
	mine2 := media.New(uuid.New(), userID, "2.jpg", "image/jpeg", 3)
	notMine := media.New(uuid.New(), otherUserID, "3.jpg", "image/jpeg", 3)
	for _, m := range []*media.Media{mine1, mine2, notMine} {
		if err := repo.Create(context.Background(), m); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	items, err := repo.ListByIDs(context.Background(), userID, []uuid.UUID{mine1.ID, mine2.ID, notMine.ID, uuid.New()})
	if err != nil {
		t.Fatalf("ListByIDs: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("ListByIDs len=%d, want 2 (another user's media and an unknown id must be omitted)", len(items))
	}
	for _, it := range items {
		if it.UserID != userID {
			t.Errorf("ListByIDs leaked %+v", it)
		}
	}
}

func TestMediaRepository_ListByIDs_EmptyIDsReturnsEmptySlice(t *testing.T) {
	repo := newTestRepo(t)

	items, err := repo.ListByIDs(context.Background(), uuid.New(), nil)
	if err != nil {
		t.Fatalf("ListByIDs with no ids: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("ListByIDs with no ids = %+v, want empty", items)
	}
}

func TestMediaRepository_Delete_HardDeletesRow(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })

	repo := repopostgres.NewMediaRepository(tx)
	userID := uuid.New()

	m := media.New(uuid.New(), userID, "f.txt", "text/plain", 9)
	if err := repo.Create(ctx, m); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := repo.Delete(ctx, userID, m.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := repo.GetByID(ctx, userID, m.ID); err != media.ErrNotFound {
		t.Fatalf("GetByID after Delete: got %v, want ErrNotFound", err)
	}

	var n int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM media WHERE id = $1", m.ID).Scan(&n); err != nil {
		t.Fatalf("raw count for media: %v", err)
	}
	if n != 0 {
		t.Errorf("media %s still present after Delete; want fully removed", m.ID)
	}

	// The id is fully free again: a new upload can reuse it, unlike a
	// soft-delete which would leave it permanently taken by the (hidden)
	// old row.
	m2 := media.New(m.ID, userID, "f2.txt", "text/plain", 3)
	if err := repo.Create(ctx, m2); err != nil {
		t.Fatalf("Create reusing a hard-deleted id: %v", err)
	}
}

func TestMediaRepository_Delete_WrongOwnerReturnsNotFound(t *testing.T) {
	repo := newTestRepo(t)
	owner := uuid.New()
	other := uuid.New()

	m := media.New(uuid.New(), owner, "f.txt", "text/plain", 12)
	if err := repo.Create(context.Background(), m); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := repo.Delete(context.Background(), other, m.ID); err != media.ErrNotFound {
		t.Fatalf("Delete by non-owner: got %v, want ErrNotFound", err)
	}
	if _, err := repo.GetByID(context.Background(), owner, m.ID); err != nil {
		t.Fatalf("owner's media should survive a failed delete attempt by another user: %v", err)
	}
}

func TestMediaRepository_DeleteByIDs_HardDeletesOnlyTheGivenIDsAndReturnsThem(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })

	repo := repopostgres.NewMediaRepository(tx)
	userID := uuid.New()

	create := func(filename string) *media.Media {
		m := media.New(uuid.New(), userID, filename, "image/jpeg", 3)
		if err := repo.Create(ctx, m); err != nil {
			t.Fatalf("Create: %v", err)
		}
		return m
	}

	toDelete1 := create("f1.jpg")
	toDelete2 := create("f2.jpg")
	keep := create("f3.jpg")

	// An id that's already gone (hard-deleted via the single-item Delete,
	// or never created at all) simply contributes nothing to the result -
	// never an error.
	alreadyGone := create("f4.jpg")
	if err := repo.Delete(ctx, userID, alreadyGone.ID); err != nil {
		t.Fatalf("Delete already-gone: %v", err)
	}

	deleted, err := repo.DeleteByIDs(ctx, userID, []uuid.UUID{toDelete1.ID, toDelete2.ID, alreadyGone.ID})
	if err != nil {
		t.Fatalf("DeleteByIDs: %v", err)
	}
	if len(deleted) != 2 {
		t.Fatalf("DeleteByIDs returned %d ids, want 2: %v", len(deleted), deleted)
	}
	deletedSet := map[uuid.UUID]bool{}
	for _, id := range deleted {
		deletedSet[id] = true
	}
	if !deletedSet[toDelete1.ID] || !deletedSet[toDelete2.ID] {
		t.Fatalf("DeleteByIDs returned %v, want exactly [%s, %s]", deleted, toDelete1.ID, toDelete2.ID)
	}

	for _, id := range []uuid.UUID{toDelete1.ID, toDelete2.ID, alreadyGone.ID} {
		var n int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM media WHERE id = $1", id).Scan(&n); err != nil {
			t.Fatalf("raw count for media: %v", err)
		}
		if n != 0 {
			t.Errorf("media %s still present after DeleteByIDs; want fully removed", id)
		}
	}

	if _, err := repo.GetByID(ctx, userID, keep.ID); err != nil {
		t.Fatalf("unrelated media should survive DeleteByIDs: %v", err)
	}
}

func TestMediaRepository_DeleteByIDs_ScopedToUser(t *testing.T) {
	repo := newTestRepo(t)
	owner := uuid.New()
	other := uuid.New()

	m := media.New(uuid.New(), owner, "f.jpg", "image/jpeg", 3)
	if err := repo.Create(context.Background(), m); err != nil {
		t.Fatalf("Create: %v", err)
	}

	deleted, err := repo.DeleteByIDs(context.Background(), other, []uuid.UUID{m.ID})
	if err != nil {
		t.Fatalf("DeleteByIDs by non-owner: %v", err)
	}
	if len(deleted) != 0 {
		t.Fatalf("DeleteByIDs by non-owner returned %v, want none", deleted)
	}

	if _, err := repo.GetByID(context.Background(), owner, m.ID); err != nil {
		t.Fatalf("owner's media should survive another user's DeleteByIDs: %v", err)
	}
}

func TestMediaRepository_DeleteByIDs_EmptyIDsIsNotAnError(t *testing.T) {
	repo := newTestRepo(t)

	deleted, err := repo.DeleteByIDs(context.Background(), uuid.New(), nil)
	if err != nil {
		t.Fatalf("DeleteByIDs with no ids: %v", err)
	}
	if len(deleted) != 0 {
		t.Fatalf("deleted = %v, want none", deleted)
	}
}

// TestMediaRepository_DeleteAllByUser_ScopedToUser proves the
// account-deletion sweep removes every row belonging to a user - not just
// a caller-supplied id list, unlike DeleteByIDs - and never touches
// another user's media.
func TestMediaRepository_DeleteAllByUser_ScopedToUser(t *testing.T) {
	repo := newTestRepo(t)
	owner := uuid.New()
	other := uuid.New()

	mine1 := media.New(uuid.New(), owner, "1.jpg", "image/jpeg", 3)
	mine2 := media.New(uuid.New(), owner, "2.jpg", "image/jpeg", 3)
	theirs := media.New(uuid.New(), other, "3.jpg", "image/jpeg", 3)
	for _, m := range []*media.Media{mine1, mine2, theirs} {
		if err := repo.Create(context.Background(), m); err != nil {
			t.Fatalf("Create %s: %v", m.OriginalFilename, err)
		}
	}

	deleted, err := repo.DeleteAllByUser(context.Background(), owner)
	if err != nil {
		t.Fatalf("DeleteAllByUser: %v", err)
	}
	if len(deleted) != 2 {
		t.Fatalf("DeleteAllByUser returned %v, want 2 ids", deleted)
	}

	for _, gone := range []*media.Media{mine1, mine2} {
		if _, err := repo.GetByID(context.Background(), owner, gone.ID); err != media.ErrNotFound {
			t.Fatalf("media %s survived DeleteAllByUser: %v", gone.ID, err)
		}
	}
	if _, err := repo.GetByID(context.Background(), other, theirs.ID); err != nil {
		t.Fatalf("another user's media should survive DeleteAllByUser: %v", err)
	}
}

func TestMediaRepository_DeleteAllByUser_NoMediaIsNotAnError(t *testing.T) {
	repo := newTestRepo(t)

	deleted, err := repo.DeleteAllByUser(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("DeleteAllByUser for a user with none: %v", err)
	}
	if len(deleted) != 0 {
		t.Fatalf("deleted = %v, want none", deleted)
	}
}

func TestMediaRepository_Create_IDConflict(t *testing.T) {
	repo := newTestRepo(t)
	id := uuid.New()
	owner := uuid.New()
	other := uuid.New()

	m1 := media.New(id, owner, "f.txt", "text/plain", 3)
	if err := repo.Create(context.Background(), m1); err != nil {
		t.Fatalf("first Create: %v", err)
	}

	m2 := media.New(id, other, "g.txt", "text/plain", 3)
	if err := repo.Create(context.Background(), m2); err != media.ErrIDConflict {
		t.Fatalf("second Create with the same id: got %v, want ErrIDConflict", err)
	}
}
