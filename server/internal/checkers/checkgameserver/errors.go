// Package checkgameserver provides Steam/Source game server A2S health check implementation.
package checkgameserver

import "errors"

// ErrInvalidConfigType is returned when the config type doesn't match the expected type.
var ErrInvalidConfigType = errors.New("invalid config type")
