// Package config holds the dns check's configuration: the struct, its
// map parsing and serialization, its key constants and the whole offline rule
// set (ValidateSpec).
//
// It is deliberately free of the execution client the parent checkdns package
// links, so `sp checks validate` can run the server's own validators against a
// config-as-code manifest without carrying a protocol driver. The parent keeps a
// type alias, so every existing call site is unaffected.
package config

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/urlparse"
)

// DNSConfig holds the configuration for DNS checks.
type DNSConfig struct {
	URL            string        `json:"url,omitempty"`
	Host           string        `json:"host,omitempty"` // Domain to query (renamed from Hostname)
	Timeout        time.Duration `json:"timeout,omitempty"`
	Nameserver     string        `json:"nameserver,omitempty"`
	RecordType     string        `json:"record_type,omitempty"`     //nolint:tagliatelle // API uses snake_case
	ExpectedIPs    []string      `json:"expected_ips,omitempty"`    //nolint:tagliatelle // API uses snake_case
	ExpectedValues []string      `json:"expected_values,omitempty"` //nolint:tagliatelle // API uses snake_case

	// DetectChanges turns on baseline change detection (spec 2026-10-03-04):
	// the first successful run of each region captures its answer into
	// Baseline, every later run compares the full answer set with it.
	DetectChanges bool `json:"detect_changes,omitempty"` //nolint:tagliatelle // API uses snake_case
	// Baseline maps a region (BaselineKey) to its normalized answer values.
	// Filled by the server on capture, editable (and resettable) by the user.
	Baseline map[string][]string `json:"baseline,omitempty"`
	// OnChange is the status a difference from the baseline produces:
	// OnChangeDown (default) or OnChangeWarning.
	OnChange string `json:"on_change,omitempty"` //nolint:tagliatelle // API uses snake_case

	// region is the job's region, set by SelectRegion before Execute.
	region string
}

const (
	// OnChangeDown reports a baseline difference as down (the default).
	OnChangeDown = "down"
	// OnChangeWarning reports a baseline difference as warning (amber, counts
	// as up, opens no incident).
	OnChangeWarning = "warning"

	// MaxBaselineValues caps the values stored for one region.
	MaxBaselineValues = 100

	// DefaultBaselineKey is the baseline key of a run without a region.
	DefaultBaselineKey = "default"

	keyDetectChanges = "detect_changes"
	keyBaseline      = "baseline"
	keyOnChange      = "on_change"
)

// BaselineKey is the Baseline map key for a region: the region itself, or
// DefaultBaselineKey for a region-less run. The worker-side selection and the
// server-side capture both go through it, so they always agree.
func BaselineKey(region string) string {
	if region == "" {
		return DefaultBaselineKey
	}

	return region
}

// SelectRegion implements checkerdef.RegionSelector: the worker hands the
// job's region over before Execute, so the checker compares against that
// region's baseline only (GeoDNS answers differ by region by design).
func (c *DNSConfig) SelectRegion(region string) {
	c.region = region
}

// Region returns the region selected by SelectRegion (empty when none).
func (c *DNSConfig) Region() string {
	return c.region
}

// RegionBaseline returns the selected region's baseline and whether one is
// stored. An empty slice counts as absent: a capture never stores one.
func (c *DNSConfig) RegionBaseline() ([]string, bool) {
	values := c.Baseline[BaselineKey(c.region)]

	return values, len(values) > 0
}

// EffectiveOnChange returns OnChange with its default applied.
func (c *DNSConfig) EffectiveOnChange() string {
	if c.OnChange == "" {
		return OnChangeDown
	}

	return c.OnChange
}

// PreserveAbsentFields implements checkerdef.AbsentFieldPreserver: a config
// update that omits `baseline` keeps the stored one, so a PATCH or an
// `sp apply` of a manifest exported before the capture never resets it. An
// explicit `baseline: {}` clears it. Only while detection stays on: turning
// it off drops the baseline with it.
func (c *DNSConfig) PreserveAbsentFields(stored, merged map[string]any) {
	if _, present := merged[keyBaseline]; present {
		return
	}

	if on, _ := merged[keyDetectChanges].(bool); !on {
		return
	}

	if baseline, ok := stored[keyBaseline]; ok && baseline != nil {
		merged[keyBaseline] = baseline
	}
}

// FromMap populates the configuration from a map.
//
//nolint:cyclop,gocognit,nestif // Configuration parsing requires checking multiple field types
func (c *DNSConfig) FromMap(configMap map[string]any) error {
	// URL takes precedence if provided
	// Format: dns://resolver/domain?type=A
	if urlStr, ok := configMap["url"].(string); ok && urlStr != "" {
		c.URL = urlStr
		parsed, err := urlparse.Parse(urlStr)
		if err != nil {
			return checkerdef.NewConfigError("url", err.Error())
		}
		if parsed.CheckType != checkerdef.CheckTypeDNS {
			return checkerdef.NewConfigError("url", "must be a DNS URL (dns://)")
		}
		// Domain to query comes from the path
		c.Host = parsed.DNSDomain
		c.RecordType = parsed.RecordType
		// Resolver comes from the host (empty = system resolver)
		if parsed.Resolver() != "" {
			c.Nameserver = parsed.Resolver()
		}
	} else {
		// Fall back to legacy host/hostname
		if host, ok := configMap[checkerdef.OutputKeyHost].(string); ok {
			c.Host = host
		} else if hostname, ok := configMap["hostname"].(string); ok {
			// Backward compatibility with old field name
			c.Host = hostname
		} else if configMap[checkerdef.OutputKeyHost] != nil {
			return checkerdef.NewConfigError(checkerdef.OutputKeyHost, "must be a string")
		} else if configMap["hostname"] != nil {
			return checkerdef.NewConfigError("hostname", "must be a string")
		}

		// Extract Nameserver (optional) - only in legacy mode
		if nameserver, ok := configMap["nameserver"].(string); ok {
			c.Nameserver = nameserver
		} else if configMap["nameserver"] != nil {
			return checkerdef.NewConfigError("nameserver", "must be a string")
		}

		// Extract RecordType (optional) - only in legacy mode
		if recordType, ok := configMap["record_type"].(string); ok {
			c.RecordType = recordType
		} else if configMap["record_type"] != nil {
			return checkerdef.NewConfigError("record_type", "must be a string")
		}
	}

	// Extract Timeout (optional, duration string)
	if timeout, ok := configMap["timeout"].(string); ok {
		duration, err := time.ParseDuration(timeout)
		if err != nil {
			return checkerdef.NewConfigError("timeout", "must be a valid duration string")
		}

		c.Timeout = duration
	} else if configMap["timeout"] != nil {
		return checkerdef.NewConfigError("timeout", "must be a string")
	}

	// Extract ExpectedIPs (optional, array of strings)
	if expectedIPs, ok := configMap["expected_ips"].([]string); ok {
		c.ExpectedIPs = expectedIPs
	} else if expectedIPsAny, ok := configMap["expected_ips"].([]any); ok {
		// Handle []any and convert to []string
		c.ExpectedIPs = make([]string, len(expectedIPsAny))
		for i, v := range expectedIPsAny {
			if strVal, ok := v.(string); ok {
				c.ExpectedIPs[i] = strVal
			} else {
				return checkerdef.NewConfigError("expected_ips", "must be a string array")
			}
		}
	} else if configMap["expected_ips"] != nil {
		return checkerdef.NewConfigError("expected_ips", "must be a string array")
	}

	// Extract ExpectedValues (optional, array of strings)
	if expectedValues, ok := configMap["expected_values"].([]string); ok {
		c.ExpectedValues = expectedValues
	} else if expectedValuesAny, ok := configMap["expected_values"].([]any); ok {
		// Handle []any and convert to []string
		c.ExpectedValues = make([]string, len(expectedValuesAny))
		for i, v := range expectedValuesAny {
			if strVal, ok := v.(string); ok {
				c.ExpectedValues[i] = strVal
			} else {
				return checkerdef.NewConfigError("expected_values", "must be a string array")
			}
		}
	} else if configMap["expected_values"] != nil {
		return checkerdef.NewConfigError("expected_values", "must be a string array")
	}

	return c.parseChangeDetection(configMap)
}

// parseChangeDetection reads detect_changes, baseline and on_change.
func (c *DNSConfig) parseChangeDetection(configMap map[string]any) error {
	if raw, present := configMap[keyDetectChanges]; present && raw != nil {
		on, ok := raw.(bool)
		if !ok {
			return checkerdef.NewConfigError(keyDetectChanges, "must be a boolean")
		}

		c.DetectChanges = on
	}

	if raw, present := configMap[keyOnChange]; present && raw != nil {
		onChange, ok := raw.(string)
		if !ok {
			return checkerdef.NewConfigError(keyOnChange, "must be a string")
		}

		c.OnChange = onChange
	}

	if raw := configMap[keyBaseline]; raw != nil {
		baseline, err := parseBaseline(raw)
		if err != nil {
			return err
		}

		c.Baseline = baseline
	}

	return nil
}

// parseBaseline reads a region → values map from its JSON-decoded form
// (map[string]any of []any) or its typed form.
func parseBaseline(raw any) (map[string][]string, error) {
	switch typed := raw.(type) {
	case map[string][]string:
		return typed, nil
	case map[string]any:
		baseline := make(map[string][]string, len(typed))

		for region, rawValues := range typed {
			values, err := toStringSlice(rawValues)
			if err != nil {
				return nil, checkerdef.NewConfigErrorf(keyBaseline, "region %q: must be a string array", region)
			}

			baseline[region] = values
		}

		return baseline, nil
	default:
		return nil, checkerdef.NewConfigError(keyBaseline, "must be an object of region to string array")
	}
}

func toStringSlice(raw any) ([]string, error) {
	switch typed := raw.(type) {
	case []string:
		return typed, nil
	case []any:
		values := make([]string, len(typed))

		for i, v := range typed {
			str, ok := v.(string)
			if !ok {
				return nil, checkerdef.NewConfigError(keyBaseline, "must be a string array")
			}

			values[i] = str
		}

		return values, nil
	case nil:
		return []string{}, nil
	default:
		return nil, checkerdef.NewConfigError(keyBaseline, "must be a string array")
	}
}

// GetConfig implements the GetConfig interface by returning the configuration as a map.
func (c *DNSConfig) GetConfig() map[string]any {
	cfg := map[string]any{
		checkerdef.OutputKeyHost: c.Host,
	}

	if c.URL != "" {
		cfg["url"] = c.URL
	}

	if c.Timeout != 0 {
		cfg["timeout"] = c.Timeout.String()
	}

	if c.Nameserver != "" {
		cfg["nameserver"] = c.Nameserver
	}

	if c.RecordType != "" {
		cfg["record_type"] = c.RecordType
	}

	if len(c.ExpectedIPs) > 0 {
		cfg["expected_ips"] = c.ExpectedIPs
	}

	if len(c.ExpectedValues) > 0 {
		cfg["expected_values"] = c.ExpectedValues
	}

	if c.DetectChanges {
		cfg[keyDetectChanges] = true
	}

	if len(c.Baseline) > 0 {
		cfg[keyBaseline] = c.Baseline
	}

	if c.OnChange != "" && c.OnChange != OnChangeDown {
		cfg[keyOnChange] = c.OnChange
	}

	return cfg
}
