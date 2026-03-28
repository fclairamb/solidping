// Package checkrabbitmq provides RabbitMQ health check implementation.
package checkrabbitmq

import "errors"

// ErrInvalidConfigType is returned when the config type doesn't match the expected type.
var ErrInvalidConfigType = errors.New("invalid config type")
