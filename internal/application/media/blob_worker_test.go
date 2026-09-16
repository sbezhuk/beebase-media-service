package media

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	domainmedia "github.com/sbezhuk/beebase-media-service/internal/domain/media"
)

type fakeBlobQueue struct {
	task      *domainmedia.BlobDeletionTask
	completed []uuid.UUID
	retried   []uuid.UUID
}

func (q *fakeBlobQueue) Claim(context.Context) (*domainmedia.BlobDeletionTask, error) {
	return q.task, nil
}
func (q *fakeBlobQueue) Complete(_ context.Context, id uuid.UUID) error {
	q.completed = append(q.completed, id)
	q.task = nil
	return nil
}
func (q *fakeBlobQueue) Retry(_ context.Context, id uuid.UUID, _ error) error {
	q.retried = append(q.retried, id)
	return nil
}

type fakeWorkerBlobs struct {
	err     error
	deleted []uuid.UUID
}

func (b *fakeWorkerBlobs) Put(context.Context, uuid.UUID, []byte, string) error { return nil }
func (b *fakeWorkerBlobs) Get(context.Context, uuid.UUID) ([]byte, error)       { return nil, nil }
func (b *fakeWorkerBlobs) Delete(_ context.Context, id uuid.UUID) error {
	b.deleted = append(b.deleted, id)
	return b.err
}

func TestBlobDeletionWorkerRetriesStorageFailures(t *testing.T) {
	id := uuid.New()
	queue := &fakeBlobQueue{task: &domainmedia.BlobDeletionTask{MediaID: id}}
	blobs := &fakeWorkerBlobs{err: errors.New("object storage unavailable")}
	worker := NewBlobDeletionWorker(queue, blobs, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if len(queue.retried) != 1 || queue.retried[0] != id {
		t.Fatalf("expected task retry, got %#v", queue.retried)
	}
	if len(queue.completed) != 0 {
		t.Fatalf("failed deletion was completed: %#v", queue.completed)
	}
}

func TestBlobDeletionWorkerCompletesAfterStorageRecovery(t *testing.T) {
	id := uuid.New()
	queue := &fakeBlobQueue{task: &domainmedia.BlobDeletionTask{MediaID: id}}
	blobs := &fakeWorkerBlobs{}
	worker := NewBlobDeletionWorker(queue, blobs, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if len(queue.completed) != 1 || queue.completed[0] != id {
		t.Fatalf("expected task completion, got %#v", queue.completed)
	}
	if len(blobs.deleted) != 1 || blobs.deleted[0] != id {
		t.Fatalf("expected blob deletion, got %#v", blobs.deleted)
	}
}
