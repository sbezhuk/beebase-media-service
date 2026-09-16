package media

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	domainmedia "github.com/sbezhuk/beebase-media-service/internal/domain/media"
)

type BlobDeletionWorker struct {
	queue domainmedia.BlobDeletionQueue
	blobs domainmedia.BlobStore
	log   *slog.Logger
}

func NewBlobDeletionWorker(queue domainmedia.BlobDeletionQueue, blobs domainmedia.BlobStore, log *slog.Logger) *BlobDeletionWorker {
	return &BlobDeletionWorker{queue: queue, blobs: blobs, log: log}
}

func (w *BlobDeletionWorker) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	for {
		_ = w.RunOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

func (w *BlobDeletionWorker) RunOnce(ctx context.Context) error {
	task, err := w.queue.Claim(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = w.blobs.Delete(ctx, task.MediaID); err != nil {
		return w.queue.Retry(ctx, task.MediaID, err)
	}
	return w.queue.Complete(ctx, task.MediaID)
}
