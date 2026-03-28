// Package checkoracle provides Oracle Database health check implementation.
package checkoracle

import "errors"

// ErrInvalidConfigType is returned when the config type doesn't match the expected type.
var ErrInvalidConfigType = errors.New("invalid config type")
