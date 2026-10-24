// Package checkdns provides DNS resolution monitoring checks.
package checkdns

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/miekg/dns"

	checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkdns/config"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	// defaultTimeout is the per-query timeout when the config sets none.
	defaultTimeout = 5 * time.Second
)

// resolvConfPath is where the CAA lookup finds the system resolvers, since
// net.Resolver cannot query that record type.
const resolvConfPath = "/etc/resolv.conf"

var (
	errSOANotSupported = errors.New("SOA record type not yet supported")
	errNoNameserver    = errors.New("no nameserver available")
	errCAALookup       = errors.New("CAA lookup failed")
)

// lookupFunc resolves host for recordType, returning addresses (A/AAAA) or
// values (every other type). Tests swap it for a stub resolver.
type lookupFunc func(ctx context.Context, cfg *DNSConfig, recordType string, timeout time.Duration) (
	ips, values []string, err error,
)

// DNSChecker implements the Checker interface for DNS checks.
type DNSChecker struct {
	// lookup overrides the real resolver (tests only); nil uses net.Resolver.
	lookup lookupFunc
}

// Type returns the check type identifier.
func (c *DNSChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeDNS
}

// Validate checks if the configuration is valid. Every rule lives in the light
// `config` sub-package so an offline validator (`sp checks validate`) can run it
// without linking this checker's execution client.
func (c *DNSChecker) Validate(spec *checkerdef.CheckSpec) error {
	return checkconfig.ValidateSpec(spec)
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

	lookup := c.lookup
	if lookup == nil {
		lookup = c.resolve
	}

	resolvedIPs, resolvedValues, err := lookup(ctx, cfg, recordType, timeout)

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
					checkerdef.OutputKeyHost:       cfg.Host,
					checkerdef.OutputKeyRecordType: recordType,
				},
			}, nil
		}

		// Check for timeout
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return &checkerdef.Result{
				Status:   checkerdef.StatusTimeout,
				Duration: duration,
				Output: map[string]any{
					checkerdef.OutputKeyHost:       cfg.Host,
					checkerdef.OutputKeyRecordType: recordType,
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
				checkerdef.OutputKeyHost:       cfg.Host,
				checkerdef.OutputKeyRecordType: recordType,
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
			checkerdef.OutputKeyHost:       cfg.Host,
			checkerdef.OutputKeyRecordType: recordType,
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
		result.Output[checkerdef.OutputKeyError] = "resolved values do not match expected values"
	}

	if cfg.DetectChanges {
		applyChangeDetection(cfg, recordType, slices.Concat(resolvedIPs, resolvedValues), &result)
	}

	return &result, nil
}

// resolve performs the real lookup for recordType.
func (c *DNSChecker) resolve(
	ctx context.Context, cfg *DNSConfig, recordType string, timeout time.Duration,
) ([]string, []string, error) {
	resolver := c.createResolver(cfg.Nameserver, timeout)

	switch recordType {
	case recordTypeA:
		ips, err := c.lookupA(ctx, resolver, cfg.Host)

		return ips, nil, err
	case recordTypeAAAA:
		ips, err := c.lookupAAAA(ctx, resolver, cfg.Host)

		return ips, nil, err
	case recordTypeCNAME:
		values, err := c.lookupCNAME(ctx, resolver, cfg.Host)

		return nil, values, err
	case recordTypeMX:
		values, err := c.lookupMX(ctx, resolver, cfg.Host)

		return nil, values, err
	case recordTypeNS:
		values, err := c.lookupNS(ctx, resolver, cfg.Host)

		return nil, values, err
	case recordTypeTXT:
		values, err := c.lookupTXT(ctx, resolver, cfg.Host)

		return nil, values, err
	case recordTypeSOA:
		values, err := c.lookupSOA(ctx, resolver, cfg.Host)

		return nil, values, err
	case recordTypeCAA:
		values, err := lookupCAA(ctx, cfg.Nameserver, cfg.Host, timeout)

		return nil, values, err
	}

	return nil, nil, nil
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
			// A custom nameserver is a user-chosen target: it answers to the
			// egress guard (spec 2026-09-25-19) riding the lookup context.
			// The system resolver above is the worker's own and is not.
			d := checkerdef.GuardDialerOr(ctx, &net.Dialer{Timeout: timeout})

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

	for i := range addrs {
		if addrs[i].IP.To4() != nil {
			ips = append(ips, addrs[i].IP.String())
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

	for i := range addrs {
		if addrs[i].IP.To4() == nil {
			ips = append(ips, addrs[i].IP.String())
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

// lookupCAA queries the CAA records of hostname. net.Resolver has no CAA
// support, so the query is a raw DNS exchange: with the configured nameserver
// (a user-chosen target, dialed under the egress guard on ctx) or with the
// system resolvers of resolv.conf. Values read "<flag> <tag> \"<value>\"".
func lookupCAA(ctx context.Context, nameserver, hostname string, timeout time.Duration) ([]string, error) {
	servers := []string{nameserver}
	dialer := checkerdef.GuardedNetDialer(ctx, &net.Dialer{Timeout: timeout})

	if nameserver == "" {
		conf, err := dns.ClientConfigFromFile(resolvConfPath)
		if err != nil {
			return nil, fmt.Errorf("read system resolvers: %w", err)
		}

		servers = make([]string, 0, len(conf.Servers))
		for _, server := range conf.Servers {
			servers = append(servers, net.JoinHostPort(server, conf.Port))
		}

		// The system resolver is the worker's own, not a check target.
		dialer = &net.Dialer{Timeout: timeout}
	}

	query := new(dns.Msg)
	query.SetQuestion(dns.Fqdn(hostname), dns.TypeCAA)

	lastErr := errNoNameserver

	for _, server := range servers {
		resp, err := exchangeCAA(ctx, query, server, dialer, timeout)
		if err != nil {
			lastErr = err

			continue
		}

		switch resp.Rcode {
		case dns.RcodeSuccess:
		case dns.RcodeNameError:
			return nil, &net.DNSError{Err: "no such host", Name: hostname, IsNotFound: true}
		default:
			lastErr = fmt.Errorf("%w: %s answered %s", errCAALookup, server, dns.RcodeToString[resp.Rcode])

			continue
		}

		values := make([]string, 0, len(resp.Answer))

		for _, rr := range resp.Answer {
			if caa, ok := rr.(*dns.CAA); ok {
				values = append(values, fmt.Sprintf("%d %s %q", caa.Flag, caa.Tag, caa.Value))
			}
		}

		return values, nil
	}

	return nil, lastErr
}

// exchangeCAA sends query over UDP, retrying over TCP when the answer was
// truncated.
func exchangeCAA(
	ctx context.Context, query *dns.Msg, server string, dialer *net.Dialer, timeout time.Duration,
) (*dns.Msg, error) {
	client := &dns.Client{Timeout: timeout, Dialer: dialer}

	resp, _, err := client.ExchangeContext(ctx, query, server)
	if err == nil && resp.Truncated {
		client.Net = "tcp"
		resp, _, err = client.ExchangeContext(ctx, query, server)
	}

	if err != nil {
		return nil, fmt.Errorf("CAA lookup via %s: %w", server, err)
	}

	return resp, nil
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
