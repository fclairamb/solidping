// Package checkkafka provides Kafka cluster health check implementation.
package checkkafka

import "errors"

// ErrInvalidConfigType is returned when the config type doesn't match the expected type.
var ErrInvalidConfigType = errors.New("invalid config type")
