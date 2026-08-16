// Package checkprometheus provides a check that reads one numeric value out of
// a Prometheus metrics endpoint (or a Prometheus server, via PromQL) and grades
// it against warning/critical thresholds.
//
// It is the first check type that inspects a *value* rather than a service:
// every other type answers "is it up?", while this one answers "is the number
// the target reports still acceptable?" — a queue depth, a free-disk gauge, an
// error counter. The graded outcome uses the shared two-tier model:
// critical breached → StatusDown (pages), warning breached → StatusWarning
// (amber, counts as up, no incident), otherwise StatusUp.
package checkprometheus

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// Modes.
const (
	// ModeScrape fetches the URL and parses the Prometheus text exposition
	// format, selecting a series by metric name + label subset.
	ModeScrape = "scrape"
	// ModePromQL treats the URL as a Prometheus server base URL and runs an
	// instant query against /api/v1/query. It is also the escape hatch for
	// rate() over counters — this checker does NO client-side rate
	// computation (that needs state between executions; out of scope).
	ModePromQL = "promql"
)

// Multi-series match strategies.
const (
	// MatchSingle is the default: more than one matching series is an error,
	// which forces the operator to write an unambiguous selector.
	MatchSingle = "single"
	MatchMin    = "min"
	MatchMax    = "max"
	MatchSum    = "sum"
	MatchAvg    = "avg"
)

// onMissing behaviors, applied when the selector matches nothing.
const (
	// OnMissingDown is the default: an absent metric usually means the target
	// is broken, not healthy.
	OnMissingDown    = "down"
	OnMissingWarning = "warning"
	OnMissingUp      = "up"
)

// Comparison operators. The check fires when `value <operator> threshold`
// is true.
const (
	OpGreater      = ">"
	OpGreaterEqual = ">="
	OpLess         = "<"
	OpLessEqual    = "<="
	OpEqual        = "=="
	OpNotEqual     = "!="
)

// DefaultOperator is used when `operator` is absent from the config. `>` is
// the overwhelmingly common shape ("alert when the number gets too big").
const DefaultOperator = OpGreater

const (
	defaultTimeout = 15 * time.Second
	maxTimeout     = 60 * time.Second

	// MaxScrapeBytes caps the scrape-mode response body. A body over the cap
	// is REFUSED (StatusDown, with the cap named in the output), never
	// truncated: a truncated exposition body parses into wrong values, which
	// is strictly worse than an error. promql responses are bounded by the
	// query itself and are not capped here.
	MaxScrapeBytes = 5 * 1024 * 1024
)

// Config map keys — constants so FromMap / GetConfig cannot drift.
const (
	keyURL           = "url"
	keyMode          = "mode"
	keyMetric        = "metric"
	keyLabels        = "labels"
	keyQuery         = "query"
	keyOperator      = "operator"
	keyWarningValue  = "warningValue"
	keyCriticalValue = "criticalValue"
	keyMatch         = "match"
	keyOnMissing     = "onMissing"
	keyHeaders       = "headers"
	keyTimeout       = "timeout"
)

//nolint:gochecknoglobals // read-only lookup tables
var (
	validModes      = []string{ModeScrape, ModePromQL}
	validMatches    = []string{MatchSingle, MatchMin, MatchMax, MatchSum, MatchAvg}
	validOnMissing  = []string{OnMissingDown, OnMissingWarning, OnMissingUp}
	validOperators  = []string{OpGreater, OpGreaterEqual, OpLess, OpLessEqual, OpEqual, OpNotEqual}
	orderedGreaters = []string{OpGreater, OpGreaterEqual}
	orderedLessers  = []string{OpLess, OpLessEqual}
	equalityOps     = []string{OpEqual, OpNotEqual}
)

// PrometheusConfig defines the configuration for a Prometheus metric check.
//
// WarningValue / CriticalValue are POINTERS on purpose. Thresholds are
// float64 and 0 is a perfectly legal threshold ("alert when free slots hit
// 0"), so "is it set?" cannot be answered by the zero value the way
// checkdomain answers it for days — presence has to be tracked explicitly or
// `warningValue: 0` silently means "unset".
type PrometheusConfig struct {
	// URL is the metrics endpoint (scrape mode) or the Prometheus server
	// base URL (promql mode).
	URL string `json:"url"`

	// Mode is "scrape" (default) or "promql".
	Mode string `json:"mode,omitempty"`

	// Metric is the metric (series) name to select, scrape mode only.
	// Histogram/summary families are addressed through their flattened
	// series names: `<name>_sum`, `<name>_count`, `<name>_bucket` (with an
	// `le` label), or `<name>` with a `quantile` label.
	Metric string `json:"metric,omitempty"`

	// Labels narrows the selection to series carrying at least these
	// label/value pairs (exact-match subset), scrape mode only.
	Labels map[string]string `json:"labels,omitempty"`

	// Query is the PromQL instant query, promql mode only.
	Query string `json:"query,omitempty"`

	// Operator is the comparison applied against the thresholds.
	Operator string `json:"operator,omitempty"`

	// WarningValue, when breached, yields StatusWarning (amber, counts as
	// up, never pages). A config with only a warning tier is valid — it can
	// never produce StatusDown, exactly like `warningDays` on domain/ssl.
	WarningValue *float64 `json:"warningValue,omitempty"`

	// CriticalValue, when breached, yields StatusDown (pages).
	CriticalValue *float64 `json:"criticalValue,omitempty"`

	// Match decides what happens when more than one series matches.
	Match string `json:"match,omitempty"`

	// OnMissing is the status reported when nothing matches.
	OnMissing string `json:"onMissing,omitempty"`

	// Headers are sent with the request (bearer/basic auth on the metrics
	// endpoint is routine). Same shape — and same at-rest handling — as the
	// HTTP check's `headers`: stored in the public config, not encrypted.
	Headers map[string]string `json:"headers,omitempty"`

	// Timeout caps the HTTP request; default 15s, max 60s.
	Timeout time.Duration `json:"timeout,omitempty"`
}

// FromMap populates the configuration from a map (the JSONB `config` shape).
func (c *PrometheusConfig) FromMap(configMap map[string]any) error {
	for key, target := range map[string]*string{
		keyURL:       &c.URL,
		keyMode:      &c.Mode,
		keyMetric:    &c.Metric,
		keyQuery:     &c.Query,
		keyOperator:  &c.Operator,
		keyMatch:     &c.Match,
		keyOnMissing: &c.OnMissing,
	} {
		if configMap[key] == nil {
			continue
		}

		v, ok := configMap[key].(string)
		if !ok {
			return checkerdef.NewConfigError(key, "must be a string")
		}

		*target = v
	}

	labels, err := readStringMap(configMap, keyLabels)
	if err != nil {
		return err
	}

	c.Labels = labels

	headers, err := readStringMap(configMap, keyHeaders)
	if err != nil {
		return err
	}

	c.Headers = headers

	if c.WarningValue, err = readThreshold(configMap, keyWarningValue); err != nil {
		return err
	}

	if c.CriticalValue, err = readThreshold(configMap, keyCriticalValue); err != nil {
		return err
	}

	if configMap[keyTimeout] != nil {
		raw, ok := configMap[keyTimeout].(string)
		if !ok {
			return checkerdef.NewConfigError(keyTimeout, "must be a duration string")
		}

		d, parseErr := time.ParseDuration(raw)
		if parseErr != nil {
			return checkerdef.NewConfigError(keyTimeout, "must be a valid duration string")
		}

		c.Timeout = d
	}

	return nil
}

// errThresholdAbsent is the sentinel meaning "the key is simply not present"
// — distinct from "the key is present but not a number", which is a real
// config error. Presence is the whole point here: 0 is a legal threshold, so
// absence can never be signaled by the zero value.
var errThresholdAbsent = errors.New("threshold key absent")

// readFloatPtr reads a numeric key, returning nil when the key is absent.
// The pointer return is what makes `warningValue: 0` distinguishable from
// "warningValue not set".
func readFloatPtr(configMap map[string]any, key string) (*float64, error) {
	raw, present := configMap[key]
	if !present || raw == nil {
		return nil, errThresholdAbsent
	}

	switch value := raw.(type) {
	case float64:
		return &value, nil
	case float32:
		converted := float64(value)

		return &converted, nil
	case int:
		converted := float64(value)

		return &converted, nil
	case int64:
		converted := float64(value)

		return &converted, nil
	case json.Number:
		converted, err := value.Float64()
		if err != nil {
			return nil, checkerdef.NewConfigError(key, "must be a number")
		}

		return &converted, nil
	default:
		return nil, checkerdef.NewConfigError(key, "must be a number")
	}
}

// readThreshold adapts readFloatPtr's sentinel back to "(nil, nil) means
// absent" for the caller.
func readThreshold(configMap map[string]any, key string) (*float64, error) {
	value, err := readFloatPtr(configMap, key)
	if errors.Is(err, errThresholdAbsent) {
		return nil, nil //nolint:nilnil // absence is the documented outcome here
	}

	return value, err
}

// readStringMap reads a map[string]string-shaped key, tolerating the
// map[string]any shape JSON decoding produces. An absent key yields an empty
// map — every caller tests it with len(), so "absent" and "empty" mean the
// same thing here.
func readStringMap(configMap map[string]any, key string) (map[string]string, error) {
	raw, present := configMap[key]
	if !present || raw == nil {
		return map[string]string{}, nil
	}

	switch typed := raw.(type) {
	case map[string]string:
		return typed, nil
	case map[string]any:
		out := make(map[string]string, len(typed))

		for name, val := range typed {
			str, ok := val.(string)
			if !ok {
				return nil, checkerdef.NewConfigErrorf(key, "value for %q must be a string", name)
			}

			out[name] = str
		}

		return out, nil
	default:
		return nil, checkerdef.NewConfigError(key, "must be an object of string values")
	}
}

// GetConfig serializes the config back to a map.
func (c *PrometheusConfig) GetConfig() map[string]any {
	cfg := map[string]any{keyURL: c.URL}

	for key, value := range map[string]string{
		keyMode:      c.Mode,
		keyMetric:    c.Metric,
		keyQuery:     c.Query,
		keyOperator:  c.Operator,
		keyMatch:     c.Match,
		keyOnMissing: c.OnMissing,
	} {
		if value != "" {
			cfg[key] = value
		}
	}

	if len(c.Labels) > 0 {
		cfg[keyLabels] = c.Labels
	}

	if len(c.Headers) > 0 {
		cfg[keyHeaders] = c.Headers
	}

	if c.WarningValue != nil {
		cfg[keyWarningValue] = *c.WarningValue
	}

	if c.CriticalValue != nil {
		cfg[keyCriticalValue] = *c.CriticalValue
	}

	if c.Timeout != 0 {
		cfg[keyTimeout] = c.Timeout.String()
	}

	return cfg
}

// Validate checks the configuration without performing any network call.
func (c *PrometheusConfig) Validate() error {
	if err := c.validateTarget(); err != nil {
		return err
	}

	if !slices.Contains(validMatches, c.EffectiveMatch()) {
		return checkerdef.NewConfigErrorf(keyMatch, "must be one of %s", strings.Join(validMatches, ", "))
	}

	if !slices.Contains(validOnMissing, c.EffectiveOnMissing()) {
		return checkerdef.NewConfigErrorf(keyOnMissing, "must be one of %s", strings.Join(validOnMissing, ", "))
	}

	if c.Timeout < 0 {
		return checkerdef.NewConfigError(keyTimeout, "must be positive")
	}

	if c.Timeout > maxTimeout {
		return checkerdef.NewConfigErrorf(keyTimeout, "must be <= %s", maxTimeout)
	}

	return c.validateThresholds()
}

// validateTarget validates the url and the mode-appropriate selector fields.
func (c *PrometheusConfig) validateTarget() error {
	if c.URL == "" {
		return checkerdef.NewConfigError(keyURL, "is required")
	}

	if !strings.HasPrefix(c.URL, "http://") && !strings.HasPrefix(c.URL, "https://") {
		return checkerdef.NewConfigError(keyURL, "must start with http:// or https://")
	}

	mode := c.EffectiveMode()
	if !slices.Contains(validModes, mode) {
		return checkerdef.NewConfigErrorf(keyMode, "must be one of %s", strings.Join(validModes, ", "))
	}

	if mode == ModeScrape && c.Metric == "" {
		return checkerdef.NewConfigError(keyMetric, "is required in scrape mode")
	}

	if mode == ModePromQL && c.Query == "" {
		return checkerdef.NewConfigError(keyQuery, "is required in promql mode")
	}

	return nil
}

// validateThresholds enforces the threshold rules. It mirrors the ordering
// logic of checkssl/checkdomain's validateThresholds, adapted to an operator
// that can point either way:
//   - at least one tier must be set;
//   - for `>` / `>=`, critical must be >= warning (critical is the further
//     breach in the same direction);
//   - for `<` / `<=`, the reverse;
//   - `==` / `!=` accept criticalValue only — a "warning tier" for an equality
//     test is meaningless (there is no ordering between two equality targets).
func (c *PrometheusConfig) validateThresholds() error {
	operator := c.EffectiveOperator()
	if !slices.Contains(validOperators, operator) {
		return checkerdef.NewConfigErrorf(keyOperator, "must be one of %s", strings.Join(validOperators, " "))
	}

	if c.WarningValue == nil && c.CriticalValue == nil {
		return checkerdef.NewConfigErrorf(
			keyCriticalValue, "at least one of %s or %s is required", keyWarningValue, keyCriticalValue,
		)
	}

	if slices.Contains(equalityOps, operator) {
		if c.WarningValue != nil {
			return checkerdef.NewConfigErrorf(
				keyWarningValue, "is not supported with the %q operator; use %s only",
				operator, keyCriticalValue,
			)
		}

		return nil
	}

	if c.WarningValue == nil || c.CriticalValue == nil {
		return nil
	}

	warning, critical := *c.WarningValue, *c.CriticalValue

	if slices.Contains(orderedGreaters, operator) && critical < warning {
		return checkerdef.NewConfigError(keyCriticalValue, fmt.Sprintf(
			"must be >= %s (%g) for the %q operator, got %g",
			keyWarningValue, warning, operator, critical,
		))
	}

	if slices.Contains(orderedLessers, operator) && critical > warning {
		return checkerdef.NewConfigError(keyCriticalValue, fmt.Sprintf(
			"must be <= %s (%g) for the %q operator, got %g",
			keyWarningValue, warning, operator, critical,
		))
	}

	return nil
}

// EffectiveMode returns the resolved mode, defaulting to scrape.
func (c *PrometheusConfig) EffectiveMode() string {
	if c.Mode == "" {
		return ModeScrape
	}

	return c.Mode
}

// EffectiveOperator returns the resolved operator, defaulting to `>`.
func (c *PrometheusConfig) EffectiveOperator() string {
	if c.Operator == "" {
		return DefaultOperator
	}

	return c.Operator
}

// EffectiveMatch returns the resolved multi-series strategy, defaulting to
// `single`.
func (c *PrometheusConfig) EffectiveMatch() string {
	if c.Match == "" {
		return MatchSingle
	}

	return c.Match
}

// EffectiveOnMissing returns the resolved missing-series behavior, defaulting
// to `down`.
func (c *PrometheusConfig) EffectiveOnMissing() string {
	if c.OnMissing == "" {
		return OnMissingDown
	}

	return c.OnMissing
}

// EffectiveTimeout returns the resolved request timeout.
func (c *PrometheusConfig) EffectiveTimeout() time.Duration {
	if c.Timeout <= 0 {
		return defaultTimeout
	}

	return c.Timeout
}
