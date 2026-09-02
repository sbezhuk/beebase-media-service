package media

import "errors"

// ErrNotFound is returned both when no media matches the given ID and
// when it exists but belongs to a different user. The two cases are
// deliberately indistinguishable to a caller: a user must never be able
// to tell whether another user's media ID exists at all.
var ErrNotFound = errors.New("media not found")

// ErrIDConflict is returned by Create when the given ID already belongs
// to an existing row. IDs are ordinarily random UUIDs generated
// server-side, so a genuine collision is a practical impossibility; this
// only happens when a client supplies its own ID as an upload
// idempotency key. The application layer disambiguates a legitimate
// replay (same user, same ID) from a genuine conflict (someone else's
// ID) by following up with GetByID.
var ErrIDConflict = errors.New("media id already in use")
