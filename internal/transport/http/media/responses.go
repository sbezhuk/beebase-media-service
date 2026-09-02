package media

import (
	"time"

	"github.com/google/uuid"

	"github.com/sbezhuk/beebase-media-service/internal/domain/media"
)

// Response is the public representation of a media row. It deliberately
// never exposes how or where content is physically stored (no storage
// key, no path) - only GET /api/v1/media/{id}/download can retrieve
// content, and only for a file the caller owns. It has no notion of an
// owning apiary/hive - that relationship lives entirely in
// apiary-service's/hive-service's own responses (their `images` field).
type Response struct {
	ID               uuid.UUID `json:"id"`
	OriginalFilename string    `json:"original_filename"`
	ContentType      string    `json:"content_type"`
	SizeBytes        int64     `json:"size_bytes"`
	Status           string    `json:"status"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func newResponse(m *media.Media) Response {
	return Response{
		ID:               m.ID,
		OriginalFilename: m.OriginalFilename,
		ContentType:      m.ContentType,
		SizeBytes:        m.SizeBytes,
		Status:           m.Status,
		CreatedAt:        m.CreatedAt,
		UpdatedAt:        m.UpdatedAt,
	}
}

func newListResponse(items []*media.Media) []Response {
	out := make([]Response, len(items))
	for i, m := range items {
		out[i] = newResponse(m)
	}
	return out
}

// ListResponse is the body of GET /media. Unlike every other collection
// endpoint in this codebase, it's not paginated: the result is bounded by
// however many ids the caller asks for, not by a page/limit.
type ListResponse struct {
	Items []Response `json:"items"`
}
