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

// DomainConfig defines the configuration for domain expiration checks.
type DomainConfig struct {
	// Domain is the domain name to check (e.g., "google.com").
	Domain string `json:"domain"`

	// ThresholdDays is the number of days before expiration to consider the check failed.
	// Default is 30 days.
	ThresholdDays int `json:"thresholdDays"`

	// Method selects the lookup path: "" or "auto" (default) tries RDAP
	// first and falls back to WHOIS on any RDAP failure, "rdap" forces RDAP
	// only (no fallback), "whois" forces the legacy WHOIS-only path.
	Method string `json:"method,omitempty"`
}

// FromMap populates the configuration from a map.
func (c *DomainConfig) FromMap(configMap map[string]any) error {
	if domain, ok := configMap[checkerdef.OutputKeyDomain].(string); ok {
		c.Domain = domain
	}

	if threshold, ok := configMap["threshold_days"].(float64); ok {
		c.ThresholdDays = int(threshold)
	} else if threshold, ok := configMap["threshold_days"].(int); ok {
		c.ThresholdDays = threshold
	}

	if method, ok := configMap["method"].(string); ok {
		c.Method = method
	}

	return nil
}

// GetConfig returns the configuration as a map.
func (c *DomainConfig) GetConfig() map[string]any {
	cfg := map[string]any{
		checkerdef.OutputKeyDomain: c.Domain,
		"threshold_days":           c.ThresholdDays,
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
		return checkerdef.NewConfigError("method", fmt.Sprintf("must be one of %q, %q, %q (or empty)", MethodAuto, MethodRDAP, MethodWHOIS))
	}

	return nil
}

// effectiveMethod returns the resolved lookup method, treating the zero
// value as MethodAuto.
func (c *DomainConfig) effectiveMethod() string {
	if c.Method == "" {
		return MethodAuto
	}

	return c.Method
}
