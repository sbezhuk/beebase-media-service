package media

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"

	"github.com/sbezhuk/beebase-common/httpx"
)

// validatable is implemented by every JSON request DTO in this package.
// Validate returns a map of field name to error code, empty if valid.
type validatable interface {
	Validate() map[string]string
}

// decodeAndValidate decodes the request body into dst and validates it,
// writing an appropriate error response and returning false if either step
// fails.
func decodeAndValidate(w http.ResponseWriter, r *http.Request, dst validatable) bool {
	defer func() { _ = r.Body.Close() }()

	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeInvalidBody, "request body must be valid JSON")
		return false
	}

	if fields := dst.Validate(); len(fields) > 0 {
		httpx.WriteValidationError(w, fields)
		return false
	}

	return true
}

// Field validation error codes. Each is a stable key a client can map to
// a localized message; the field carrying no error is simply absent from
// the response's "fields" map.
const (
	CodeMediaIDInvalid = "media_id_invalid"
	CodeIDsInvalid     = "ids_invalid"
	CodeIDsTooMany     = "ids_too_many"
)

// UploadRequest holds the non-file multipart form fields of POST /media.
// The file itself is handled separately by the handler, since it isn't a
// plain form value.
type UploadRequest struct {
	MediaID string // optional client-generated idempotency key
}

func (r *UploadRequest) Validate() map[string]string {
	fields := map[string]string{}

	if r.MediaID != "" {
		if _, err := uuid.Parse(r.MediaID); err != nil {
			fields["media_id"] = CodeMediaIDInvalid
		}
	}

	return fields
}

// IDsQuery holds the query parameters of GET /media and DELETE /media - a
// list of media ids, the only filter either endpoint accepts. UUID format
// is validated here; the cap on how many distinct ids may be requested in
// one call is enforced by the service (after de-duplication), not here.
type IDsQuery struct {
	IDs []string
}

func (q *IDsQuery) Validate() map[string]string {
	fields := map[string]string{}

	for _, id := range q.IDs {
		if _, err := uuid.Parse(id); err != nil {
			fields["ids"] = CodeIDsInvalid
			break
		}
	}

	return fields
}
