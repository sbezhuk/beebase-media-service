package media

import (
	"context"

	"github.com/google/uuid"

	"github.com/sbezhuk/beebase-common/pagination"
)

// Repository is the port through which the application persists and
// retrieves media. Every method that targets a specific media row takes
// the owning userID alongside the media ID, so ownership is enforced by
// the query itself, not by a separate check layered on top.
//
// UserID is denormalized onto the media row rather than looked up via
// OwnerType/OwnerID on every call: apiary-service/hive-service (different
// services, different databases) are the only source of truth for that
// ownership, and are asked exactly once, at upload time. OwnerType/
// OwnerID never change after that, so the denormalized UserID stays
// correct without a cross-service call on every read.
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
	// ListByOwner returns the page of media described by p, attached to
	// ownerID of type ownerType and owned by userID, along with the total
	// number of matching rows (independent of p, for pagination metadata).
	ListByOwner(ctx context.Context, userID uuid.UUID, ownerType string, ownerID uuid.UUID, p pagination.Params) (items []*Media, total int, err error)
	// Delete soft-deletes the media row (sets deleted_at) and removes its
	// stored content, reclaiming storage immediately.
	Delete(ctx context.Context, userID, mediaID uuid.UUID) error
	// DeleteByOwner hard-deletes every media row (and, via the
	// media_blobs FK's ON DELETE CASCADE, its stored content) attached to
	// ownerID of type ownerType and belonging to userID, including rows a
	// prior soft-delete already marked gone. Used only when apiary-service
	// or hive-service cascades a delete; a zero count is a normal outcome
	// (the owner may simply have no media), not an error.
	DeleteByOwner(ctx context.Context, userID uuid.UUID, ownerType string, ownerID uuid.UUID) (int64, error)
}
