// Package checkdocker provides Docker container health check implementation.
package checkdocker

import "errors"

// ErrInvalidConfigType is returned when the config type doesn't match the expected type.
var ErrInvalidConfigType = errors.New("invalid config type")
