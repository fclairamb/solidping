// Package checkjs provides a JavaScript-based check implementation using the goja runtime.
package checkjs

import "errors"

// ErrInvalidConfigType is returned when the config type doesn't match the expected type.
var ErrInvalidConfigType = errors.New("invalid config type")
