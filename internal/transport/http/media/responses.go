package media

import (
	"time"

	"github.com/google/uuid"

	"github.com/sbezhuk/beebase-common/medialink"
	"github.com/sbezhuk/beebase-media-service/internal/domain/media"
)

// Response is the public representation of a media row. It deliberately
// never exposes how or where content is physically stored (no storage
// key, no path) - ImageURL always points at GET
// /api/v1/media/{id}/download, the only route that can retrieve content,
// and only for a file the caller owns. It has no notion of an owning
// apiary/hive - that relationship lives entirely in
// apiary-service's/hive-service's own responses (their `images` field).
type Response struct {
	ID               uuid.UUID `json:"id"`
	OriginalFilename string    `json:"originalFilename"`
	ContentType      string    `json:"contentType"`
	SizeBytes        int64     `json:"sizeBytes"`
	Status           string    `json:"status"`
	ImageURL         string    `json:"imageUrl"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

func newResponse(m *media.Media, publicBaseURL string) Response {
	return Response{
		ID:               m.ID,
		OriginalFilename: m.OriginalFilename,
		ContentType:      m.ContentType,
		SizeBytes:        m.SizeBytes,
		Status:           m.Status,
		ImageURL:         medialink.DownloadURL(publicBaseURL, m.ID),
		CreatedAt:        m.CreatedAt,
		UpdatedAt:        m.UpdatedAt,
	}
}

func newListResponse(items []*media.Media, publicBaseURL string) []Response {
	out := make([]Response, len(items))
	for i, m := range items {
		out[i] = newResponse(m, publicBaseURL)
	}
	return out
}

// ListResponse is the body of GET /media. Unlike every other collection
// endpoint in this codebase, it's not paginated: the result is bounded by
// however many ids the caller asks for, not by a page/limit.
type ListResponse struct {
	Items []Response `json:"items"`
}
