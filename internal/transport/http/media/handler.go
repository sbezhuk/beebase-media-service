// Package media holds the HTTP handlers for media upload/download.
// Handlers stay thin: they decode/validate the request, pull the
// authenticated user's ID from the request, call into the application
// service, and map the result (or error) to a response. No business logic
// or repository access happens here.
package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	httpmw "github.com/sbezhuk/beebase-common/authmw"
	"github.com/sbezhuk/beebase-common/httpx"
	appmedia "github.com/sbezhuk/beebase-media-service/internal/application/media"
	"github.com/sbezhuk/beebase-media-service/internal/domain/media"
)

// Error codes for media failures, returned as the top-level "error.code".
// Each is a stable key a client can map to a localized message.
const (
	CodeMediaNotFound       = "media_not_found"
	CodeInvalidMediaID      = "invalid_media_id"
	CodeUnsupportedFileType = "unsupported_file_type"
	CodeFileTooLarge        = "file_too_large"
	CodeMediaIDConflict     = "media_id_conflict"
	CodeFileRequired        = "file_required"
)

// multipartOverheadBytes accounts for multipart boundaries, headers, and
// the media_id form field beyond the file content itself, when bounding
// the total request body size.
const multipartOverheadBytes = 64 * 1024

// Handler exposes the media HTTP endpoints. Every method requires the
// request to have already passed through httpmw.RequireAuth.
type Handler struct {
	service            *appmedia.Service
	log                *slog.Logger
	maxUploadSizeBytes int64
	publicBaseURL      string
}

func (h *Handler) DeleteUserData(ctx context.Context, userID uuid.UUID) error {
	_, err := h.service.DeleteAllMine(ctx, userID)
	return err
}

// NewHandler returns a Handler backed by service. maxUploadSizeBytes
// bounds the total request body accepted by Upload. publicBaseURL is the
// gateway's externally reachable base URL, used to build each response's
// image_url.
func NewHandler(service *appmedia.Service, log *slog.Logger, maxUploadSizeBytes int64, publicBaseURL string) *Handler {
	return &Handler{service: service, log: log, maxUploadSizeBytes: maxUploadSizeBytes, publicBaseURL: publicBaseURL}
}

// Upload handles POST /media.
func (h *Handler) Upload(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUserID(w, r)
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, h.maxUploadSizeBytes+multipartOverheadBytes)

	if err := r.ParseMultipartForm(h.maxUploadSizeBytes); err != nil {
		h.writeMultipartError(w, err)
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	req := UploadRequest{
		MediaID: r.FormValue("mediaId"),
	}
	fields := req.Validate()

	file, header, fileErr := r.FormFile("file")
	if fileErr != nil {
		fields["file"] = CodeFileRequired
	}
	if len(fields) > 0 {
		if file != nil {
			_ = file.Close()
		}
		httpx.WriteValidationError(w, fields)
		return
	}
	defer func() { _ = file.Close() }()

	content, err := io.ReadAll(file)
	if err != nil {
		h.writeMultipartError(w, err)
		return
	}

	var clientMediaID *uuid.UUID
	if req.MediaID != "" {
		id, _ := uuid.Parse(req.MediaID) // already validated by req.Validate
		clientMediaID = &id
	}

	result, err := h.service.Upload(r.Context(), appmedia.UploadInput{
		UserID:           userID,
		ClientMediaID:    clientMediaID,
		OriginalFilename: header.Filename,
		Content:          content,
	})
	if err != nil {
		h.writeServiceError(w, err)
		return
	}

	status := http.StatusCreated
	if result.AlreadyExisted {
		status = http.StatusOK
	}
	httpx.WriteJSON(w, status, newResponse(result.Media, h.publicBaseURL))
}

// Get handles GET /media/{mediaID}.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUserID(w, r)
	if !ok {
		return
	}

	mediaID, ok := h.pathMediaID(w, r)
	if !ok {
		return
	}

	got, err := h.service.Get(r.Context(), userID, mediaID)
	if err != nil {
		h.writeServiceError(w, err)
		return
	}

	httpx.WriteJSON(w, http.StatusOK, newResponse(got, h.publicBaseURL))
}

// Download handles GET /media/{mediaID}/download.
func (h *Handler) Download(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUserID(w, r)
	if !ok {
		return
	}

	mediaID, ok := h.pathMediaID(w, r)
	if !ok {
		return
	}

	m, content, err := h.service.Download(r.Context(), userID, mediaID)
	if err != nil {
		h.writeServiceError(w, err)
		return
	}

	w.Header().Set("Content-Type", m.ContentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", m.OriginalFilename))
	w.Header().Set("Content-Length", strconv.Itoa(len(content)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

// List handles GET /media?ids=&ids=... It's the only filter this endpoint
// accepts - no owner_type/owner_id, no page/limit.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUserID(w, r)
	if !ok {
		return
	}

	q := IDsQuery{IDs: r.URL.Query()["ids"]}
	if fields := q.Validate(); len(fields) > 0 {
		httpx.WriteValidationError(w, fields)
		return
	}

	ids := make([]uuid.UUID, len(q.IDs))
	for i, raw := range q.IDs {
		ids[i], _ = uuid.Parse(raw) // already validated by q.Validate
	}

	items, err := h.service.List(r.Context(), userID, ids)
	if err != nil {
		h.writeServiceError(w, err)
		return
	}

	httpx.WriteJSON(w, http.StatusOK, ListResponse{Items: newListResponse(items, h.publicBaseURL)})
}

// Delete handles DELETE /media/{mediaID}.
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUserID(w, r)
	if !ok {
		return
	}

	mediaID, ok := h.pathMediaID(w, r)
	if !ok {
		return
	}

	if err := h.service.Delete(r.Context(), userID, mediaID); err != nil {
		h.writeServiceError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// DeleteByIDs handles DELETE /media?ids=&ids=... It hard-deletes every
// media item in ids belonging to the caller, used by
// apiary-service/hive-service to cascade a delete across every media id
// an apiary/hive itself knows it references.
func (h *Handler) DeleteByIDs(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUserID(w, r)
	if !ok {
		return
	}

	q := IDsQuery{IDs: r.URL.Query()["ids"]}
	if fields := q.Validate(); len(fields) > 0 {
		httpx.WriteValidationError(w, fields)
		return
	}

	ids := make([]uuid.UUID, len(q.IDs))
	for i, raw := range q.IDs {
		ids[i], _ = uuid.Parse(raw) // already validated by q.Validate
	}

	if _, err := h.service.DeleteByIDs(r.Context(), userID, ids); err != nil {
		h.writeServiceError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// DeleteMine handles DELETE /media/mine. It hard-deletes every media item
// belonging to the caller, used by auth-service when it deletes an
// account, to sweep up any media never referenced by an apiary/hive/
// inspection (e.g. the profile avatar, or an upload that was never
// attached to anything).
func (h *Handler) DeleteMine(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUserID(w, r)
	if !ok {
		return
	}

	if _, err := h.service.DeleteAllMine(r.Context(), userID); err != nil {
		h.writeServiceError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// requireUserID returns the authenticated user's ID (from context, set by
// httpmw.RequireAuth).
func (h *Handler) requireUserID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	userID, ok := httpmw.UserIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, httpmw.CodeMissingAuthorization, "missing authentication")
		return uuid.Nil, false
	}
	return userID, true
}

func (h *Handler) pathMediaID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "mediaId"))
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, CodeInvalidMediaID, "media id must be a valid UUID")
		return uuid.Nil, false
	}
	return id, true
}

// writeMultipartError maps a failure from ParseMultipartForm or reading
// the file part to the right response: 413 if the body was cut off for
// exceeding the configured max size, 400 for anything else malformed.
func (h *Handler) writeMultipartError(w http.ResponseWriter, err error) {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		httpx.WriteError(w, http.StatusRequestEntityTooLarge, CodeFileTooLarge, "file exceeds the maximum allowed size")
		return
	}
	httpx.WriteError(w, http.StatusBadRequest, httpx.CodeInvalidBody, "request must be valid multipart/form-data")
}

func (h *Handler) writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, media.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, CodeMediaNotFound, "media not found")
	case errors.Is(err, appmedia.ErrUnsupportedMIME):
		httpx.WriteError(w, http.StatusUnsupportedMediaType, CodeUnsupportedFileType, "unsupported file type")
	case errors.Is(err, appmedia.ErrFileTooLarge):
		httpx.WriteError(w, http.StatusRequestEntityTooLarge, CodeFileTooLarge, "file exceeds the maximum allowed size")
	case errors.Is(err, appmedia.ErrMediaIDConflict):
		httpx.WriteError(w, http.StatusConflict, CodeMediaIDConflict, "media id already used by another upload")
	case errors.Is(err, appmedia.ErrTooManyIDs):
		httpx.WriteValidationError(w, map[string]string{"ids": CodeIDsTooMany})
	default:
		httpx.WriteInternalError(w, h.log, err)
	}
}
