package checkerdef

import (
	"errors"
	"fmt"
)

// ConfigError represents an error specific to a configuration parameter.
// It provides structured information about which parameter failed validation
// and why, making it easier for API clients to display field-specific errors.
type ConfigError struct {
	// Parameter is the name of the configuration parameter that failed validation.
	// This should match the field name in the configuration map/struct.
	Parameter string

	// Message is the human-readable error message describing what's wrong.
	Message string
}

// Error implements the error interface.
func (e *ConfigError) Error() string {
	if e.Parameter == "" {
		return e.Message
	}

	return fmt.Sprintf("%s: %s", e.Parameter, e.Message)
}

// NewConfigError creates a new ConfigError for a specific parameter.
//
// Example:
//
//	return checkerdef.NewConfigError("url", "must be a valid HTTP or HTTPS URL")
func NewConfigError(parameter, message string) error {
	return &ConfigError{
		Parameter: parameter,
		Message:   message,
	}
}

// NewConfigErrorf creates a new ConfigError with a formatted message.
//
// Example:
//
//	return checkerdef.NewConfigErrorf("timeout", "must be between %d and %d seconds", minTimeout, maxTimeout)
func NewConfigErrorf(parameter, format string, args ...any) error {
	return &ConfigError{
		Parameter: parameter,
		Message:   fmt.Sprintf(format, args...),
	}
}

// IsConfigError checks if an error is a ConfigError and returns it.
// Returns nil if the error is not a ConfigError.
func IsConfigError(err error) *ConfigError {
	var configErr *ConfigError
	if errors.As(err, &configErr) {
		return configErr
	}

	return nil
}
