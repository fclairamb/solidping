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

// ConfigNormalizer is an *optional* interface a checker config can implement to
// rewrite an effective (post-merge) config map into its canonical stored shape
// — e.g. folding HTTP's legacy `username`/`password` pair into a single
// reserved `basicAuth` key that `SecretFields` can then encrypt as a whole.
//
// It is probed exactly like credentials.SecretFielder: an optional interface
// rather than a method on Config, so the 30+ checker types that need no
// normalization stay untouched.
//
// Implementations MUST NOT mutate the input map (the caller may still hold it)
// and MUST be idempotent — normalizing an already-normalized map must be a
// no-op, since the same manifest can be applied repeatedly.
//
// An error returned here MUST be a *ConfigError so handlers map it to a 400:
// on the PATCH path normalization is the only validation the config sees.
type ConfigNormalizer interface {
	NormalizeConfig(configMap map[string]any) (map[string]any, error)
}

// NormalizeConfigFor normalizes a config map through cfg's optional
// ConfigNormalizer, returning the map untouched when cfg does not implement it.
func NormalizeConfigFor(cfg any, configMap map[string]any) (map[string]any, error) {
	if cfg == nil || configMap == nil {
		return configMap, nil
	}

	if normalizer, ok := cfg.(ConfigNormalizer); ok {
		return normalizer.NormalizeConfig(configMap)
	}

	return configMap, nil
}

// CheckerSamplesProvider is an optional interface that provides sample configurations.
type CheckerSamplesProvider interface {
	// GetSampleConfigs returns a slice of sample configurations with metadata.
	GetSampleConfigs(opts *ListSampleOptions) []CheckSpec
}
