// Package media composes a metadata store and a media.BlobStore into the
// full domain/media.Repository port, as BEEB-32's Cloudflare R2 migration:
// file content moves to R2, media metadata stays in PostgreSQL exactly as
// before. See Repository's doc comment for the ordering guarantees this
// composition provides.
package media

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/sbezhuk/beebase-media-service/internal/domain/media"
)

// metadataStore is the subset of postgres.MediaRepository Repository
// needs. It's kept as a narrow interface purely so tests can substitute
// an in-memory fake instead of a real database - this is not a
// general-purpose repository abstraction.
type metadataStore interface {
	Create(ctx context.Context, m *media.Media) error
	GetByID(ctx context.Context, userID, mediaID uuid.UUID) (*media.Media, error)
	ListByIDs(ctx context.Context, userID uuid.UUID, ids []uuid.UUID) ([]*media.Media, error)
	Delete(ctx context.Context, userID, mediaID uuid.UUID) error
	DeleteByIDs(ctx context.Context, userID uuid.UUID, ids []uuid.UUID) ([]uuid.UUID, error)
	DeleteAllByUser(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error)
}

// legacyContentReader reads content that's still sitting in the pre-R2
// media_blobs table (postgres.MediaRepository.GetLegacyContent), for
// GetContent's migration-window fallback below.
type legacyContentReader interface {
	GetLegacyContent(ctx context.Context, mediaID uuid.UUID) ([]byte, error)
}

// Repository implements domain/media.Repository by storing metadata via
// metadataStore (PostgreSQL, see internal/repository/postgres) and file
// content via a media.BlobStore (Cloudflare R2 in production, see
// internal/platform/r2). Its job is entirely about keeping those two
// writes/deletes consistent with each other - see each method's own
// comment for the specific ordering and compensating-action rationale.
type Repository struct {
	metadata metadataStore
	legacy   legacyContentReader
	blobs    media.BlobStore
	log      *slog.Logger
}

// New returns a Repository. legacy may be nil, in which case GetContent's
// migration-window fallback is simply skipped (every read goes straight
// to blobs) - useful once the one-time migration is complete and
// media_blobs has been dropped.
func New(metadata metadataStore, legacy legacyContentReader, blobs media.BlobStore, log *slog.Logger) *Repository {
	return &Repository{metadata: metadata, legacy: legacy, blobs: blobs, log: log}
}

// Create uploads content to the blob store first and only persists m's
// metadata once that succeeds - per BEEB-32's storage contract, a media
// row must never exist without its content actually being in R2.
//
// If the metadata write then fails, the blob upload above already
// committed; Create makes a best-effort attempt to delete it so a failed
// upload doesn't leave an unreferenced object behind. That cleanup uses a
// context.WithoutCancel derivative of ctx so it isn't skipped just
// because the request that triggered it was itself cancelled or timed
// out. If the cleanup attempt also fails, the object is simply orphaned
// (logged for manual cleanup) rather than left in an inconsistent
// state: with no metadata row pointing at it, it's never reachable by any
// caller, exactly as if it didn't exist.
func (r *Repository) Create(ctx context.Context, m *media.Media, content []byte) error {
	if err := r.blobs.Put(ctx, m.ID, content, m.ContentType); err != nil {
		if errors.Is(err, media.ErrBlobAlreadyExists) {
			return media.ErrIDConflict
		}
		return fmt.Errorf("media: upload blob: %w", err)
	}

	if err := r.metadata.Create(ctx, m); err != nil {
		if delErr := r.blobs.Delete(context.WithoutCancel(ctx), m.ID); delErr != nil {
			r.log.Error("media: failed to clean up orphaned blob after metadata create failure", "media_id", m.ID, "error", delErr)
		}
		if errors.Is(err, media.ErrIDConflict) {
			return media.ErrIDConflict
		}
		return fmt.Errorf("media: create metadata: %w", err)
	}

	return nil
}

func (r *Repository) GetByID(ctx context.Context, userID, mediaID uuid.UUID) (*media.Media, error) {
	return r.metadata.GetByID(ctx, userID, mediaID)
}

// GetContent returns the media's metadata alongside its content, read
// from the blob store. While the one-time migration off the old
// database-blob storage is still in progress, a media id whose content
// hasn't been moved yet falls back to the legacy reader instead of
// failing - so retrieval keeps working uninterrupted throughout a
// migration that may pause, resume, or take a while on a large existing
// dataset. Once every row has been migrated (media_blobs empty) this
// fallback path is simply never taken.
func (r *Repository) GetContent(ctx context.Context, userID, mediaID uuid.UUID) (*media.Media, []byte, error) {
	m, err := r.metadata.GetByID(ctx, userID, mediaID)
	if err != nil {
		return nil, nil, err
	}

	content, err := r.blobs.Get(ctx, mediaID)
	switch {
	case err == nil:
		return m, content, nil
	case errors.Is(err, media.ErrBlobNotFound) && r.legacy != nil:
		legacyContent, legacyErr := r.legacy.GetLegacyContent(ctx, mediaID)
		if legacyErr != nil {
			if errors.Is(legacyErr, media.ErrBlobNotFound) {
				// Metadata exists but there's no content anywhere. This
				// should be unreachable given Create keeps the two in
				// sync; surfaced as a hard error rather than a 404 so it
				// doesn't look like a client mistake.
				return nil, nil, fmt.Errorf("media: media %s has no stored content in R2 or the legacy table", mediaID)
			}
			return nil, nil, fmt.Errorf("media: get legacy content: %w", legacyErr)
		}
		return m, legacyContent, nil
	default:
		return nil, nil, fmt.Errorf("media: get content: %w", err)
	}
}

func (r *Repository) ListByIDs(ctx context.Context, userID uuid.UUID, ids []uuid.UUID) ([]*media.Media, error) {
	return r.metadata.ListByIDs(ctx, userID, ids)
}

// Delete hard-deletes the metadata row first, then best-effort deletes
// the blob. Metadata going first means the media is immediately "gone"
// from every caller's point of view (a 404 on the next Get) regardless of
// what happens next; if the blob delete itself then fails, the object is
// simply orphaned (unreferenced by any metadata row, so never reachable
// again) rather than a user-facing failure - logged so it can be cleaned
// up later.
func (r *Repository) Delete(ctx context.Context, userID, mediaID uuid.UUID) error {
	if err := r.metadata.Delete(ctx, userID, mediaID); err != nil {
		return err
	}

	if err := r.blobs.Delete(ctx, mediaID); err != nil {
		r.log.Error("media: failed to delete orphaned blob after metadata delete", "media_id", mediaID, "error", err)
	}

	return nil
}

// DeleteByIDs hard-deletes every matching metadata row in one statement,
// then best-effort deletes each one's blob - same ordering and rationale
// as Delete.
func (r *Repository) DeleteByIDs(ctx context.Context, userID uuid.UUID, ids []uuid.UUID) (int64, error) {
	deleted, err := r.metadata.DeleteByIDs(ctx, userID, ids)
	if err != nil {
		return 0, err
	}

	for _, id := range deleted {
		if err := r.blobs.Delete(ctx, id); err != nil {
			r.log.Error("media: failed to delete orphaned blob after metadata delete", "media_id", id, "error", err)
		}
	}

	return int64(len(deleted)), nil
}

// DeleteAllByUser hard-deletes every metadata row belonging to userID in
// one statement, then best-effort deletes each one's blob - same ordering
// and rationale as Delete.
func (r *Repository) DeleteAllByUser(ctx context.Context, userID uuid.UUID) (int64, error) {
	deleted, err := r.metadata.DeleteAllByUser(ctx, userID)
	if err != nil {
		return 0, err
	}

	for _, id := range deleted {
		if err := r.blobs.Delete(ctx, id); err != nil {
			r.log.Error("media: failed to delete orphaned blob after metadata delete", "media_id", id, "error", err)
		}
	}

	return int64(len(deleted)), nil
}
