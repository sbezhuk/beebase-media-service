// Package media holds the HTTP handlers for media upload/download.
// Handlers stay thin: they decode/validate the request, pull the
// authenticated user's ID (and, for Upload, their raw access token,
// forwarded to apiary-service/hive-service) from the request, call into
// the application service, and map the result (or error) to a response.
// No business logic or repository access happens here.
package media

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	httpmw "github.com/sbezhuk/beebase-common/authmw"
	"github.com/sbezhuk/beebase-common/httpx"
	"github.com/sbezhuk/beebase-common/pagination"
	appmedia "github.com/sbezhuk/beebase-media-service/internal/application/media"
	"github.com/sbezhuk/beebase-media-service/internal/domain/media"
)

// Error codes for media failures, returned as the top-level "error.code".
// Each is a stable key a client can map to a localized message.
// CodeApiaryNotFound/CodeHiveNotFound intentionally reuse
// apiary-service's/hive-service's own code strings, since it's the same
// meaning from the client's point of view regardless of which service
// returned it.
const (
	CodeMediaNotFound       = "media_not_found"
	CodeInvalidMediaID      = "invalid_media_id"
	CodeApiaryNotFound      = "apiary_not_found"
	CodeHiveNotFound        = "hive_not_found"
	CodeInvalidOwnerType    = "invalid_owner_type"
	CodeUnsupportedFileType = "unsupported_file_type"
	CodeFileTooLarge        = "file_too_large"
	CodeMediaIDConflict     = "media_id_conflict"
	CodeFileRequired        = "file_required"
)

// multipartOverheadBytes accounts for multipart boundaries, headers, and
// the other form fields (owner_type, owner_id, media_id) beyond the file
// content itself, when bounding the total request body size.
const multipartOverheadBytes = 64 * 1024

// Handler exposes the media HTTP endpoints. Every method requires the
// request to have already passed through httpmw.RequireAuth.
type Handler struct {
	service            *appmedia.Service
	log                *slog.Logger
	maxUploadSizeBytes int64
}

// NewHandler returns a Handler backed by service. maxUploadSizeBytes
// bounds the total request body accepted by Upload.
func NewHandler(service *appmedia.Service, log *slog.Logger, maxUploadSizeBytes int64) *Handler {
	return &Handler{service: service, log: log, maxUploadSizeBytes: maxUploadSizeBytes}
}

// Upload handles POST /media.
func (h *Handler) Upload(w http.ResponseWriter, r *http.Request) {
	userID, token, ok := h.requireAuth(w, r)
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
		OwnerType: r.FormValue("owner_type"),
		OwnerID:   r.FormValue("owner_id"),
		MediaID:   r.FormValue("media_id"),
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

	ownerID, _ := uuid.Parse(req.OwnerID) // already validated by req.Validate

	var clientMediaID *uuid.UUID
	if req.MediaID != "" {
		id, _ := uuid.Parse(req.MediaID) // already validated by req.Validate
		clientMediaID = &id
	}

	result, err := h.service.Upload(r.Context(), appmedia.UploadInput{
		UserID:           userID,
		AccessToken:      token,
		OwnerType:        req.OwnerType,
		OwnerID:          ownerID,
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
	httpx.WriteJSON(w, status, newResponse(result.Media))
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

	httpx.WriteJSON(w, http.StatusOK, newResponse(got))
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

// List handles GET /media.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUserID(w, r)
	if !ok {
		return
	}

	q := ListQuery{
		OwnerType: r.URL.Query().Get("owner_type"),
		OwnerID:   r.URL.Query().Get("owner_id"),
	}
	fields := q.Validate()

	p, pageFields := pagination.ParseParams(r)
	for k, v := range pageFields {
		fields[k] = v
	}
	if len(fields) > 0 {
		httpx.WriteValidationError(w, fields)
		return
	}

	ownerID, _ := uuid.Parse(q.OwnerID) // already validated by q.Validate

	items, total, err := h.service.List(r.Context(), userID, q.OwnerType, ownerID, p)
	if err != nil {
		h.writeServiceError(w, err)
		return
	}

	httpx.WriteJSON(w, http.StatusOK, pagination.NewResponse(newListResponse(items), p, total))
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

// requireAuth returns the authenticated user's ID alongside their raw
// access token (read back off the request's own Authorization header,
// which RequireAuth already validated) so it can be forwarded to
// apiary-service/hive-service.
func (h *Handler) requireAuth(w http.ResponseWriter, r *http.Request) (uuid.UUID, string, bool) {
	userID, ok := h.requireUserID(w, r)
	if !ok {
		return uuid.Nil, "", false
	}

	const prefix = "Bearer "
	token := strings.TrimPrefix(r.Header.Get("Authorization"), prefix)

	return userID, token, true
}

func (h *Handler) pathMediaID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "mediaID"))
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
	case errors.Is(err, appmedia.ErrApiaryNotFound):
		httpx.WriteError(w, http.StatusNotFound, CodeApiaryNotFound, "apiary not found")
	case errors.Is(err, appmedia.ErrHiveNotFound):
		httpx.WriteError(w, http.StatusNotFound, CodeHiveNotFound, "hive not found")
	case errors.Is(err, appmedia.ErrInvalidOwnerType):
		httpx.WriteError(w, http.StatusBadRequest, CodeInvalidOwnerType, `owner_type must be "APIARY" or "HIVE"`)
	case errors.Is(err, appmedia.ErrUnsupportedMIME):
		httpx.WriteError(w, http.StatusUnsupportedMediaType, CodeUnsupportedFileType, "unsupported file type")
	case errors.Is(err, appmedia.ErrFileTooLarge):
		httpx.WriteError(w, http.StatusRequestEntityTooLarge, CodeFileTooLarge, "file exceeds the maximum allowed size")
	case errors.Is(err, appmedia.ErrMediaIDConflict):
		httpx.WriteError(w, http.StatusConflict, CodeMediaIDConflict, "media id already used by another upload")
	default:
		httpx.WriteInternalError(w, h.log, err)
	}
}
