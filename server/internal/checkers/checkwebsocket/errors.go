package checkwebsocket

import "errors"

var (
	// ErrInvalidConfigType is returned when the config is not of the correct type.
	ErrInvalidConfigType = errors.New("invalid config type")
	errPatternMismatch   = errors.New("response did not match expected pattern")
)
