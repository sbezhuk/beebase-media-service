// Package media implements the media use cases: upload, get metadata,
// download content, list, and delete. It depends only on the domain/media
// port and the ApiaryVerifier/HiveVerifier ports declared in this
// package, never on HTTP or PostgreSQL directly.
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
	apiaries           ApiaryVerifier
	hives              HiveVerifier
	maxUploadSizeBytes int64
}

// NewService constructs a Service. maxUploadSizeBytes bounds the size of
// UploadInput.Content that Upload will accept.
func NewService(repo media.Repository, apiaries ApiaryVerifier, hives HiveVerifier, maxUploadSizeBytes int64) *Service {
	return &Service{media: repo, apiaries: apiaries, hives: hives, maxUploadSizeBytes: maxUploadSizeBytes}
}

// Upload validates and stores a new file, owned by in.UserID and
// unattached to anything - the caller doesn't need to own (or even
// specify) an apiary/hive yet. Attach links it to one afterward.
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

// Attach links the caller's own media item to an apiary or hive
// (whichever in.OwnerType selects), after confirming with apiary-service
// or hive-service that the caller owns that entity. It's idempotent:
// attaching a media item that's already linked to the same owner
// succeeds without error, so a retried request after a network failure
// is safe. Attaching media already linked to a *different* owner fails
// with media.ErrAlreadyAttached - a media item's owner is fixed the first
// time it's attached and can't be moved.
func (s *Service) Attach(ctx context.Context, in AttachInput) (*media.Media, error) {
	switch in.OwnerType {
	case media.OwnerTypeApiary:
		if err := s.apiaries.Verify(ctx, in.AccessToken, in.OwnerID); err != nil {
			return nil, err
		}
	case media.OwnerTypeHive:
		if err := s.hives.Verify(ctx, in.AccessToken, in.OwnerID); err != nil {
			return nil, err
		}
	default:
		return nil, ErrInvalidOwnerType
	}

	m, err := s.media.Attach(ctx, in.UserID, in.MediaID, in.OwnerType, in.OwnerID)
	if err != nil {
		if errors.Is(err, media.ErrNotFound) || errors.Is(err, media.ErrAlreadyAttached) {
			return nil, err
		}
		return nil, fmt.Errorf("media: attach: %w", err)
	}

	return m, nil
}

// Download returns the media's metadata alongside its raw file content,
// if it belongs to userID.
func (s *Service) Download(ctx context.Context, userID, mediaID uuid.UUID) (*media.Media, []byte, error) {
	return s.media.GetContent(ctx, userID, mediaID)
}

// List returns the page of media described by p, attached to ownerID of
// type ownerType, out of every such media belonging to userID.
func (s *Service) List(ctx context.Context, userID uuid.UUID, ownerType string, ownerID uuid.UUID, p pagination.Params) ([]*media.Media, int, error) {
	return s.media.ListByOwner(ctx, userID, ownerType, ownerID, p)
}

// Delete deletes the media identified by mediaID, if it belongs to userID.
func (s *Service) Delete(ctx context.Context, userID, mediaID uuid.UUID) error {
	return s.media.Delete(ctx, userID, mediaID)
}

// DeleteByOwner hard-deletes every media item attached to ownerID of type
// ownerType, belonging to userID. Used when an ancestor entity (a hive or
// an apiary) is itself being cascade-deleted.
func (s *Service) DeleteByOwner(ctx context.Context, userID uuid.UUID, ownerType string, ownerID uuid.UUID) (int64, error) {
	return s.media.DeleteByOwner(ctx, userID, ownerType, ownerID)
}
