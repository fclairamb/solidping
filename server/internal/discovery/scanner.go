package discovery

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
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
	ports       []int
	concurrency int
	timeout     time.Duration
}

// resolveScanParams normalizes the ports / concurrency / timeout from a Config,
// applying defaults. Shared by Scan and ScanHosts.
func resolveScanParams(cfg Config) (scanParams, error) {
	params := scanParams{
		ports:       cfg.Ports,
		concurrency: cfg.Concurrency,
		timeout:     defaultTimeout,
	}

	if len(params.ports) == 0 {
		params.ports = defaultPortList()
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

// Scan performs a network discovery scan based on the given configuration.
// It expands all CIDRs, probes each address with ICMP and TCP, and returns
// a list of responsive hosts.
func Scan(ctx context.Context, cfg Config) ([]DiscoveredHost, error) {
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

	return runProbes(ctx, targets, params), nil
}

// ScanHosts probes a fixed list of IPs through the same engine as Scan.
// cfg.Ports defaults to defaultPortList(); cfg.CIDRs is ignored. Each result's
// Hostname comes from the matching HostInput.HostnameHint when non-empty, else
// from reverse DNS. Only responsive hosts are returned (same semantics as Scan).
func ScanHosts(ctx context.Context, hosts []HostInput, cfg Config) ([]DiscoveredHost, error) {
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

	return runProbes(ctx, targets, params), nil
}

// runProbes drives the bounded-concurrency worker pool over the given targets.
// It is the shared core used by both Scan (after CIDR expansion) and ScanHosts
// (with a pre-supplied list).
func runProbes(ctx context.Context, targets []probeTarget, params scanParams) []DiscoveredHost {
	// Attempt to open an ICMP socket; fall back to TCP-only if unavailable.
	icmpConn, icmpErr := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if icmpErr != nil {
		slog.WarnContext(ctx, "ICMP unavailable, TCP-only liveness probing will be used",
			"error", icmpErr)
	} else {
		defer func() { _ = icmpConn.Close() }()
	}

	sem := make(chan struct{}, params.concurrency)
	var hostsMu sync.Mutex
	hosts := make([]DiscoveredHost, 0)

	var wgroup sync.WaitGroup

	for i := range targets {
		if ctx.Err() != nil {
			break
		}

		wgroup.Add(1)
		tgt := targets[i]

		go func() {
			defer wgroup.Done()

			sem <- struct{}{}
			defer func() { <-sem }()

			host := probeHost(ctx, tgt, params.ports, icmpConn, params.timeout)
			if host == nil {
				return
			}

			hostsMu.Lock()
			hosts = append(hosts, *host)
			hostsMu.Unlock()
		}()
	}

	wgroup.Wait()

	return hosts
}

// probeHost probes a single host with ICMP and TCP and returns a DiscoveredHost
// if it responds to any probe, or nil if it's unreachable. The host's name is
// taken from target.HostnameHint when non-empty, otherwise from reverse DNS.
func probeHost(
	ctx context.Context,
	target probeTarget,
	ports []int,
	icmpConn *icmp.PacketConn,
	timeout time.Duration,
) *DiscoveredHost {
	ipStr := target.IP

	var (
		icmpReachable bool
		openPorts     []int
		wgroup        sync.WaitGroup
		portsMu       sync.Mutex
	)

	// ICMP probe.
	if icmpConn != nil {
		wgroup.Add(1)
		go func() {
			defer wgroup.Done()
			if pingHost(icmpConn, ipStr, timeout) {
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

	return &DiscoveredHost{
		IP:              ipStr,
		Hostname:        hostname,
		ICMPReachable:   icmpReachable,
		OpenPorts:       openPorts,
		SuggestedChecks: SuggestChecks(ipStr, icmpReachable, openPorts),
	}
}

// pingHost sends a single ICMP echo request and waits for a reply.
func pingHost(conn *icmp.PacketConn, ipStr string, timeout time.Duration) bool {
	msg := icmp.Message{
		Type: ipv4.ICMPTypeEcho,
		Code: 0,
		Body: &icmp.Echo{
			ID:   1,
			Seq:  1,
			Data: []byte("solidping"),
		},
	}

	data, err := msg.Marshal(nil)
	if err != nil {
		return false
	}

	dst := &net.IPAddr{IP: net.ParseIP(ipStr)}

	if err = conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		return false
	}

	if _, err = conn.WriteTo(data, dst); err != nil {
		return false
	}

	if err = conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return false
	}

	buf := make([]byte, 1500)

	var n int
	n, _, err = conn.ReadFrom(buf)
	if err != nil {
		return false
	}

	reply, err := icmp.ParseMessage(icmpProtocol, buf[:n])
	if err != nil {
		return false
	}

	return reply.Type == ipv4.ICMPTypeEchoReply
}

// dialTCP attempts a TCP connection to the given host:port within the timeout.
func dialTCP(ctx context.Context, ipStr string, port int, timeout time.Duration) bool {
	addr := fmt.Sprintf("%s:%d", ipStr, port)
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	conn, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", addr)
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
