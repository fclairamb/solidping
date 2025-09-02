// Package checkssh provides SSH server check implementation.
package checkssh

import (
	"errors"
)

// ErrInvalidConfigType is returned when the config type doesn't match the expected type.
var ErrInvalidConfigType = errors.New("invalid config type")
