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

// BurstBudgeter is an optional interface a checker config implements when the
// check's wall-clock cost scales with burst configuration (count/interval/
// timeout) rather than being one probe's duration (spec 2026-09-21-01). The
// check worker probes it after parsing the config and raises the execution
// budget to the returned worst case, so a burst that meets packet loss is
// never truncated by a budget sized for a single probe. `timeout` keeps its
// per-packet meaning inside the checker; it is never again overloaded as the
// whole-burst budget.
type BurstBudgeter interface {
	// BurstBudget returns the worst-case wall-clock time a full execution
	// needs with this config, margins excluded.
	BurstBudget() time.Duration
}

// ExtraBudgeter is an optional interface a checker config implements when its
// execution needs wall-clock time beyond the checker's own probe timeout for
// work that happens AFTER the verdict is decided — the browser checker's
// screenshot capture, taken against a session it deliberately keeps alive
// past its probe timeout, is the motivating case (spec 2026-09-25-35). The
// check worker probes it after parsing the config and adds the returned
// duration to the HARD execution context deadline only; the budget threaded
// into the checker's own config (`timeout`) is unchanged, so extra time here
// can never let a slow target answer that would otherwise have timed out —
// it only extends how long a checker gets to do something once its own
// verdict already exists.
type ExtraBudgeter interface {
	// ExtraBudget returns the extra wall-clock time this execution needs
	// beyond its own timeout. forcedCapture reports whether this run is an
	// on-demand capture (spec 2026-09-25-34, "Capture now") that keeps its
	// capture regardless of the config's own opt-in: the caller passes it
	// through because that is a property of the CLAIMED JOB, not of the
	// check's stored configuration, so the config alone cannot know it.
	ExtraBudget(forcedCapture bool) time.Duration
}
