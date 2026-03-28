// Package checkdomain provides domain expiration monitoring via WHOIS lookups.
package checkdomain

import (
	"context"
	"fmt"
	"time"

	"github.com/likexian/whois"
	whoisparser "github.com/likexian/whois-parser"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// DefaultThresholdDays is the default threshold for domain expiration.
const DefaultThresholdDays = 30

// DomainChecker implements the Checker interface for domain expiration checks.
type DomainChecker struct{}

// Type returns the check type identifier.
func (c *DomainChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeDomain
}

// Validate checks if the configuration is valid.
func (c *DomainChecker) Validate(spec *checkerdef.CheckSpec) error {
	cfg := &DomainConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return checkerdef.NewConfigError("domain", err.Error())
	}

	// Auto-generate name and slug from domain if not provided
	if spec.Name == "" {
		spec.Name = "Domain: " + cfg.Domain
	}

	if spec.Slug == "" {
		spec.Slug = "domain-" + cfg.Domain
	}

	return nil
}

func errorResult(domain string, duration time.Duration, errMsg string) *checkerdef.Result {
	return &checkerdef.Result{
		Status:   checkerdef.StatusDown,
		Duration: duration,
		Output:   map[string]any{"domain": domain, "error": errMsg},
	}
}

// Execute performs the domain expiration check and returns the result.
func (c *DomainChecker) Execute(_ context.Context, config checkerdef.Config) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*DomainConfig](config)
	if err != nil {
		return nil, err
	}

	start := time.Now()

	raw, err := whois.Whois(cfg.Domain)
	duration := time.Since(start)

	if err != nil {
		return errorResult(cfg.Domain, duration, fmt.Sprintf("WHOIS lookup failed: %v", err)), nil
	}

	parsed, err := whoisparser.Parse(raw)
	if err != nil {
		return errorResult(cfg.Domain, duration, fmt.Sprintf("failed to parse WHOIS data: %v", err)), nil
	}

	expiryDateStr := parsed.Domain.ExpirationDate
	if expiryDateStr == "" {
		return errorResult(cfg.Domain, duration, "could not find expiration date in WHOIS data"), nil
	}

	expiryDate, err := time.Parse(time.RFC3339, expiryDateStr)
	if err != nil {
		msg := fmt.Sprintf("failed to parse expiration date %q: %v", expiryDateStr, err)
		return errorResult(cfg.Domain, duration, msg), nil
	}

	daysRemaining := int(time.Until(expiryDate).Hours() / 24)

	threshold := cfg.ThresholdDays
	if threshold <= 0 {
		threshold = DefaultThresholdDays
	}

	status := checkerdef.StatusUp
	if daysRemaining <= threshold {
		status = checkerdef.StatusDown
	}

	return &checkerdef.Result{
		Status:   status,
		Duration: duration,
		Metrics: map[string]any{
			"days_remaining": daysRemaining,
			"duration_ms":    float64(duration.Microseconds()) / 1000.0,
		},
		Output: map[string]any{
			"domain":         cfg.Domain,
			"expiry_date":    expiryDate.Format(time.RFC3339),
			"days_remaining": daysRemaining,
			"registrar":      parsed.Registrar.Name,
		},
	}, nil
}
