// Package checkmssql provides Microsoft SQL Server health check implementation.
package checkmssql

import "errors"

// ErrInvalidConfigType is returned when the config type doesn't match the expected type.
var ErrInvalidConfigType = errors.New("invalid config type")
