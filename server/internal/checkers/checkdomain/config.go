package checkdomain

import (
	"errors"
	"fmt"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

var errDomainRequired = errors.New("domain is required")

// Lookup method values for DomainConfig.Method. MethodAuto is also the
// zero-value behavior, so existing checks with no "method" key upgrade to
// RDAP-first transparently.
const (
	MethodAuto  = "auto"
	MethodRDAP  = "rdap"
	MethodWHOIS = "whois"
)

const (
	// DefaultThresholdDays is the default threshold for domain expiration
	// (both warning and critical, kept equal so behavior is unchanged until
	// a caller explicitly widens the warning tier).
	DefaultThresholdDays = 30
	defaultCriticalDays  = DefaultThresholdDays
	defaultWarningDays   = DefaultThresholdDays
	maxThresholdDays     = 3650 // 10 years — generous sanity cap, mirrors checkssl
)

// DomainConfig defines the configuration for domain expiration checks.
//
// Expiry is graduated across two tiers, exactly like checkssl, driven by the
// shared checkerdef.GradedExpiryStatus:
//   - <= CriticalDays → StatusDown (paging)
//   - <= WarningDays  → StatusWarning (amber, counts as up, no incident)
//
// The legacy single ThresholdDays maps to CriticalDays for back-compat.
type DomainConfig struct {
	// Domain is the domain name to check (e.g., "google.com").
	Domain string `json:"domain"`

	// WarningDays is the days-before-expiration threshold that marks the
	// check as Warning (amber, non-paging). Defaults to 30, equal to
	// CriticalDays so behavior is unchanged until widened.
	WarningDays int `json:"warningDays"`

	// CriticalDays is the days-before-expiration threshold that marks the
	// check as Down (paging). Defaults to 30. The legacy thresholdDays
	// aliases here.
	CriticalDays int `json:"criticalDays"`

	// ThresholdDays is the legacy single threshold, kept as an alias of
	// CriticalDays for back-compat with existing rows and dashboards.
	ThresholdDays int `json:"thresholdDays"`

	// Method selects the lookup path: "" or "auto" (default) tries RDAP
	// first and falls back to WHOIS on any RDAP failure, "rdap" forces RDAP
	// only (no fallback), "whois" forces the legacy WHOIS-only path.
	Method string `json:"method,omitempty"`
}

// FromMap populates the configuration from a map. Accepts both camelCase
// (canonical) and snake_case (legacy) for thresholdDays so existing rows
// (stored as "threshold_days") keep working.
func (c *DomainConfig) FromMap(configMap map[string]any) error {
	if domain, ok := configMap[checkerdef.OutputKeyDomain].(string); ok {
		c.Domain = domain
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

	// Legacy alias: when criticalDays is absent but the legacy thresholdDays
	// is present, the threshold drives the (paging) critical tier so
	// existing rows keep behaving exactly as before.
	if c.CriticalDays == 0 && c.ThresholdDays != 0 {
		c.CriticalDays = c.ThresholdDays
	}

	if method, ok := configMap["method"].(string); ok {
		c.Method = method
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

// GetConfig returns the configuration as a map.
func (c *DomainConfig) GetConfig() map[string]any {
	cfg := map[string]any{
		checkerdef.OutputKeyDomain: c.Domain,
	}

	// Canonical critical tier — derive from the legacy threshold when only
	// that was set, so GetConfig always emits the new key.
	critical := c.CriticalDays
	if critical == 0 {
		critical = c.ThresholdDays
	}

	// threshold_days is always emitted (even when zero) for back-compat with
	// existing readers of the exported config shape.
	cfg["threshold_days"] = critical

	if critical != 0 {
		cfg["criticalDays"] = critical
	}

	if c.WarningDays != 0 {
		cfg["warningDays"] = c.WarningDays
	}

	if c.Method != "" {
		cfg["method"] = c.Method
	}

	return cfg
}

// Validate checks if the configuration is valid.
func (c *DomainConfig) Validate() error {
	if c.Domain == "" {
		return errDomainRequired
	}

	switch c.Method {
	case "", MethodAuto, MethodRDAP, MethodWHOIS:
	default:
		msg := fmt.Sprintf("must be one of %q, %q, %q (or empty)", MethodAuto, MethodRDAP, MethodWHOIS)

		return checkerdef.NewConfigError("method", msg)
	}

	if err := c.validateThresholds(); err != nil {
		return err
	}

	return nil
}

// validateThresholds enforces warningDays >= criticalDays >= 0 and a sane
// upper bound, mirroring checkssl.validateThresholds. It validates the
// EXPLICIT values (after the legacy alias) so that an explicit
// warningDays < criticalDays is rejected; unset tiers (0) are skipped
// because their defaults are applied at execution time.
func (c *DomainConfig) validateThresholds() error {
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

	// Only enforce the ordering when both tiers are explicitly set; a
	// default (0) warning is clamped up to critical at execution time,
	// never below it.
	if c.WarningDays != 0 && criticalRaw != 0 && c.WarningDays < criticalRaw {
		return checkerdef.NewConfigErrorf(
			"warningDays", "must be >= criticalDays (%d), got %d", criticalRaw, c.WarningDays,
		)
	}

	return nil
}

// effectiveThresholds resolves the warning/critical days that will actually
// be used, applying the legacy alias and per-tier defaults. Used by
// execution. Returns (warning, critical).
func (c *DomainConfig) effectiveThresholds() (int, int) {
	critical := c.CriticalDays
	if critical == 0 {
		critical = c.ThresholdDays
	}

	if critical <= 0 {
		critical = defaultCriticalDays
	}

	warning := c.WarningDays
	if warning <= 0 {
		warning = defaultWarningDays
	}

	// Default warning never weakens an explicitly-widened critical tier.
	if warning < critical {
		warning = critical
	}

	return warning, critical
}

// effectiveMethod returns the resolved lookup method, treating the zero
// value as MethodAuto.
func (c *DomainConfig) effectiveMethod() string {
	if c.Method == "" {
		return MethodAuto
	}

	return c.Method
}
