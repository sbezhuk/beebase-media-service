package media

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type BlobDeletionTask struct {
	MediaID uuid.UUID
}

type BlobDeletionQueue interface {
	Claim(ctx context.Context) (*BlobDeletionTask, error)
	Complete(ctx context.Context, mediaID uuid.UUID) error
	Retry(ctx context.Context, mediaID uuid.UUID, reason error) error
}

const BlobDeletionLease = 2 * time.Minute
