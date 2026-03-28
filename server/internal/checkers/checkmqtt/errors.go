// Package checkmqtt provides MQTT broker health check implementation.
package checkmqtt

import "errors"

// ErrInvalidConfigType is returned when the config type doesn't match the expected type.
var ErrInvalidConfigType = errors.New("invalid config type")
