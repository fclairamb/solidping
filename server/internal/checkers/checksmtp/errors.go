// Package checksmtp provides SMTP server health check implementation.
package checksmtp

import (
	"errors"
)

// ErrInvalidConfigType is returned when the config type doesn't match the expected type.
var ErrInvalidConfigType = errors.New("invalid config type")
