// Package config holds the health check's configuration: the struct, its map
// parsing and serialization and the offline rule set (ValidateSpec). Like the
// other check types it is free of the execution client, so `sp checks validate`
// can run it without linking a protocol driver.
package config

import (
	"fmt"
	"slices"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkhealth/formats"
	httpconfig "github.com/fclairamb/solidping/server/internal/checkers/checkhttp/config"
)

const (
	// DefaultMaxAge is how old a result timestamp may be before the results are
	// stale (the usual rule for this kind of endpoint).
	DefaultMaxAge = 10 * time.Minute

	// MaxIgnored caps the `ignore` list.
	MaxIgnored = 50

	// MaxComponentOverrides caps the `components` map.
	MaxComponentOverrides = 100

	// OnFailedDown (the default) lets a failed component bring the check down.
	OnFailedDown = "down"
	// OnFailedWarning downgrades a failed component to a warning.
	OnFailedWarning = "warning"

	keyFormat     = "format"
	keyMaxAge     = "maxAge"
	keyMaxAgeSnk  = "max_age"
	keyIgnore     = "ignore"
	keyComponents = "components"
	keyOnFailed   = "onFailed"
	keyOnFailSnk  = "on_failed"
)

// ComponentOverride is the per-component downgrade.
type ComponentOverride struct {
	// OnFailed is "down" (default) or "warning".
	OnFailed string `json:"onFailed,omitempty"`
}

// HealthConfig is the configuration of a health check. It embeds the http
// check's request configuration, so every request option (URL, method,
// headers, secretHeaders, basic auth, TLS, redirects, IP version, tunnel) is
// the http one.
type HealthConfig struct {
	httpconfig.HTTPConfig

	// Format is one of formats.Names(); "" means auto.
	Format string `json:"format,omitempty"`
	// MaxAge is a duration string; "" means DefaultMaxAge, "0" disables the
	// staleness rule.
	MaxAge string `json:"maxAge,omitempty"`
	// Ignore lists component names left out of the verdict.
	Ignore []string `json:"ignore,omitempty"`
	// Components holds per-component overrides keyed by component name.
	Components map[string]ComponentOverride `json:"components,omitempty"`
}

// EffectiveFormat returns the format with the auto default applied.
func (c *HealthConfig) EffectiveFormat() string {
	if c.Format == "" {
		return formats.NameAuto
	}

	return c.Format
}

// EffectiveMaxAge returns the staleness window; 0 means the rule is disabled.
func (c *HealthConfig) EffectiveMaxAge() time.Duration {
	if c.MaxAge == "" {
		return DefaultMaxAge
	}

	parsed, err := time.ParseDuration(c.MaxAge)
	if err != nil || parsed < 0 {
		return DefaultMaxAge
	}

	return parsed
}

// IsIgnored reports whether a component is ignored.
func (c *HealthConfig) IsIgnored(name string) bool {
	return slices.Contains(c.Ignore, name)
}

// OnFailed returns what a failed component does to the check.
func (c *HealthConfig) OnFailed(name string) string {
	if override, ok := c.Components[name]; ok && override.OnFailed != "" {
		return override.OnFailed
	}

	return OnFailedDown
}

// FromMap populates the configuration from a map.
func (c *HealthConfig) FromMap(configMap map[string]any) error {
	if err := c.HTTPConfig.FromMap(configMap); err != nil {
		return err
	}

	if raw, ok := configMap[keyFormat]; ok && raw != nil {
		text, isString := raw.(string)
		if !isString {
			return checkerdef.NewConfigError(keyFormat, "must be a string")
		}

		c.Format = text
	}

	if err := c.readMaxAge(configMap); err != nil {
		return err
	}

	ignore, err := readStrings(configMap, keyIgnore)
	if err != nil {
		return err
	}

	c.Ignore = ignore

	return c.readComponents(configMap)
}

func (c *HealthConfig) readMaxAge(configMap map[string]any) error {
	for _, key := range []string{keyMaxAge, keyMaxAgeSnk} {
		raw, ok := configMap[key]
		if !ok || raw == nil {
			continue
		}

		switch typed := raw.(type) {
		case string:
			c.MaxAge = typed
		case time.Duration:
			c.MaxAge = typed.String()
		default:
			return checkerdef.NewConfigError(key, "must be a duration string such as 10m")
		}

		return nil
	}

	return nil
}

func (c *HealthConfig) readComponents(configMap map[string]any) error {
	raw, ok := configMap[keyComponents]
	if !ok || raw == nil {
		return nil
	}

	c.Components = make(map[string]ComponentOverride)

	switch typed := raw.(type) {
	case map[string]ComponentOverride:
		for name, override := range typed {
			c.Components[name] = override
		}
	case map[string]any:
		for name, item := range typed {
			object, isMap := item.(map[string]any)
			if !isMap {
				return checkerdef.NewConfigErrorf(keyComponents, "%s must be an object", name)
			}

			override := ComponentOverride{}

			for _, key := range []string{keyOnFailed, keyOnFailSnk} {
				value, present := object[key]
				if !present || value == nil {
					continue
				}

				text, isString := value.(string)
				if !isString {
					return checkerdef.NewConfigErrorf(keyComponents, "%s.%s must be a string", name, key)
				}

				override.OnFailed = text

				break
			}

			c.Components[name] = override
		}
	default:
		return checkerdef.NewConfigError(keyComponents, "must be an object")
	}

	return nil
}

// GetConfig returns the configuration as a map.
func (c *HealthConfig) GetConfig() map[string]any {
	cfg := c.HTTPConfig.GetConfig()

	if c.Format != "" {
		cfg[keyFormat] = c.Format
	}

	if c.MaxAge != "" {
		cfg[keyMaxAge] = c.MaxAge
	}

	if len(c.Ignore) > 0 {
		cfg[keyIgnore] = toAnySlice(c.Ignore)
	}

	if len(c.Components) > 0 {
		components := make(map[string]any, len(c.Components))

		for name, override := range c.Components {
			entry := map[string]any{}
			if override.OnFailed != "" {
				entry[keyOnFailed] = override.OnFailed
			}

			components[name] = entry
		}

		cfg[keyComponents] = components
	}

	return cfg
}

// Validate applies the health-specific rules.
func (c *HealthConfig) Validate() error {
	if !slices.Contains(formats.Names(), c.EffectiveFormat()) {
		return checkerdef.NewConfigErrorf(keyFormat, "must be one of %v, got %q", formats.Names(), c.Format)
	}

	if c.MaxAge != "" {
		parsed, err := time.ParseDuration(c.MaxAge)
		if err != nil {
			return checkerdef.NewConfigError(keyMaxAge, "must be a valid duration such as 10m (0 disables)")
		}

		if parsed < 0 {
			return checkerdef.NewConfigError(keyMaxAge, "must not be negative")
		}
	}

	if len(c.Ignore) > MaxIgnored {
		return checkerdef.NewConfigErrorf(keyIgnore, "at most %d components can be ignored", MaxIgnored)
	}

	for _, name := range c.Ignore {
		if name == "" {
			return checkerdef.NewConfigError(keyIgnore, "component names must not be empty")
		}
	}

	if len(c.Components) > MaxComponentOverrides {
		return checkerdef.NewConfigErrorf(keyComponents, "at most %d overrides are allowed", MaxComponentOverrides)
	}

	for name, override := range c.Components {
		if name == "" {
			return checkerdef.NewConfigError(keyComponents, "component names must not be empty")
		}

		switch override.OnFailed {
		case "", OnFailedDown, OnFailedWarning:
		default:
			return checkerdef.NewConfigErrorf(keyComponents,
				"%s: onFailed must be %q or %q, got %q", name, OnFailedDown, OnFailedWarning, override.OnFailed)
		}
	}

	return nil
}

// SchemaNotes implements checkerdef.SchemaNoter.
func (c *HealthConfig) SchemaNotes() []string {
	return []string{
		"The request options are the http check's. Its body assertions (body_expect, body_reject, " +
			"body_pattern, body_pattern_reject, json_path_assertions, bodyAssertions) and status " +
			"expectations (expected_status, expected_status_codes) are refused: the health document is " +
			"the only thing that judges the response.",
		fmt.Sprintf("format is one of %v.", formats.Names()),
		"maxAge is a duration string (default 10m); 0 disables the staleness rule. " +
			"snake_case aliases (max_age, on_failed) are accepted.",
	}
}

func readStrings(configMap map[string]any, key string) ([]string, error) {
	switch value := configMap[key].(type) {
	case nil:
		return nil, nil
	case []string:
		return slices.Clone(value), nil
	case []any:
		out := make([]string, 0, len(value))

		for _, item := range value {
			text, ok := item.(string)
			if !ok {
				return nil, checkerdef.NewConfigError(key, "must be a list of strings")
			}

			out = append(out, text)
		}

		return out, nil
	default:
		return nil, checkerdef.NewConfigError(key, "must be a list of strings")
	}
}

func toAnySlice(values []string) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}

	return out
}
