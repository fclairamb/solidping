package checkdomain

import (
	"errors"
)

var (
	// ErrInvalidConfigType is returned when the config is not of the correct type.
	ErrInvalidConfigType = errors.New("invalid config type")
	errDomainRequired    = errors.New("domain is required")
)

// DomainConfig defines the configuration for domain expiration checks.
type DomainConfig struct {
	// Domain is the domain name to check (e.g., "google.com").
	Domain string `json:"domain"`

	// ThresholdDays is the number of days before expiration to consider the check failed.
	// Default is 30 days.
	ThresholdDays int `json:"thresholdDays"`
}

// FromMap populates the configuration from a map.
func (c *DomainConfig) FromMap(configMap map[string]any) error {
	if domain, ok := configMap["domain"].(string); ok {
		c.Domain = domain
	}

	if threshold, ok := configMap["threshold_days"].(float64); ok {
		c.ThresholdDays = int(threshold)
	} else if threshold, ok := configMap["threshold_days"].(int); ok {
		c.ThresholdDays = threshold
	}

	return nil
}

// GetConfig returns the configuration as a map.
func (c *DomainConfig) GetConfig() map[string]any {
	return map[string]any{
		"domain":         c.Domain,
		"threshold_days": c.ThresholdDays,
	}
}

// Validate checks if the configuration is valid.
func (c *DomainConfig) Validate() error {
	if c.Domain == "" {
		return errDomainRequired
	}

	return nil
}
