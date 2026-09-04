package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/sbezhuk/beebase-media-service/internal/migration/blobmigrator"
)

// LegacyBlobStore implements blobmigrator.LegacyBlobStore against the
// pre-R2 media_blobs table: the one-time (but safely re-runnable) reader
// the migrate-media-blobs command uses to move existing content into R2.
// See MediaRepository's doc comment for how media_blobs' remaining rows
// double as the migration's own "not yet done" bookkeeping.
type LegacyBlobStore struct {
	db Querier
}

// NewLegacyBlobStore returns a LegacyBlobStore backed by db.
func NewLegacyBlobStore(db Querier) *LegacyBlobStore {
	return &LegacyBlobStore{db: db}
}

// Next returns up to limit not-yet-migrated blobs, decompressed, ordered
// by media id so repeated calls (as rows are removed by MarkMigrated)
// steadily work through the remaining set without needing an offset or
// cursor to track progress.
func (s *LegacyBlobStore) Next(ctx context.Context, limit int) ([]blobmigrator.LegacyBlob, error) {
	const q = `
		SELECT mb.media_id, mb.data, mb.compressed, m.content_type
		FROM media_blobs mb
		JOIN media m ON m.id = mb.media_id
		ORDER BY mb.media_id
		LIMIT $1
	`

	rows, err := s.db.Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: list next legacy blobs: %w", err)
	}
	defer rows.Close()

	var blobs []blobmigrator.LegacyBlob
	for rows.Next() {
		var (
			mediaID     uuid.UUID
			data        []byte
			compressed  bool
			contentType string
		)
		if err := rows.Scan(&mediaID, &data, &compressed, &contentType); err != nil {
			return nil, fmt.Errorf("postgres: scan legacy blob: %w", err)
		}

		content, err := decompress(data, compressed)
		if err != nil {
			return nil, fmt.Errorf("postgres: decompress legacy blob %s: %w", mediaID, err)
		}

		blobs = append(blobs, blobmigrator.LegacyBlob{
			MediaID:     mediaID,
			ContentType: contentType,
			Content:     content,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list next legacy blobs: %w", err)
	}

	return blobs, nil
}

// MarkMigrated deletes mediaID's media_blobs row - the single source of
// truth for "already migrated to R2", so a crashed or interrupted run can
// simply be restarted: Next never sees this id again. Deleting an id with
// no remaining row is not an error - it's already in the desired state,
// which matters if MarkMigrated succeeded on a previous run but the
// process died before moving on.
func (s *LegacyBlobStore) MarkMigrated(ctx context.Context, mediaID uuid.UUID) error {
	const q = `DELETE FROM media_blobs WHERE media_id = $1`

	if _, err := s.db.Exec(ctx, q, mediaID); err != nil {
		return fmt.Errorf("postgres: mark legacy blob %s migrated: %w", mediaID, err)
	}

	return nil
}
