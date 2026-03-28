// Package checkbrowser provides headless Chrome browser health check implementation.
package checkbrowser

import "errors"

// ErrInvalidConfigType is returned when the config type doesn't match the expected type.
var ErrInvalidConfigType = errors.New("invalid config type")
