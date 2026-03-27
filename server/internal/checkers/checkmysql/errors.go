// Package checkmysql provides MySQL/MariaDB database health check implementation.
package checkmysql

import "errors"

// ErrInvalidConfigType is returned when the config type doesn't match the expected type.
var ErrInvalidConfigType = errors.New("invalid config type")
