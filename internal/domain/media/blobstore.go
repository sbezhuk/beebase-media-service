package media

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// ErrBlobAlreadyExists is returned by BlobStore.Put when an object already
// exists for the given media ID. Because the object key is deterministic
// (see BlobStore), this is also how the migration off the old
// database-blob storage recognizes an id it already uploaded in a prior,
// interrupted run: Put's own conflict is the resume signal, so no separate
// "already migrated" bookkeeping is needed.
var ErrBlobAlreadyExists = errors.New("blob already exists")

// ErrBlobNotFound is returned by BlobStore.Get when no object exists for
// the given media ID.
var ErrBlobNotFound = errors.New("blob not found")

// BlobStore is the port through which media file content is physically
// stored, keyed deterministically by media ID (Media ID -> object key ->
// bytes) so no extra database column is needed to remember where a given
// file lives. Today's implementation stores objects in Amazon S3 (see
// internal/platform/blobstore); nothing above this interface knows or
// cares.
type BlobStore interface {
	// Put stores content under mediaID's key. It only succeeds if no
	// object already exists for mediaID - ErrBlobAlreadyExists otherwise -
	// so a colliding client-supplied id (see domain/media.Repository's
	// Create) can never silently overwrite someone else's stored bytes.
	Put(ctx context.Context, mediaID uuid.UUID, content []byte, contentType string) error
	// Get returns mediaID's stored content, or ErrBlobNotFound if there is none.
	Get(ctx context.Context, mediaID uuid.UUID) ([]byte, error)
	// Delete removes mediaID's stored content. Deleting an id that has no
	// object is not an error - it's already in the desired end state,
	// which keeps callers that race a retry (or replay a DeleteByIDs)
	// simple.
	Delete(ctx context.Context, mediaID uuid.UUID) error
}
