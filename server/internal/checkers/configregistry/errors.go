package configregistry

import "errors"

// ErrUnknownType is returned by ValidateSpec for a check type this build does
// not implement. Callers that render user-facing findings test the type with
// IsKnownType first and report their own message; this exists so a caller that
// forgets cannot silently treat an unknown type as valid.
var ErrUnknownType = errors.New("unknown check type")
