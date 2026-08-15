// Package checkdomain provides domain expiration monitoring via RDAP (with
// a WHOIS fallback/override).
package checkdomain

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/likexian/whois"
	whoisparser "github.com/likexian/whois-parser"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// DefaultThresholdDays is the default threshold for domain expiration.
const DefaultThresholdDays = 30

// errWHOISNoExpirationDate is returned when the WHOIS parser found no
// expiration date in the raw WHOIS reply.
var errWHOISNoExpirationDate = errors.New("could not find expiration date in WHOIS data")

// DomainChecker implements the Checker interface for domain expiration checks.
type DomainChecker struct {
	// rdapClient and whoisFunc are test seams: nil (the production default)
	// uses defaultRDAPClient.lookup / whois.Whois. registry.GetChecker
	// constructs a fresh &DomainChecker{} per call, so these must stay
	// optional overrides rather than required constructor args.
	rdapClient *rdapClient
	whoisFunc  func(domain string) (string, error)
}

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
		return checkerdef.NewConfigError(checkerdef.OutputKeyDomain, err.Error())
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
		Output:   map[string]any{checkerdef.OutputKeyDomain: domain, "error": errMsg},
	}
}

func (c *DomainChecker) lookupRDAP(ctx context.Context, domain string) (*rdapResult, error) {
	client := c.rdapClient
	if client == nil {
		client = defaultRDAPClient
	}

	return client.lookup(ctx, domain)
}

func (c *DomainChecker) lookupWHOIS(domain string) (*whoisparser.WhoisInfo, error) {
	rawFunc := c.whoisFunc
	if rawFunc == nil {
		rawFunc = func(d string) (string, error) { return whois.Whois(d) }
	}

	raw, err := rawFunc(domain)
	if err != nil {
		return nil, fmt.Errorf("WHOIS lookup failed: %w", err)
	}

	parsed, err := whoisparser.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("failed to parse WHOIS data: %w", err)
	}

	if parsed.Domain.ExpirationDate == "" {
		return nil, errWHOISNoExpirationDate
	}

	return &parsed, nil
}

// Execute performs the domain expiration check and returns the result.
func (c *DomainChecker) Execute(ctx context.Context, config checkerdef.Config) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*DomainConfig](config)
	if err != nil {
		return nil, err
	}

	start := time.Now()

	expiryDate, registrar, method, lookupErr := c.lookupExpiration(ctx, cfg)
	duration := time.Since(start)

	if lookupErr != nil {
		return errorResult(cfg.Domain, duration, lookupErr.Error()), nil
	}

	daysRemaining := int(time.Until(expiryDate).Hours() / 24) //nolint:mnd // hours-per-day, not a tunable

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
			"duration_ms":    float64(duration.Microseconds()) / 1000.0, //nolint:mnd // µs -> ms
		},
		Output: map[string]any{
			checkerdef.OutputKeyDomain: cfg.Domain,
			"expiry_date":              expiryDate.Format(time.RFC3339),
			"days_remaining":           daysRemaining,
			"registrar":                registrar,
			"method":                   method,
		},
	}, nil
}

// lookupExpiration resolves the expiration date and registrar for cfg.Domain
// according to cfg.effectiveMethod():
//   - "rdap": RDAP only, no fallback.
//   - "whois": WHOIS only (legacy path), RDAP never invoked.
//   - "auto" (default): RDAP first, WHOIS fallback on ANY RDAP failure.
func (c *DomainChecker) lookupExpiration(
	ctx context.Context, cfg *DomainConfig,
) (time.Time, string, string, error) {
	switch cfg.effectiveMethod() {
	case MethodRDAP:
		res, rdapErr := c.lookupRDAP(ctx, cfg.Domain)
		if rdapErr != nil {
			return time.Time{}, "", "", fmt.Errorf("RDAP lookup failed: %w", rdapErr)
		}

		return res.ExpiryDate, res.Registrar, "rdap", nil

	case MethodWHOIS:
		parsed, whoisErr := c.lookupWHOIS(cfg.Domain)
		if whoisErr != nil {
			return time.Time{}, "", "", whoisErr
		}

		expiry, parseErr := parseWHOISExpiry(parsed.Domain.ExpirationDate)
		if parseErr != nil {
			return time.Time{}, "", "", parseErr
		}

		return expiry, parsed.Registrar.Name, "whois", nil

	default: // MethodAuto
		if res, rdapErr := c.lookupRDAP(ctx, cfg.Domain); rdapErr == nil {
			return res.ExpiryDate, res.Registrar, "rdap", nil
		}

		parsed, whoisErr := c.lookupWHOIS(cfg.Domain)
		if whoisErr != nil {
			return time.Time{}, "", "", whoisErr
		}

		expiry, parseErr := parseWHOISExpiry(parsed.Domain.ExpirationDate)
		if parseErr != nil {
			return time.Time{}, "", "", parseErr
		}

		return expiry, parsed.Registrar.Name, "whois", nil
	}
}

func parseWHOISExpiry(expiryDateStr string) (time.Time, error) {
	expiryDate, err := time.Parse(time.RFC3339, expiryDateStr)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to parse expiration date %q: %w", expiryDateStr, err)
	}

	return expiryDate, nil
}
