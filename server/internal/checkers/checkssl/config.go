package checkssl

import (
	"errors"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

var errHostRequired = errors.New("host is required")

const (
	defaultPort          = 443
	defaultCriticalDays  = 30
	defaultWarningDays   = 30
	defaultThresholdDays = defaultCriticalDays
	maxThresholdDays     = 3650 // 10 years — generous sanity cap
	defaultTimeout       = 10 * time.Second
	maxTimeout           = 60 * time.Second
)

// SSLConfig defines the configuration for SSL certificate checks.
//
// Expiry is graduated across two tiers driven by the MINIMUM days-remaining
// across the whole presented certificate chain:
//   - <= CriticalDays  → StatusDown (paging, as before)
//   - <= WarningDays   → StatusWarning (amber, counts as up, no incident)
//
// The legacy single ThresholdDays maps to CriticalDays for back-compat.
type SSLConfig struct {
	// Host is the target hostname to connect to.
	Host string `json:"host"`

	// Port is the TCP port to connect to (default: 443).
	Port int `json:"port"`

	// WarningDays is the days-before-expiration threshold that marks the check
	// as Warning (amber, non-paging). Defaults to 30, equal to CriticalDays so
	// behaviour is unchanged until widened.
	WarningDays int `json:"warningDays"`

	// CriticalDays is the days-before-expiration threshold that marks the check
	// as Down (paging). Defaults to 30. The legacy thresholdDays aliases here.
	CriticalDays int `json:"criticalDays"`

	// ThresholdDays is the legacy single threshold, kept as an alias of
	// CriticalDays for back-compat with existing rows and dashboards.
	ThresholdDays int `json:"thresholdDays"`

	// Timeout is the maximum time for connection + handshake (default: 10s).
	Timeout time.Duration `json:"timeout,omitempty"`

	// ServerName overrides the SNI server name (defaults to Host).
	ServerName string `json:"serverName"`
}

// FromMap populates the configuration from a map. Accepts both camelCase
// (canonical, matching JSON tags) and snake_case (legacy) for thresholdDays
// and serverName so existing rows keep working.
func (c *SSLConfig) FromMap(configMap map[string]any) error {
	if host, ok := configMap[checkerdef.OutputKeyHost].(string); ok {
		c.Host = host
	}

	if port, ok := configMap["port"].(float64); ok {
		c.Port = int(port)
	} else if port, ok := configMap["port"].(int); ok {
		c.Port = port
	}

	if threshold, ok := readIntKey(configMap, "thresholdDays", "threshold_days"); ok {
		c.ThresholdDays = threshold
	}

	if warning, ok := readIntKey(configMap, "warningDays", "warning_days"); ok {
		c.WarningDays = warning
	}

	if critical, ok := readIntKey(configMap, "criticalDays", "critical_days"); ok {
		c.CriticalDays = critical
	}

	// Legacy alias: when criticalDays is absent but the legacy thresholdDays is
	// present, the threshold drives the (paging) critical tier so existing rows
	// keep behaving exactly as before.
	if c.CriticalDays == 0 && c.ThresholdDays != 0 {
		c.CriticalDays = c.ThresholdDays
	}

	if timeout, ok := configMap["timeout"].(string); ok {
		duration, err := time.ParseDuration(timeout)
		if err != nil {
			return checkerdef.NewConfigError("timeout", "must be a valid duration string")
		}

		c.Timeout = duration
	}

	if serverName, ok := readStringKey(configMap, "serverName", "server_name"); ok {
		c.ServerName = serverName
	}

	return nil
}

// readIntKey returns the first integer value found at any of the supplied keys.
func readIntKey(configMap map[string]any, keys ...string) (int, bool) {
	for _, key := range keys {
		switch v := configMap[key].(type) {
		case float64:
			return int(v), true
		case int:
			return v, true
		}
	}

	return 0, false
}

// readStringKey returns the first non-empty string value found at any of the supplied keys.
func readStringKey(configMap map[string]any, keys ...string) (string, bool) {
	for _, key := range keys {
		if s, ok := configMap[key].(string); ok && s != "" {
			return s, true
		}
	}

	return "", false
}

// GetConfig returns the configuration as a map using the canonical camelCase
// keys that match the JSON tags (and the keys the dash0 form writes).
func (c *SSLConfig) GetConfig() map[string]any {
	config := map[string]any{
		checkerdef.OutputKeyHost: c.Host,
	}

	if c.Port != 0 && c.Port != defaultPort {
		config["port"] = c.Port
	}

	// Canonical critical tier — derive from the legacy threshold when only that
	// was set, so GetConfig always emits the new key.
	critical := c.CriticalDays
	if critical == 0 {
		critical = c.ThresholdDays
	}

	if critical != 0 {
		config["criticalDays"] = critical
		// Keep emitting the legacy key for one release so dashboards that still
		// read thresholdDays keep working.
		config["thresholdDays"] = critical
	}

	if c.WarningDays != 0 {
		config["warningDays"] = c.WarningDays
	}

	if c.Timeout != 0 {
		config["timeout"] = c.Timeout.String()
	}

	if c.ServerName != "" {
		config["serverName"] = c.ServerName
	}

	return config
}

// Validate checks if the configuration is valid.
func (c *SSLConfig) Validate() error {
	if c.Host == "" {
		return errHostRequired
	}

	if c.Port < 0 || c.Port > 65535 {
		return checkerdef.NewConfigError("port", "must be between 1 and 65535")
	}

	if err := c.validateThresholds(); err != nil {
		return err
	}

	if c.Timeout != 0 && (c.Timeout <= 0 || c.Timeout > maxTimeout) {
		return checkerdef.NewConfigErrorf("timeout", "must be > 0 and <= 60s, got %s", c.Timeout.String())
	}

	return nil
}

// validateThresholds enforces warningDays >= criticalDays >= 0 and a sane upper
// bound. It validates the EXPLICIT values (after the legacy alias) so that an
// explicit warningDays < criticalDays is rejected; unset tiers (0) are skipped
// because their defaults are applied at execution time.
func (c *SSLConfig) validateThresholds() error {
	// criticalRaw is the explicit critical tier after applying the legacy alias.
	criticalRaw := c.CriticalDays
	if criticalRaw == 0 {
		criticalRaw = c.ThresholdDays
	}

	if criticalRaw < 0 {
		return checkerdef.NewConfigError("criticalDays", "must be >= 0")
	}

	if c.WarningDays < 0 {
		return checkerdef.NewConfigError("warningDays", "must be >= 0")
	}

	if criticalRaw > maxThresholdDays {
		return checkerdef.NewConfigErrorf("criticalDays", "must be <= %d", maxThresholdDays)
	}

	if c.WarningDays > maxThresholdDays {
		return checkerdef.NewConfigErrorf("warningDays", "must be <= %d", maxThresholdDays)
	}

	// Only enforce the ordering when both tiers are explicitly set; a default
	// (0) warning is clamped up to critical at execution time, never below it.
	if c.WarningDays != 0 && criticalRaw != 0 && c.WarningDays < criticalRaw {
		return checkerdef.NewConfigErrorf(
			"warningDays", "must be >= criticalDays (%d), got %d", criticalRaw, c.WarningDays,
		)
	}

	return nil
}

// effectiveThresholds resolves the warning/critical days that will actually be
// used, applying the legacy alias and per-tier defaults. Used by execution.
func (c *SSLConfig) effectiveThresholds() (warning, critical int) {
	critical = c.CriticalDays
	if critical == 0 {
		critical = c.ThresholdDays
	}

	if critical <= 0 {
		critical = defaultCriticalDays
	}

	warning = c.WarningDays
	if warning <= 0 {
		warning = defaultWarningDays
	}

	// Default warning never weakens an explicitly-widened critical tier.
	if warning < critical {
		warning = critical
	}

	return warning, critical
}
