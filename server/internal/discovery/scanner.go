package discovery

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/fclairamb/solidping/server/internal/egress"
)

const (
	// defaultConcurrency is the default number of concurrent probes.
	defaultConcurrency = 64
	// defaultTimeout is the default per-probe timeout.
	defaultTimeout = time.Second
	// reverseDNSTimeout is the maximum time to wait for a reverse DNS lookup.
	reverseDNSTimeout = 500 * time.Millisecond
	// icmpProtocol is the ICMP protocol number.
	icmpProtocol = 1
)

// Config is the configuration for a discovery scan.
type Config struct {
	CIDRs       []string `json:"cidrs"`
	Ports       []int    `json:"ports,omitempty"`
	Concurrency int      `json:"concurrency,omitempty"`
	Timeout     string   `json:"timeout,omitempty"`
	// ParentJobUID, when set on a child network_discovery job, is the UID of the
	// network_discovery_plan job that scheduled it. Discovered hosts roll up under
	// the parent so the scan-detail page sees them all under one UID. The
	// synchronous Scan ignores it.
	ParentJobUID string `json:"parentJobUid,omitempty"`
	// SkipSuggestions leaves DiscoveredHost.SuggestedChecks empty: an agent
	// reports raw facts only, the server builds the suggestions itself (spec
	// 2026-10-22-04). Never part of a stored config.
	SkipSuggestions bool `json:"-"`
}

// DiscoveredHost is a host discovered during a scan.
type DiscoveredHost struct {
	IP              string           `json:"ip"`
	Hostname        string           `json:"hostname,omitempty"`
	ICMPReachable   bool             `json:"icmpReachable"`
	OpenPorts       []int            `json:"openPorts"`
	SuggestedChecks []SuggestedCheck `json:"suggestedChecks"`
}

// HostInput is one host to probe, with an optional pre-resolved name. It lets
// callers (e.g. the Freebox job) feed a fixed host list through the same engine
// the CIDR scan uses, preserving a known hostname over reverse DNS.
type HostInput struct {
	IP           net.IP
	HostnameHint string // preserved over reverse-DNS if non-empty
}

// probeTarget is the internal unit the worker pool probes: an IP plus an
// optional hostname hint. CIDR-expanded targets carry no hint; explicit-host
// targets carry the caller-supplied name.
type probeTarget struct {
	IP           string
	HostnameHint string
}

// scanParams holds the normalized scan tuning resolved from a Config.
type scanParams struct {
	ports           []int
	concurrency     int
	timeout         time.Duration
	skipSuggestions bool
}

// resolveScanParams normalizes the ports / concurrency / timeout from a Config,
// applying defaults. Shared by Scan and ScanHosts.
func resolveScanParams(cfg *Config) (scanParams, error) {
	params := scanParams{
		ports:           cfg.Ports,
		concurrency:     cfg.Concurrency,
		timeout:         defaultTimeout,
		skipSuggestions: cfg.SkipSuggestions,
	}

	if len(params.ports) == 0 {
		params.ports = DefaultPortList()
	}

	if params.concurrency <= 0 {
		params.concurrency = defaultConcurrency
	}

	if cfg.Timeout != "" {
		d, err := time.ParseDuration(cfg.Timeout)
		if err != nil {
			return scanParams{}, fmt.Errorf("invalid timeout %q: %w", cfg.Timeout, err)
		}

		params.timeout = d
	}

	return params, nil
}

// ScanResult is the outcome of a scan. ICMPAvailable is false when no ICMP
// socket could be opened and liveness fell back to TCP only.
type ScanResult struct {
	Hosts         []DiscoveredHost
	ICMPAvailable bool
}

// Scan performs a network discovery scan based on the given configuration.
// It expands all CIDRs, probes each address with ICMP and TCP, and returns
// a list of responsive hosts.
func Scan(ctx context.Context, cfg *Config) (*ScanResult, error) {
	if err := ValidateCIDRs(cfg.CIDRs); err != nil {
		return nil, err
	}

	params, err := resolveScanParams(cfg)
	if err != nil {
		return nil, err
	}

	// Expand all CIDRs into individual probe targets.
	targets := make([]probeTarget, 0, 256)
	for _, cidr := range cfg.CIDRs {
		ips, err := expandCIDR(cidr)
		if err != nil {
			return nil, err
		}

		for _, ip := range ips {
			targets = append(targets, probeTarget{IP: ip.String()})
		}
	}

	return runProbes(ctx, targets, params, listenICMPPacket), nil
}

// ScanHosts probes a fixed list of IPs through the same engine as Scan.
// cfg.Ports defaults to DefaultPortList(); cfg.CIDRs is ignored. Each result's
// Hostname comes from the matching HostInput.HostnameHint when non-empty, else
// from reverse DNS. Only responsive hosts are returned (same semantics as Scan).
func ScanHosts(ctx context.Context, hosts []HostInput, cfg *Config) (*ScanResult, error) {
	params, err := resolveScanParams(cfg)
	if err != nil {
		return nil, err
	}

	targets := make([]probeTarget, 0, len(hosts))
	for i := range hosts {
		if hosts[i].IP == nil {
			continue
		}

		targets = append(targets, probeTarget{
			IP:           hosts[i].IP.String(),
			HostnameHint: hosts[i].HostnameHint,
		})
	}

	return runProbes(ctx, targets, params, listenICMPPacket), nil
}

// runProbes drives the bounded-concurrency worker pool over the given targets.
// It is the shared core used by both Scan (after CIDR expansion) and ScanHosts
// (with a pre-supplied list).
func runProbes(
	ctx context.Context, targets []probeTarget, params scanParams, listen listenFunc,
) *ScanResult {
	// Attempt to open an ICMP socket; fall back to TCP-only if unavailable.
	pinger := newICMPPinger(listen)
	if pinger == nil {
		slog.WarnContext(ctx, "ICMP unavailable, TCP-only liveness probing will be used")
	} else {
		defer pinger.close()
	}

	sem := make(chan struct{}, params.concurrency)
	var hostsMu sync.Mutex
	hosts := make([]DiscoveredHost, 0)

	var wgroup sync.WaitGroup

	for i := range targets {
		if ctx.Err() != nil {
			break
		}

		tgt := targets[i]

		// Under an enforcing egress policy a refused target is skipped before
		// any probe: no ICMP echo, no TCP connect, no PTR lookup.
		if guard := egress.FromContext(ctx); guard.Enforcing() {
			ip := net.ParseIP(tgt.IP)
			if ip == nil {
				continue
			}

			if err := guard.CheckAddr(ctx, tgt.IP, ip); err != nil {
				continue
			}
		}

		wgroup.Add(1)

		go func() {
			defer wgroup.Done()

			sem <- struct{}{}
			defer func() { <-sem }()

			host := probeHost(ctx, tgt, params, pinger)
			if host == nil {
				return
			}

			hostsMu.Lock()
			hosts = append(hosts, *host)
			hostsMu.Unlock()
		}()
	}

	wgroup.Wait()

	return &ScanResult{Hosts: hosts, ICMPAvailable: pinger != nil}
}

// probeHost probes a single host with ICMP and TCP and returns a DiscoveredHost
// if it responds to any probe, or nil if it's unreachable. The host's name is
// taken from target.HostnameHint when non-empty, otherwise from reverse DNS.
func probeHost(
	ctx context.Context,
	target probeTarget,
	params scanParams,
	pinger *icmpPinger,
) *DiscoveredHost {
	ipStr := target.IP
	ports := params.ports
	timeout := params.timeout

	var (
		icmpReachable bool
		openPorts     []int
		wgroup        sync.WaitGroup
		portsMu       sync.Mutex
	)

	// ICMP probe.
	if pinger != nil {
		wgroup.Add(1)
		go func() {
			defer wgroup.Done()
			if pinger.ping(ipStr, timeout) {
				portsMu.Lock()
				icmpReachable = true
				portsMu.Unlock()
			}
		}()
	}

	// TCP probes (concurrent per port).
	for _, port := range ports {
		wgroup.Add(1)
		p := port
		go func() {
			defer wgroup.Done()
			if dialTCP(ctx, ipStr, p, timeout) {
				portsMu.Lock()
				openPorts = append(openPorts, p)
				portsMu.Unlock()
			}
		}()
	}

	wgroup.Wait()

	if !icmpReachable && len(openPorts) == 0 {
		return nil
	}

	// Prefer the caller-supplied hostname hint; fall back to reverse DNS.
	hostname := target.HostnameHint
	if hostname == "" {
		hostname = resolveHostname(ctx, ipStr)
	}

	// Sort open ports for consistent output.
	sortPorts(openPorts)

	host := &DiscoveredHost{
		IP:            ipStr,
		Hostname:      hostname,
		ICMPReachable: icmpReachable,
		OpenPorts:     openPorts,
	}

	if !params.skipSuggestions {
		host.SuggestedChecks = SuggestChecks(ipStr, hostname, icmpReachable, openPorts)
	}

	return host
}

// dialTCP attempts a TCP connection to the given host:port within the timeout.
func dialTCP(ctx context.Context, ipStr string, port int, timeout time.Duration) bool {
	addr := fmt.Sprintf("%s:%d", ipStr, port)
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// A nil or permissive guard dials exactly like a bare net.Dialer.
	conn, err := egress.FromContext(ctx).DialContextWith(dialCtx, &net.Dialer{}, "tcp", addr)
	if err != nil {
		return false
	}

	_ = conn.Close()

	return true
}

// resolveHostname performs a best-effort reverse DNS lookup with a bounded timeout.
func resolveHostname(ctx context.Context, ipStr string) string {
	lookupCtx, cancel := context.WithTimeout(ctx, reverseDNSTimeout)
	defer cancel()

	names, err := net.DefaultResolver.LookupAddr(lookupCtx, ipStr)
	if err != nil || len(names) == 0 {
		return ""
	}

	// Strip trailing dot from PTR record.
	name := names[0]
	if len(name) > 0 && name[len(name)-1] == '.' {
		name = name[:len(name)-1]
	}

	return name
}

// sortPorts sorts ports in ascending order.
func sortPorts(ports []int) {
	for i := 0; i < len(ports); i++ {
		for j := i + 1; j < len(ports); j++ {
			if ports[i] > ports[j] {
				ports[i], ports[j] = ports[j], ports[i]
			}
		}
	}
}
