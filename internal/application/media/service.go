// Package media implements the media use cases: upload, get metadata,
// download content, list, and delete. It depends only on the domain/media
// port, never on HTTP or PostgreSQL directly - and, unlike most other
// services in this project, never on another service either: it has no
// notion of apiaries or hives at all.
package media

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/sbezhuk/beebase-common/pagination"
	"github.com/sbezhuk/beebase-media-service/internal/domain/media"
)

// maxListIDs bounds how many ids a single GET /media?ids= (or DELETE
// /media?ids=) call may request, mirroring pagination.MaxLimit's role of
// capping the size of any one collection response.
const maxListIDs = pagination.MaxLimit

// dedupeIDs returns ids with duplicates removed, preserving first-seen
// order.
func dedupeIDs(ids []uuid.UUID) []uuid.UUID {
	dedup := make([]uuid.UUID, 0, len(ids))
	seen := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		dedup = append(dedup, id)
	}
	return dedup
}

// allowedType describes one accepted file extension: the canonical MIME
// type this service stores and serves for it (never the client's raw
// declared Content-Type), and which http.DetectContentType results are
// consistent with that extension. A nil sniffFamilies means "trust the
// extension" - used for formats Go's stdlib sniffer doesn't reliably
// recognize (e.g. HEIC).
type allowedType struct {
	canonicalMIME string
	sniffFamilies []string
}

// allowedExtensions is the server-side file type allowlist. This is a
// security control, not operator-facing configuration, so it's a
// hardcoded map rather than environment-driven: making it env-tunable
// would let a misconfigured deployment silently widen what's accepted.
var allowedExtensions = map[string]allowedType{
	".jpg":  {canonicalMIME: "image/jpeg", sniffFamilies: []string{"image/jpeg"}},
	".jpeg": {canonicalMIME: "image/jpeg", sniffFamilies: []string{"image/jpeg"}},
	".png":  {canonicalMIME: "image/png", sniffFamilies: []string{"image/png"}},
	".webp": {canonicalMIME: "image/webp", sniffFamilies: []string{"image/webp"}},
	".heic": {canonicalMIME: "image/heic", sniffFamilies: nil},
	".pdf":  {canonicalMIME: "application/pdf", sniffFamilies: []string{"application/pdf"}},
	".xml":  {canonicalMIME: "application/xml", sniffFamilies: []string{"text/xml", "text/plain", "application/xml"}},
	".txt":  {canonicalMIME: "text/plain", sniffFamilies: []string{"text/plain"}},
	".csv":  {canonicalMIME: "text/csv", sniffFamilies: []string{"text/plain", "text/csv"}},
}

// canonicalContentType validates filename's extension against
// allowedExtensions and, where the format is reliably sniffable, checks
// that content's actual bytes are consistent with it - rejecting a
// mismatch (e.g. PNG magic bytes behind a ".pdf" filename) rather than
// trusting the client. It returns the canonical MIME to store, which is
// derived entirely server-side.
func canonicalContentType(filename string, content []byte) (string, error) {
	ext := strings.ToLower(filepath.Ext(filename))

	allowed, ok := allowedExtensions[ext]
	if !ok {
		return "", ErrUnsupportedMIME
	}

	if len(allowed.sniffFamilies) == 0 {
		return allowed.canonicalMIME, nil
	}

	sniffed := http.DetectContentType(content)
	for _, family := range allowed.sniffFamilies {
		if strings.HasPrefix(sniffed, family) {
			return allowed.canonicalMIME, nil
		}
	}

	return "", ErrUnsupportedMIME
}

// Service implements the media use cases. Every method takes the
// requesting user's ID (extracted from their verified access token by the
// transport layer) and passes it straight through to the repository,
// which enforces ownership at the query level.
type Service struct {
	media              media.Repository
	maxUploadSizeBytes int64
}

// NewService constructs a Service. maxUploadSizeBytes bounds the size of
// UploadInput.Content that Upload will accept.
func NewService(repo media.Repository, maxUploadSizeBytes int64) *Service {
	return &Service{media: repo, maxUploadSizeBytes: maxUploadSizeBytes}
}

// Upload validates and stores a new file, owned by in.UserID. Whether
// (and where) it ends up referenced by an apiary or hive is entirely
// apiary-service's/hive-service's own concern - this service never
// learns about it.
//
// If in.ClientMediaID is set and already identifies media owned by the
// caller, Upload treats the call as a retried, already-completed upload:
// it does nothing further and returns the existing record with
// AlreadyExisted set, rather than creating a duplicate.
func (s *Service) Upload(ctx context.Context, in UploadInput) (*UploadResult, error) {
	if int64(len(in.Content)) > s.maxUploadSizeBytes {
		return nil, ErrFileTooLarge
	}

	contentType, err := canonicalContentType(in.OriginalFilename, in.Content)
	if err != nil {
		return nil, err
	}

	id := uuid.New()
	if in.ClientMediaID != nil {
		id = *in.ClientMediaID
	}

	m := media.New(id, in.UserID, in.OriginalFilename, contentType, int64(len(in.Content)))

	err = s.media.Create(ctx, m, in.Content)
	switch {
	case err == nil:
		return &UploadResult{Media: m}, nil
	case errors.Is(err, media.ErrIDConflict) && in.ClientMediaID != nil:
		// The ID is already in use. If it's the caller's own media, this
		// is a retried upload after e.g. a network failure - not an
		// error. GetByID can't otherwise tell "doesn't exist" from
		// "someone else's", so a miss here means the latter.
		existing, getErr := s.media.GetByID(ctx, in.UserID, id)
		if getErr == nil {
			return &UploadResult{Media: existing, AlreadyExisted: true}, nil
		}
		return nil, ErrMediaIDConflict
	default:
		return nil, fmt.Errorf("media: upload: %w", err)
	}
}

// Get returns the media identified by mediaID, if it belongs to userID.
func (s *Service) Get(ctx context.Context, userID, mediaID uuid.UUID) (*media.Media, error) {
	return s.media.GetByID(ctx, userID, mediaID)
}

// Download returns the media's metadata alongside its raw file content,
// if it belongs to userID.
func (s *Service) Download(ctx context.Context, userID, mediaID uuid.UUID) (*media.Media, []byte, error) {
	return s.media.GetContent(ctx, userID, mediaID)
}

// List returns every media item in ids that belongs to userID, in the same
// order as the (de-duplicated) ids given - the ones that don't exist, are
// already deleted, or belong to someone else are simply absent from the
// result, never an error. An empty ids returns an empty slice.
func (s *Service) List(ctx context.Context, userID uuid.UUID, ids []uuid.UUID) ([]*media.Media, error) {
	dedup := dedupeIDs(ids)

	if len(dedup) == 0 {
		return []*media.Media{}, nil
	}
	if len(dedup) > maxListIDs {
		return nil, ErrTooManyIDs
	}

	found, err := s.media.ListByIDs(ctx, userID, dedup)
	if err != nil {
		return nil, fmt.Errorf("media: list by ids: %w", err)
	}

	byID := make(map[uuid.UUID]*media.Media, len(found))
	for _, m := range found {
		byID[m.ID] = m
	}

	ordered := make([]*media.Media, 0, len(found))
	for _, id := range dedup {
		if m, ok := byID[id]; ok {
			ordered = append(ordered, m)
		}
	}

	return ordered, nil
}

// Delete deletes the media identified by mediaID, if it belongs to userID.
func (s *Service) Delete(ctx context.Context, userID, mediaID uuid.UUID) error {
	return s.media.Delete(ctx, userID, mediaID)
}

// DeleteByIDs hard-deletes every media item in ids belonging to userID.
// Used by apiary-service/hive-service to cascade a delete across every
// media id an apiary/hive itself knows it references (its own Images).
// ids beyond maxListIDs is rejected the same way List's are - the
// deleting service already knows its exact, locally-stored id list, so a
// caller can never legitimately need more than that in one call.
func (s *Service) DeleteByIDs(ctx context.Context, userID uuid.UUID, ids []uuid.UUID) (int64, error) {
	dedup := dedupeIDs(ids)
	if len(dedup) == 0 {
		return 0, nil
	}
	if len(dedup) > maxListIDs {
		return 0, ErrTooManyIDs
	}

	return s.media.DeleteByIDs(ctx, userID, dedup)
}

// DeleteAllMine hard-deletes every media item belonging to userID. Used by
// auth-service when it deletes an account, as the final sweep for media
// never referenced by any apiary/hive/inspection (e.g. the profile avatar,
// or an upload that was never attached to anything) - unlike DeleteByIDs,
// there is no cap here, since this always means "everything", not a
// caller-supplied list.
func (s *Service) DeleteAllMine(ctx context.Context, userID uuid.UUID) (int64, error) {
	return s.media.DeleteAllByUser(ctx, userID)
}
