package postgres

import (
	"context"

	"github.com/google/uuid"

	"github.com/sbezhuk/beebase-media-service/internal/domain/media"
)

type BlobDeletionRepository struct{ db Querier }

func NewBlobDeletionRepository(db Querier) *BlobDeletionRepository {
	return &BlobDeletionRepository{db: db}
}

func (r *BlobDeletionRepository) Claim(ctx context.Context) (*media.BlobDeletionTask, error) {
	const q = `
		WITH candidate AS (
			SELECT media_id FROM media_blob_deletion_tasks
			WHERE (status = 'pending' AND next_attempt_at <= NOW())
			   OR (status = 'processing' AND lease_until < NOW())
			ORDER BY next_attempt_at, media_id LIMIT 1 FOR UPDATE SKIP LOCKED
		)
		UPDATE media_blob_deletion_tasks t
		SET status='processing', lease_until=NOW()+$1, attempt_count=attempt_count+1, updated_at=NOW()
		FROM candidate WHERE t.media_id=candidate.media_id
		RETURNING t.media_id`
	var id uuid.UUID
	if err := r.db.QueryRow(ctx, q, media.BlobDeletionLease).Scan(&id); err != nil {
		return nil, err
	}
	return &media.BlobDeletionTask{MediaID: id}, nil
}

func (r *BlobDeletionRepository) Complete(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.Exec(ctx, `UPDATE media_blob_deletion_tasks SET status='completed', lease_until=NULL, updated_at=NOW() WHERE media_id=$1`, id)
	return err
}

func (r *BlobDeletionRepository) Retry(ctx context.Context, id uuid.UUID, reason error) error {
	_, err := r.db.Exec(ctx, `UPDATE media_blob_deletion_tasks SET status='pending', next_attempt_at=NOW() + LEAST((INTERVAL '1 minute' * POWER(2, LEAST(attempt_count, 8))), INTERVAL '256 minutes'), lease_until=NULL, last_error=$2, updated_at=NOW() WHERE media_id=$1`, id, reason.Error())
	return err
}
