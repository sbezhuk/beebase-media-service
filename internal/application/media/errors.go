package media

import "errors"

var (
	// ErrApiaryNotFound is returned when the apiary a file is being
	// attached to doesn't exist, doesn't belong to the caller, or its
	// ownership couldn't be confirmed. As with media.ErrNotFound, these
	// cases are deliberately indistinguishable: a caller must not be able
	// to tell whether another user's apiary ID exists at all.
	ErrApiaryNotFound = errors.New("apiary not found")

	// ErrHiveNotFound is the hive equivalent of ErrApiaryNotFound.
	ErrHiveNotFound = errors.New("hive not found")

	// ErrInvalidOwnerType is returned when owner_type isn't "APIARY" or
	// "HIVE".
	ErrInvalidOwnerType = errors.New(`owner type must be "APIARY" or "HIVE"`)

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
)
