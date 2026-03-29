// Package checkdns provides DNS resolution monitoring checks.
package checkdns

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	// Default values from spec.
	defaultTimeout    = 5 * time.Second
	defaultRecordType = "A"
)

var (
	//nolint:gochecknoglobals // validRecordTypes is a constant lookup map
	validRecordTypes = map[string]bool{
		"A":     true,
		"AAAA":  true,
		"CNAME": true,
		"MX":    true,
		"NS":    true,
		"TXT":   true,
		"SOA":   true,
	}
	errSOANotSupported = errors.New("SOA record type not yet supported")
)

// DNSChecker implements the Checker interface for DNS checks.
type DNSChecker struct{}

// Type returns the check type identifier.
func (c *DNSChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeDNS
}

// Validate checks if the configuration is valid.
func (c *DNSChecker) Validate(spec *checkerdef.CheckSpec) error {
	cfg := &DNSConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	// Validate Host (domain to query)
	if cfg.Host == "" {
		return checkerdef.NewConfigError("host", "is required")
	}

	// Validate Timeout if set
	if cfg.Timeout != 0 && (cfg.Timeout <= 0 || cfg.Timeout > 60*time.Second) {
		return checkerdef.NewConfigErrorf("timeout", "must be > 0 and <= 60s, got %s", cfg.Timeout.String())
	}

	// Validate RecordType if set
	recordType := cfg.RecordType
	if recordType == "" {
		recordType = defaultRecordType
	}

	recordType = strings.ToUpper(recordType)
	if !validRecordTypes[recordType] {
		return checkerdef.NewConfigErrorf(
			"record_type", "must be one of A, AAAA, CNAME, MX, NS, TXT, SOA, got %s", cfg.RecordType,
		)
	}

	// Validate Nameserver format if set
	if cfg.Nameserver != "" {
		if !strings.Contains(cfg.Nameserver, ":") {
			return checkerdef.NewConfigErrorf("nameserver", "must be in format host:port, got %s", cfg.Nameserver)
		}
	}

	// Cannot specify both expected_ips and expected_values
	if len(cfg.ExpectedIPs) > 0 && len(cfg.ExpectedValues) > 0 {
		return checkerdef.NewConfigError("expected_values", "cannot specify both expected_ips and expected_values")
	}

	return nil
}

// Execute performs the DNS check and returns the result.
//
//nolint:funlen,cyclop // DNS checking requires comprehensive logic for different record types
func (c *DNSChecker) Execute(ctx context.Context, config checkerdef.Config) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*DNSConfig](config)
	if err != nil {
		return nil, err
	}

	// Apply defaults
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}

	recordType := strings.ToUpper(cfg.RecordType)
	if recordType == "" {
		recordType = defaultRecordType
	}

	start := time.Now()

	// Create resolver
	resolver := c.createResolver(cfg.Nameserver, timeout)

	// Perform DNS lookup based on record type
	var resolvedIPs []string

	var resolvedValues []string

	switch recordType {
	case "A":
		resolvedIPs, err = c.lookupA(ctx, resolver, cfg.Host)
	case "AAAA":
		resolvedIPs, err = c.lookupAAAA(ctx, resolver, cfg.Host)
	case "CNAME":
		resolvedValues, err = c.lookupCNAME(ctx, resolver, cfg.Host)
	case "MX":
		resolvedValues, err = c.lookupMX(ctx, resolver, cfg.Host)
	case "NS":
		resolvedValues, err = c.lookupNS(ctx, resolver, cfg.Host)
	case "TXT":
		resolvedValues, err = c.lookupTXT(ctx, resolver, cfg.Host)
	case "SOA":
		resolvedValues, err = c.lookupSOA(ctx, resolver, cfg.Host)
	}

	duration := time.Since(start)

	// Handle errors
	if err != nil {
		// Check for NXDOMAIN (non-existent domain) - this is StatusDown
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
			return &checkerdef.Result{
				Status:   checkerdef.StatusDown,
				Duration: duration,
				Output: map[string]any{
					"host":        cfg.Host,
					"record_type": recordType,
				},
			}, nil
		}

		// Check for timeout
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return &checkerdef.Result{
				Status:   checkerdef.StatusTimeout,
				Duration: duration,
				Output: map[string]any{
					"host":        cfg.Host,
					"record_type": recordType,
				},
			}, nil
		}

		// Other errors - return nil result and error
		return nil, err
	}

	// Check if we got any results
	recordCount := len(resolvedIPs) + len(resolvedValues)
	if recordCount == 0 {
		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: duration,
			Output: map[string]any{
				"host":        cfg.Host,
				"record_type": recordType,
			},
		}, nil
	}

	// Check expected values if specified
	status := checkerdef.StatusUp

	if len(cfg.ExpectedIPs) > 0 {
		if !c.matchValues(resolvedIPs, cfg.ExpectedIPs, true) {
			status = checkerdef.StatusDown
		}
	}

	if len(cfg.ExpectedValues) > 0 {
		if !c.matchValues(resolvedValues, cfg.ExpectedValues, false) {
			status = checkerdef.StatusDown
		}
	}

	// Build result
	result := checkerdef.Result{
		Status:   status,
		Duration: duration,
		Metrics: map[string]any{
			"query_time_ms": float64(duration.Milliseconds()),
			"record_count":  recordCount,
		},
		Output: map[string]any{
			"host":        cfg.Host,
			"record_type": recordType,
		},
	}

	if len(resolvedIPs) > 0 {
		result.Output["resolved_ips"] = resolvedIPs
	}

	if len(resolvedValues) > 0 {
		result.Output["resolved_values"] = resolvedValues
	}

	if cfg.Nameserver != "" {
		result.Output["nameserver"] = cfg.Nameserver
	}

	if status == checkerdef.StatusDown && (len(cfg.ExpectedIPs) > 0 || len(cfg.ExpectedValues) > 0) {
		result.Output["error"] = "resolved values do not match expected values"
	}

	return &result, nil
}

// createResolver creates a DNS resolver with optional custom nameserver.
func (c *DNSChecker) createResolver(nameserver string, timeout time.Duration) *net.Resolver {
	if nameserver == "" {
		// Use system default DNS
		return &net.Resolver{}
	}

	// Custom nameserver
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: timeout}
			return d.DialContext(ctx, "udp", nameserver)
		},
	}
}

// lookupA performs A record lookup (IPv4).
func (c *DNSChecker) lookupA(ctx context.Context, resolver *net.Resolver, hostname string) ([]string, error) {
	addrs, err := resolver.LookupIPAddr(ctx, hostname)
	if err != nil {
		return nil, err
	}

	var ips []string

	//nolint:gocritic // rangeValCopy: IPAddr is small enough, copying is acceptable
	for _, addr := range addrs {
		if addr.IP.To4() != nil {
			ips = append(ips, addr.IP.String())
		}
	}

	return ips, nil
}

// lookupAAAA performs AAAA record lookup (IPv6).
func (c *DNSChecker) lookupAAAA(ctx context.Context, resolver *net.Resolver, hostname string) ([]string, error) {
	addrs, err := resolver.LookupIPAddr(ctx, hostname)
	if err != nil {
		return nil, err
	}

	var ips []string

	//nolint:gocritic // rangeValCopy: IPAddr is small enough, copying is acceptable
	for _, addr := range addrs {
		if addr.IP.To4() == nil {
			ips = append(ips, addr.IP.String())
		}
	}

	return ips, nil
}

// lookupCNAME performs CNAME record lookup.
func (c *DNSChecker) lookupCNAME(ctx context.Context, resolver *net.Resolver, hostname string) ([]string, error) {
	cname, err := resolver.LookupCNAME(ctx, hostname)
	if err != nil {
		return nil, err
	}

	// LookupCNAME returns the canonical name (with trailing dot removed)
	cname = strings.TrimSuffix(cname, ".")

	return []string{cname}, nil
}

// lookupMX performs MX record lookup.
func (c *DNSChecker) lookupMX(ctx context.Context, resolver *net.Resolver, hostname string) ([]string, error) {
	mxRecords, err := resolver.LookupMX(ctx, hostname)
	if err != nil {
		return nil, err
	}

	values := make([]string, 0, len(mxRecords))

	for _, mx := range mxRecords {
		// Format: "priority hostname"
		host := strings.TrimSuffix(mx.Host, ".")
		values = append(values, fmt.Sprintf("%d %s", mx.Pref, host))
	}

	return values, nil
}

// lookupNS performs NS record lookup.
func (c *DNSChecker) lookupNS(ctx context.Context, resolver *net.Resolver, hostname string) ([]string, error) {
	nsRecords, err := resolver.LookupNS(ctx, hostname)
	if err != nil {
		return nil, err
	}

	values := make([]string, 0, len(nsRecords))

	for _, ns := range nsRecords {
		host := strings.TrimSuffix(ns.Host, ".")
		values = append(values, host)
	}

	return values, nil
}

// lookupTXT performs TXT record lookup.
func (c *DNSChecker) lookupTXT(ctx context.Context, resolver *net.Resolver, hostname string) ([]string, error) {
	return resolver.LookupTXT(ctx, hostname)
}

// lookupSOA performs SOA record lookup.
func (c *DNSChecker) lookupSOA(_ context.Context, _ *net.Resolver, _ string) ([]string, error) {
	// SOA records are not directly supported by net.Resolver
	// For now, return not supported error
	return nil, errSOANotSupported
}

// matchValues checks if resolved values match expected values.
// caseInsensitive should be true for IP addresses.
func (c *DNSChecker) matchValues(resolved, expected []string, caseInsensitive bool) bool {
	// Convert to sets for comparison
	resolvedSet := make(map[string]bool)

	for _, val := range resolved {
		key := val
		if caseInsensitive {
			key = strings.ToLower(val)
		}

		resolvedSet[key] = true
	}

	// Check if all expected values are present
	for _, val := range expected {
		key := val
		if caseInsensitive {
			key = strings.ToLower(val)
		}

		if !resolvedSet[key] {
			return false
		}
	}

	return true
}
