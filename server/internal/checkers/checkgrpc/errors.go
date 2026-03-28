// Package checkgrpc provides gRPC health check implementation.
package checkgrpc

import "errors"

// ErrInvalidConfigType is returned when the config type doesn't match the expected type.
var ErrInvalidConfigType = errors.New("invalid config type")
