package checkrdp

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// Config field defaults and bounds.
const (
	defaultPort      = 3389
	defaultTimeout   = 5 * time.Second
	maxTimeout       = 60 * time.Second
	minPort          = 1
	maxPort          = 65535
	maxThresholdDays = 3650 // 10 years — generous sanity cap, mirrors checkssl
)

// RDPConfig holds the configuration for an RDP pre-auth negotiation check.
//
// The default verdict is service liveness: TCP connect plus a valid X.224
// Connection Confirm. Two optional knobs layer on top: RequireNLA turns a
// silently-disabled NLA policy into a failure, and the SSL-style
// WarningDays/CriticalDays thresholds grade the server certificate's expiry
// when a TLS-based protocol was negotiated (0 = disabled). No credentials
// are used anywhere — the handshake stops before authentication.
type RDPConfig struct {
	// Host is the RDP server hostname or IP (required).
	Host string `json:"host,omitempty"`

	// Port is the TCP port to connect to (default: 3389).
	Port int `json:"port,omitempty"`

	// Timeout is the maximum time for the whole check (default: 5s, <=60s).
	Timeout time.Duration `json:"timeout,omitempty"`

	// RequireNLA marks the check Down unless the server selects an NLA
	// (CredSSP) protocol — HYBRID or HYBRID_EX. Off by default.
	RequireNLA bool `json:"require_nla,omitempty"` //nolint:tagliatelle // API uses snake_case

	// WarningDays marks the check Warning when the server certificate expires
	// in at most this many days (evaluated only when a TLS-based protocol was
	// negotiated). 0 = disabled. Must be >= CriticalDays when both are set.
	WarningDays int `json:"warning_days,omitempty"` //nolint:tagliatelle // API uses snake_case

	// CriticalDays marks the check Down when the server certificate expires
	// in at most this many days. 0 = disabled (an already-expired certificate
	// is always Down).
	CriticalDays int `json:"critical_days,omitempty"` //nolint:tagliatelle // API uses snake_case
}

// FromMap populates the configuration from a map.
func (c *RDPConfig) FromMap(configMap map[string]any) error {
	if host, ok := configMap["host"].(string); ok {
		c.Host = host
	} else if configMap["host"] != nil {
		return checkerdef.NewConfigError("host", "must be a string")
	}

	if port, ok := readIntKey(configMap, "port"); ok {
		c.Port = port
	} else if configMap["port"] != nil {
		return checkerdef.NewConfigError("port", "must be a number")
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

	if requireNLA, ok := configMap["require_nla"].(bool); ok {
		c.RequireNLA = requireNLA
	} else if configMap["require_nla"] != nil {
		return checkerdef.NewConfigError("require_nla", "must be a boolean")
	}

	return c.readThresholds(configMap)
}

// readThresholds reads the optional certificate-expiry threshold fields.
func (c *RDPConfig) readThresholds(configMap map[string]any) error {
	if v, ok := readIntKey(configMap, "warning_days"); ok {
		c.WarningDays = v
	} else if configMap["warning_days"] != nil {
		return checkerdef.NewConfigError("warning_days", "must be a number")
	}

	if v, ok := readIntKey(configMap, "critical_days"); ok {
		c.CriticalDays = v
	} else if configMap["critical_days"] != nil {
		return checkerdef.NewConfigError("critical_days", "must be a number")
	}

	return nil
}

// readIntKey returns the integer value at key, accepting int or float64
// (JSON numbers decode to float64).
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
func (c *RDPConfig) GetConfig() map[string]any {
	cfg := map[string]any{
		checkerdef.OutputKeyHost: c.Host,
	}

	if c.Port != 0 {
		cfg[checkerdef.OutputKeyPort] = c.Port
	}

	if c.Timeout != 0 {
		cfg["timeout"] = c.Timeout.String()
	}

	if c.RequireNLA {
		cfg["require_nla"] = true
	}

	if c.WarningDays != 0 {
		cfg["warning_days"] = c.WarningDays
	}

	if c.CriticalDays != 0 {
		cfg["critical_days"] = c.CriticalDays
	}

	return cfg
}

// Validate performs config-only validation (no network). It also fills in
// defaults for the optional numeric fields so Execute can rely on them.
func (c *RDPConfig) Validate() error {
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

	return c.validateThresholds()
}

// validateThresholds enforces non-negative thresholds, a sanity cap, and the
// warning >= critical ordering when both tiers are explicitly set (same rule
// as checkssl: the warning band sits above the critical band).
func (c *RDPConfig) validateThresholds() error {
	if c.WarningDays < 0 {
		return checkerdef.NewConfigError("warning_days", "must be >= 0")
	}

	if c.CriticalDays < 0 {
		return checkerdef.NewConfigError("critical_days", "must be >= 0")
	}

	if c.WarningDays > maxThresholdDays {
		return checkerdef.NewConfigErrorf("warning_days", "must be <= %d", maxThresholdDays)
	}

	if c.CriticalDays > maxThresholdDays {
		return checkerdef.NewConfigErrorf("critical_days", "must be <= %d", maxThresholdDays)
	}

	if c.WarningDays != 0 && c.CriticalDays != 0 && c.WarningDays < c.CriticalDays {
		return checkerdef.NewConfigErrorf(
			"warning_days", "must be >= critical_days (%d), got %d", c.CriticalDays, c.WarningDays,
		)
	}

	return nil
}
