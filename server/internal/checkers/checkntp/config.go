package checkntp

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// Config field defaults and bounds.
const (
	defaultPort    = 123
	defaultTimeout = 5 * time.Second
	maxTimeout     = 60 * time.Second
	defaultVersion = 4
	minVersion     = 3
	maxVersion     = 4
	minPort        = 1
	maxPort        = 65535
	// maxStratumCap is the highest stratum the upstream library considers
	// valid; stratum 16 already means "unsynchronized" (rejected by
	// Validate()), so a MaxStratum threshold never needs to exceed 15.
	maxStratumCap = 15
)

// NTPConfig holds the configuration for an NTP time-source check.
//
// The default verdict is reachability plus the server's own self-reported
// health (Response.Validate()). The offset and stratum thresholds are
// SSL-style, opt-in tiers (0 = disabled): they let a user additionally page on
// a server whose clock is too far from the worker's, or whose stratum is too
// deep, on top of the default health signal.
type NTPConfig struct {
	// Host is the NTP server hostname or IP (required).
	Host string `json:"host,omitempty"`

	// Port is the UDP port to query (default: 123).
	Port int `json:"port,omitempty"`

	// Timeout is the maximum time to wait for a response (default: 5s, <=60s).
	Timeout time.Duration `json:"timeout,omitempty"`

	// Version is the NTP protocol version to use (default: 4, one of {3,4}).
	Version int `json:"version,omitempty"`

	// OffsetWarnMs marks the check Warning when the absolute clock offset
	// (ms, measured relative to the worker clock) exceeds it. 0 = disabled.
	OffsetWarnMs int `json:"offset_warn_ms,omitempty"` //nolint:tagliatelle // API uses snake_case

	// OffsetCritMs marks the check Down when the absolute clock offset (ms)
	// exceeds it. 0 = disabled. Must be >= OffsetWarnMs when both are set.
	OffsetCritMs int `json:"offset_crit_ms,omitempty"` //nolint:tagliatelle // API uses snake_case

	// MaxStratum marks the check Down when the server's stratum exceeds it.
	// 0 = disabled (the default health check already rejects stratum 0/16).
	MaxStratum int `json:"max_stratum,omitempty"` //nolint:tagliatelle // API uses snake_case
}

// FromMap populates the configuration from a map.
func (c *NTPConfig) FromMap(configMap map[string]any) error {
	if host, ok := configMap["host"].(string); ok {
		c.Host = host
	} else if configMap["host"] != nil {
		return checkerdef.NewConfigError("host", "must be a string")
	}

	if err := c.readPortAndVersion(configMap); err != nil {
		return err
	}

	if timeout, ok := configMap["timeout"].(string); ok {
		duration, err := time.ParseDuration(timeout)
		if err != nil {
			return checkerdef.NewConfigError("timeout", "must be a valid duration string")
		}

		c.Timeout = duration
	} else if configMap["timeout"] != nil {
		return checkerdef.NewConfigError("timeout", "must be a string")
	}

	return c.readThresholds(configMap)
}

// readPortAndVersion reads the integer port/version fields, accepting both int
// and float64 (JSON numbers decode to float64).
func (c *NTPConfig) readPortAndVersion(configMap map[string]any) error {
	if port, ok := readIntKey(configMap, "port"); ok {
		c.Port = port
	} else if configMap["port"] != nil {
		return checkerdef.NewConfigError("port", "must be a number")
	}

	if version, ok := readIntKey(configMap, "version"); ok {
		c.Version = version
	} else if configMap["version"] != nil {
		return checkerdef.NewConfigError("version", "must be a number")
	}

	return nil
}

// readThresholds reads the optional offset/stratum threshold fields.
func (c *NTPConfig) readThresholds(configMap map[string]any) error {
	if v, ok := readIntKey(configMap, "offset_warn_ms"); ok {
		c.OffsetWarnMs = v
	} else if configMap["offset_warn_ms"] != nil {
		return checkerdef.NewConfigError("offset_warn_ms", "must be a number")
	}

	if v, ok := readIntKey(configMap, "offset_crit_ms"); ok {
		c.OffsetCritMs = v
	} else if configMap["offset_crit_ms"] != nil {
		return checkerdef.NewConfigError("offset_crit_ms", "must be a number")
	}

	if v, ok := readIntKey(configMap, "max_stratum"); ok {
		c.MaxStratum = v
	} else if configMap["max_stratum"] != nil {
		return checkerdef.NewConfigError("max_stratum", "must be a number")
	}

	return nil
}

// readIntKey returns the integer value at key, accepting int or float64.
func readIntKey(configMap map[string]any, key string) (int, bool) {
	switch v := configMap[key].(type) {
	case int:
		return v, true
	case float64:
		return int(v), true
	default:
		return 0, false
	}
}

// GetConfig returns the configuration as a map.
func (c *NTPConfig) GetConfig() map[string]any {
	cfg := map[string]any{
		"host": c.Host,
	}

	if c.Port != 0 {
		cfg["port"] = c.Port
	}

	if c.Timeout != 0 {
		cfg["timeout"] = c.Timeout.String()
	}

	if c.Version != 0 {
		cfg["version"] = c.Version
	}

	if c.OffsetWarnMs != 0 {
		cfg["offset_warn_ms"] = c.OffsetWarnMs
	}

	if c.OffsetCritMs != 0 {
		cfg["offset_crit_ms"] = c.OffsetCritMs
	}

	if c.MaxStratum != 0 {
		cfg["max_stratum"] = c.MaxStratum
	}

	return cfg
}

// Validate performs config-only validation (no network). It also fills in
// defaults for the optional numeric fields so Execute can rely on them.
func (c *NTPConfig) Validate() error {
	if c.Host == "" {
		return checkerdef.NewConfigError("host", "is required")
	}

	if c.Port == 0 {
		c.Port = defaultPort
	}

	if c.Port < minPort || c.Port > maxPort {
		return checkerdef.NewConfigErrorf("port", "must be between %d and %d, got %d", minPort, maxPort, c.Port)
	}

	if c.Timeout != 0 && (c.Timeout <= 0 || c.Timeout > maxTimeout) {
		return checkerdef.NewConfigErrorf("timeout", "must be > 0 and <= 60s, got %s", c.Timeout.String())
	}

	if c.Version == 0 {
		c.Version = defaultVersion
	}

	if c.Version < minVersion || c.Version > maxVersion {
		return checkerdef.NewConfigErrorf("version", "must be %d or %d, got %d", minVersion, maxVersion, c.Version)
	}

	return c.validateThresholds()
}

// validateThresholds enforces non-negative thresholds and warn <= crit ordering.
func (c *NTPConfig) validateThresholds() error {
	if c.OffsetWarnMs < 0 {
		return checkerdef.NewConfigError("offset_warn_ms", "must be >= 0")
	}

	if c.OffsetCritMs < 0 {
		return checkerdef.NewConfigError("offset_crit_ms", "must be >= 0")
	}

	if c.OffsetWarnMs != 0 && c.OffsetCritMs != 0 && c.OffsetWarnMs > c.OffsetCritMs {
		return checkerdef.NewConfigErrorf(
			"offset_warn_ms", "must be <= offset_crit_ms (%d), got %d", c.OffsetCritMs, c.OffsetWarnMs,
		)
	}

	if c.MaxStratum < 0 {
		return checkerdef.NewConfigError("max_stratum", "must be >= 0")
	}

	if c.MaxStratum > maxStratumCap {
		return checkerdef.NewConfigErrorf("max_stratum", "must be <= %d, got %d", maxStratumCap, c.MaxStratum)
	}

	return nil
}
