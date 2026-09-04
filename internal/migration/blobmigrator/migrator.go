// Package blobmigrator moves media content that still lives in the
// pre-R2 media_blobs PostgreSQL table into a media.BlobStore (Cloudflare
// R2 in production), as part of BEEB-32. It knows nothing about HTTP,
// PostgreSQL, or R2 specifically - see internal/repository/postgres for
// the LegacyBlobStore adapter and internal/platform/r2 for the BlobStore
// this migrates into; cmd/migrate-media-blobs wires the two together.
package blobmigrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/sbezhuk/beebase-media-service/internal/domain/media"
)

// LegacyBlob is one not-yet-migrated row from the pre-R2 storage, content
// already decompressed and ready to upload as-is.
type LegacyBlob struct {
	MediaID     uuid.UUID
	ContentType string
	Content     []byte
}

// LegacyBlobStore is the pre-R2 content source Migrate reads from and
// checks off as it goes.
type LegacyBlobStore interface {
	// Next returns up to limit blobs not yet migrated. An empty result
	// means nothing is left to do. Implementations must not return the
	// same blob twice within a run except in response to a MarkMigrated
	// call that hasn't happened yet (or failed) - i.e. Next always
	// reflects the current "still needs migrating" set.
	Next(ctx context.Context, limit int) ([]LegacyBlob, error)
	// MarkMigrated records mediaID as done, so a later Next (in this run
	// or a resumed one) never returns it again.
	MarkMigrated(ctx context.Context, mediaID uuid.UUID) error
}

// Migrate uploads every remaining legacy blob to blobs in batches of
// batchSize, calling legacy.MarkMigrated only once each upload is
// confirmed stored. That ordering is what makes the whole operation
// safely resumable: a crash or failure at any point leaves nothing but a
// smaller "remaining" set behind, and simply calling Migrate again picks
// up exactly where it left off - already-migrated blobs are never
// re-read, and a blob that was uploaded but not yet marked (a crash
// between the two) is recognized via media.ErrBlobAlreadyExists from
// blobs.Put, not re-uploaded.
//
// Per-blob upload failures are logged and left for the next run rather
// than aborting immediately, so one bad blob doesn't block the rest of a
// batch. If an entire batch fails to make any progress, Migrate stops and
// returns an error rather than looping on the same stuck items forever;
// the migrated count so far is still returned and still durable (every
// MarkMigrated call up to that point has already committed).
func Migrate(ctx context.Context, legacy LegacyBlobStore, blobs media.BlobStore, batchSize int, log *slog.Logger) (int, error) {
	migrated := 0

	for {
		batch, err := legacy.Next(ctx, batchSize)
		if err != nil {
			return migrated, fmt.Errorf("blobmigrator: list next batch: %w", err)
		}
		if len(batch) == 0 {
			return migrated, nil
		}

		progressed := false
		for _, b := range batch {
			err := blobs.Put(ctx, b.MediaID, b.Content, b.ContentType)
			if err != nil && !errors.Is(err, media.ErrBlobAlreadyExists) {
				log.Error("blobmigrator: upload failed, will retry on next run", "media_id", b.MediaID, "error", err)
				continue
			}

			if err := legacy.MarkMigrated(ctx, b.MediaID); err != nil {
				return migrated, fmt.Errorf("blobmigrator: mark %s migrated: %w", b.MediaID, err)
			}
			migrated++
			progressed = true
		}

		if !progressed {
			return migrated, fmt.Errorf("blobmigrator: made no progress on a batch of %d remaining item(s); fix the underlying failures (see logs) and rerun", len(batch))
		}
	}
}
