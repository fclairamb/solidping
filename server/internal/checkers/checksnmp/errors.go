// Package checksnmp provides SNMP health check implementation.
package checksnmp

import (
	"errors"
)

// ErrInvalidConfigType is returned when the config type doesn't match the expected type.
var ErrInvalidConfigType = errors.New("invalid config type")
