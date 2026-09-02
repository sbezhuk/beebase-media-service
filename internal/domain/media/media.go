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

// Media is an uploaded file (photo, PDF, XML, or other document), owned by
// the user who uploaded it. It may optionally be attached to one owning
// entity in another service (an apiary or a hive) - OwnerType/OwnerID are
// nil until Attach links it, and immutable afterward: once attached, a
// media item can't be moved to a different owner. It is a synchronizable
// entity (UUID, created_at, updated_at, deleted_at) per the project's
// offline-sync plan, even though full sync isn't implemented yet.
type Media struct {
	ID     uuid.UUID
	UserID uuid.UUID // denormalized owner; see Repository doc comment

	// OwnerType and OwnerID are both nil, or both set - never one without
	// the other. Nil means "uploaded but not yet attached to anything".
	OwnerType *string // OwnerTypeApiary | OwnerTypeHive
	OwnerID   *uuid.UUID

	OriginalFilename string
	ContentType      string // canonical MIME, derived server-side; never the client's raw header
	SizeBytes        int64  // original, uncompressed size
	Status           string // StatusAvailable

	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time
}

// IsAttached reports whether m is currently linked to an owning apiary or
// hive.
func (m *Media) IsAttached() bool {
	return m.OwnerType != nil
}

// New constructs a Media row with the given id, unattached to any owner.
// Unlike most other entities in this codebase, id is a caller-supplied
// parameter rather than always generated internally: the application
// layer needs to honor an optional client-generated idempotency key for
// retried uploads.
func New(id, userID uuid.UUID, originalFilename, contentType string, sizeBytes int64) *Media {
	now := time.Now().UTC()
	return &Media{
		ID:               id,
		UserID:           userID,
		OriginalFilename: originalFilename,
		ContentType:      contentType,
		SizeBytes:        sizeBytes,
		Status:           StatusAvailable,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
}
