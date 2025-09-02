// Package checkicmp provides ICMP ping check implementation.
package checkicmp

import (
	"errors"
)

// ErrInvalidConfigType is returned when the config type doesn't match the expected type.
var ErrInvalidConfigType = errors.New("invalid config type")
