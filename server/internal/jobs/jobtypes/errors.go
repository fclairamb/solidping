// Package jobtypes contains implementations of various job types.
package jobtypes

import "errors"

// ErrCheckRunNotImplemented is returned when check-run is attempted but not fully implemented.
var ErrCheckRunNotImplemented = errors.New(
	"check-run job not yet fully implemented - needs integration with check execution system",
)
