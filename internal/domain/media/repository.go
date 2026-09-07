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
// How file content is physically stored (today: Amazon S3, behind
// the BlobStore port; metadata itself is always PostgreSQL, see
// internal/repository/media and internal/repository/postgres) is
// entirely an implementation detail of the concrete Repository: nothing
// above this interface knows or cares.
type Repository interface {
	// Create persists m's metadata and content together. If m.ID already
	// belongs to an existing row, it returns ErrIDConflict.
	Create(ctx context.Context, m *Media, content []byte) error
	GetByID(ctx context.Context, userID, mediaID uuid.UUID) (*Media, error)
	// GetContent returns the media's metadata alongside its raw file
	// content.
	GetContent(ctx context.Context, userID, mediaID uuid.UUID) (*Media, []byte, error)
	// ListByIDs returns every media row in ids that belongs to userID and
	// isn't deleted, in a single query. ids may contain duplicates or ids
	// that don't exist (or belong to someone else) - both are simply
	// absent from the result, never an error. The result is not ordered
	// to match ids; the caller reorders if it needs to.
	ListByIDs(ctx context.Context, userID uuid.UUID, ids []uuid.UUID) ([]*Media, error)
	// Delete hard-deletes the media row and its stored content.
	Delete(ctx context.Context, userID, mediaID uuid.UUID) error
	// DeleteByIDs hard-deletes every row in ids belonging to userID, and
	// its stored content, in one call. Used by apiary-service/hive-service
	// to cascade a delete across every media id an apiary/hive itself
	// knows it references (its own Images); ids not found (already gone,
	// or never existed) are simply not counted, never an error - a zero
	// count is a normal outcome, not an error.
	DeleteByIDs(ctx context.Context, userID uuid.UUID, ids []uuid.UUID) (int64, error)
	// DeleteAllByUser hard-deletes every media row belonging to userID, and
	// its stored content. Used by auth-service when it deletes an account,
	// as the final sweep for media never referenced by any apiary/hive/
	// inspection (e.g. the profile avatar, or an upload that was never
	// attached to anything) - the apiary/hive/inspection cascades only
	// ever reach the media ids those entities themselves know about.
	DeleteAllByUser(ctx context.Context, userID uuid.UUID) (int64, error)
}
