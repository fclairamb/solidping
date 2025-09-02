// Package checkerdef defines the core interfaces for health checkers.
package checkerdef

import (
	"context"
	"time"
)

// Config is the interface that all check configurations must implement.
// Each checker defines its own config struct with protocol-specific fields.
type Config interface {
	// FromMap populates the configuration from a map.
	// Returns an error if the map contains invalid values.
	// TODO: Remove it
	FromMap(configMap map[string]any) error

	// GetConfig returns the configuration as a map.
	// TODO: Support it through the `models.Result` so that we can pass it directly to the plugins
	GetConfig() map[string]any
}

// CheckSpec represents a sample check configuration with metadata.
type CheckSpec struct {
	// Name is the human-readable name for the sample check.
	Name string

	// Slug is the URL-friendly identifier for the sample check.
	Slug string

	// Period is the check frequency interval.
	Period time.Duration

	// Config is the actual checker configuration.
	Config map[string]any
}

// Checker is the interface that all protocol checkers must implement.
type Checker interface {
	// Type returns the check type identifier this checker handles (e.g., "http", "tcp").
	Type() CheckType

	// Validate checks if the configuration is valid.
	// It shall not perform any network operations.
	// Returns nil if valid, or an error describing what's wrong.
	Validate(spec *CheckSpec) error

	// Execute performs the check and returns the result.
	// The context should be used for cancellation and timeout control.
	// The config is already validated before being passed to Execute.
	// Returns a pointer to Result and an error. If error is not nil, Result will be nil.
	Execute(ctx context.Context, config Config) (*Result, error)
}

// CheckerSamplesProvider is an optional interface that provides sample configurations.
type CheckerSamplesProvider interface {
	// GetSampleConfigs returns a slice of sample configurations with metadata.
	GetSampleConfigs(opts *ListSampleOptions) []CheckSpec
}
