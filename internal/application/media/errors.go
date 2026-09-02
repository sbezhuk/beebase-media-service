package media

import "errors"

var (
	// ErrUnsupportedMIME is returned when a file's extension isn't
	// recognized, or its sniffed content doesn't match what the extension
	// implies (a spoofing attempt, or simply a mislabeled file).
	ErrUnsupportedMIME = errors.New("unsupported file type")

	// ErrFileTooLarge is returned when the uploaded content exceeds the
	// configured maximum. The HTTP layer also caps the request body size
	// up front (so an oversized upload never has to be fully read into
	// memory first), but the service checks again so this rule holds for
	// any caller, not only ones going through HTTP.
	ErrFileTooLarge = errors.New("file exceeds the maximum allowed size")

	// ErrMediaIDConflict is returned when a client-supplied idempotency
	// key (ClientMediaID) already belongs to a different user's media.
	ErrMediaIDConflict = errors.New("media id already used by another upload")

	// ErrTooManyIDs is returned when GET /media?ids= is called with more
	// than maxListIDs distinct ids.
	ErrTooManyIDs = errors.New("too many ids requested")
)
