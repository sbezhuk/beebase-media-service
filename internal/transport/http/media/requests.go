package media

import (
	"strings"

	"github.com/google/uuid"

	"github.com/sbezhuk/beebase-media-service/internal/domain/media"
)

// Field validation error codes. Each is a stable key a client can map to
// a localized message; the field carrying no error is simply absent from
// the response's "fields" map.
const (
	CodeOwnerTypeRequired = "owner_type_required"
	CodeOwnerTypeInvalid  = "owner_type_invalid"
	CodeOwnerIDRequired   = "owner_id_required"
	CodeOwnerIDInvalid    = "owner_id_invalid"
	CodeMediaIDInvalid    = "media_id_invalid"
)

// UploadRequest holds the non-file multipart form fields of POST /media.
// The file itself is handled separately by the handler, since it isn't a
// plain form value.
type UploadRequest struct {
	OwnerType string
	OwnerID   string
	MediaID   string // optional client-generated idempotency key
}

func (r *UploadRequest) Validate() map[string]string {
	fields := validateOwner(r.OwnerType, r.OwnerID)

	if r.MediaID != "" {
		if _, err := uuid.Parse(r.MediaID); err != nil {
			fields["media_id"] = CodeMediaIDInvalid
		}
	}

	return fields
}

// ListQuery holds the required query parameters of GET /media.
type ListQuery struct {
	OwnerType string
	OwnerID   string
}

func (q *ListQuery) Validate() map[string]string {
	return validateOwner(q.OwnerType, q.OwnerID)
}

func validateOwner(ownerType, ownerID string) map[string]string {
	fields := map[string]string{}

	switch strings.TrimSpace(ownerType) {
	case media.OwnerTypeApiary, media.OwnerTypeHive:
	case "":
		fields["owner_type"] = CodeOwnerTypeRequired
	default:
		fields["owner_type"] = CodeOwnerTypeInvalid
	}

	switch {
	case strings.TrimSpace(ownerID) == "":
		fields["owner_id"] = CodeOwnerIDRequired
	default:
		if _, err := uuid.Parse(ownerID); err != nil {
			fields["owner_id"] = CodeOwnerIDInvalid
		}
	}

	return fields
}
