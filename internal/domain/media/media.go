// Package media holds the Media entity and the port through which the rest
// of the application persists and retrieves it. It has no dependency on
// HTTP, PostgreSQL, or any other infrastructure concern.
package media

import (
	"time"

	"github.com/google/uuid"
)

// Owner types a Media row can attach to. Deliberately generic (owner_type
// + owner_id) rather than a dedicated ApiaryMedia/HiveMedia table per
// entity, so the same infrastructure can support additional entity types
// later without a schema change.
const (
	OwnerTypeApiary = "APIARY"
	OwnerTypeHive   = "HIVE"
)

// StatusAvailable is the only status a Media row currently has. It's kept
// as a real column (rather than folded into deleted_at) purely as a
// forward-compatible extension point, e.g. a future async
// virus-scan/quarantine state.
const StatusAvailable = "AVAILABLE"

// Media is an uploaded file (photo, PDF, XML, or other document) attached
// to exactly one owning entity in another service (an apiary or a hive).
// It is a synchronizable entity (UUID, created_at, updated_at, deleted_at)
// per the project's offline-sync plan, even though full sync isn't
// implemented yet.
type Media struct {
	ID     uuid.UUID
	UserID uuid.UUID // denormalized owner; see Repository doc comment

	OwnerType string // OwnerTypeApiary | OwnerTypeHive
	OwnerID   uuid.UUID

	OriginalFilename string
	ContentType      string // canonical MIME, derived server-side; never the client's raw header
	SizeBytes        int64  // original, uncompressed size
	Status           string // StatusAvailable

	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time
}

// New constructs a Media row with the given id. Unlike most other
// entities in this codebase, id is a caller-supplied parameter rather
// than always generated internally: the application layer needs to honor
// an optional client-generated idempotency key for retried uploads.
// Callers must have already verified that ownerID belongs to userID
// before calling New.
func New(id, userID uuid.UUID, ownerType string, ownerID uuid.UUID, originalFilename, contentType string, sizeBytes int64) *Media {
	now := time.Now().UTC()
	return &Media{
		ID:               id,
		UserID:           userID,
		OwnerType:        ownerType,
		OwnerID:          ownerID,
		OriginalFilename: originalFilename,
		ContentType:      contentType,
		SizeBytes:        sizeBytes,
		Status:           StatusAvailable,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
}
