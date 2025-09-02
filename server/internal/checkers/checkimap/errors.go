// Package checkimap provides IMAP server health check implementation.
package checkimap

import (
	"errors"
)

// ErrInvalidConfigType is returned when the config type doesn't match the expected type.
var ErrInvalidConfigType = errors.New("invalid config type")
