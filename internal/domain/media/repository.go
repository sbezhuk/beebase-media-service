package media

import (
	"context"

	"github.com/google/uuid"
)

// Repository is the port through which the application persists and
// retrieves media. Every method that targets a specific media row takes
// the owning userID alongside the media ID, so ownership is enforced by
// the query itself, not by a separate check layered on top.
//
// UserID is set directly from the uploader's own verified token at
// upload time - it's the only ownership concept this service has. Which
// apiary/hive (if any) references a media id is entirely
// apiary-service's/hive-service's own concern; this service never learns
// about it.
//
// How file content is physically stored (today: gzip-compressed bytes in
// a PostgreSQL table, see the postgres implementation) is entirely an
// implementation detail of the concrete Repository: nothing above this
// interface knows or cares.
type Repository interface {
	// Create persists m's metadata and content together. If m.ID already
	// belongs to an existing row, it returns ErrIDConflict.
	Create(ctx context.Context, m *Media, content []byte) error
	GetByID(ctx context.Context, userID, mediaID uuid.UUID) (*Media, error)
	// GetContent returns the media's metadata alongside its raw (already
	// decompressed) file content.
	GetContent(ctx context.Context, userID, mediaID uuid.UUID) (*Media, []byte, error)
	// ListByIDs returns every media row in ids that belongs to userID and
	// isn't deleted, in a single query. ids may contain duplicates or ids
	// that don't exist (or belong to someone else) - both are simply
	// absent from the result, never an error. The result is not ordered
	// to match ids; the caller reorders if it needs to.
	ListByIDs(ctx context.Context, userID uuid.UUID, ids []uuid.UUID) ([]*Media, error)
	// Delete soft-deletes the media row (sets deleted_at) and removes its
	// stored content, reclaiming storage immediately.
	Delete(ctx context.Context, userID, mediaID uuid.UUID) error
	// DeleteByIDs hard-deletes every row in ids belonging to userID (and,
	// via the media_blobs FK's ON DELETE CASCADE, its stored content) in
	// a single statement, including rows a prior soft-delete already
	// marked gone. Used by apiary-service/hive-service to cascade a
	// delete across every media id an apiary/hive itself knows it
	// references; ids not found (already gone, or never existed) are
	// simply not counted, never an error - a zero count is a normal
	// outcome, not an error.
	DeleteByIDs(ctx context.Context, userID uuid.UUID, ids []uuid.UUID) (int64, error)
}
