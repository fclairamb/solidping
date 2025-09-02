// Package checkpop3 provides POP3 server health check implementation.
package checkpop3

import (
	"errors"
)

// ErrInvalidConfigType is returned when the config type doesn't match the expected type.
var ErrInvalidConfigType = errors.New("invalid config type")
