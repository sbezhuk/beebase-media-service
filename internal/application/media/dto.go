package media

import (
	"github.com/google/uuid"

	"github.com/sbezhuk/beebase-media-service/internal/domain/media"
)

// UploadInput is the input to Service.Upload.
type UploadInput struct {
	UserID uuid.UUID
	// AccessToken is the caller's own access token, forwarded to
	// apiary-service or hive-service so it can run its own, identical
	// ownership check rather than this service trusting a client-supplied
	// user/owner pairing.
	AccessToken string

	OwnerType string // media.OwnerTypeApiary | media.OwnerTypeHive
	OwnerID   uuid.UUID

	// ClientMediaID, if set, makes Upload idempotent: retrying the same
	// upload (e.g. after a network failure) with the same ID returns the
	// already-stored media instead of creating a duplicate.
	ClientMediaID *uuid.UUID

	OriginalFilename string
	Content          []byte
}

// UploadResult is the output of Service.Upload.
type UploadResult struct {
	Media *media.Media
	// AlreadyExisted is true when this call was an idempotent replay of a
	// prior successful upload (matched by ClientMediaID) rather than a
	// new one - the caller should respond 200, not 201.
	AlreadyExisted bool
}
