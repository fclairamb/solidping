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

// DefaultPorts is the default set of ports to scan when none are specified.
var DefaultPorts = []int{22, 25, 53, 80, 110, 143, 443, 465, 587, 993, 995, 3306, 5432, 6379, 8080, 8443}

// Config is the configuration for a discovery scan.
type Config struct {
	CIDRs       []string `json:"cidrs"`
	Ports       []int    `json:"ports,omitempty"`
	Concurrency int      `json:"concurrency,omitempty"`
	Timeout     string   `json:"timeout,omitempty"`
}

// DiscoveredHost is a host discovered during a scan.
type DiscoveredHost struct {
	IP              string           `json:"ip"`
	Hostname        string           `json:"hostname,omitempty"`
	ICMPReachable   bool             `json:"icmpReachable"`
	OpenPorts       []int            `json:"openPorts"`
	SuggestedChecks []SuggestedCheck `json:"suggestedChecks"`
}

// Scan performs a network discovery scan based on the given configuration.
// It expands all CIDRs, probes each address with ICMP and TCP, and returns
// a list of responsive hosts.
func Scan(ctx context.Context, cfg Config) ([]DiscoveredHost, error) {
	if err := ValidateCIDRs(cfg.CIDRs); err != nil {
		return nil, err
	}

	ports := cfg.Ports
	if len(ports) == 0 {
		ports = DefaultPorts
	}

	concurrency := cfg.Concurrency
	if concurrency <= 0 {
		concurrency = defaultConcurrency
	}

	probeTimeout := defaultTimeout
	if cfg.Timeout != "" {
		d, err := time.ParseDuration(cfg.Timeout)
		if err != nil {
			return nil, fmt.Errorf("invalid timeout %q: %w", cfg.Timeout, err)
		}
		probeTimeout = d
	}

	// Expand all CIDRs into individual IPs.
	allIPs := make([]net.IP, 0, 256)
	for _, cidr := range cfg.CIDRs {
		ips, err := expandCIDR(cidr)
		if err != nil {
			return nil, err
		}
		allIPs = append(allIPs, ips...)
	}

	// Attempt to open an ICMP socket; fall back to TCP-only if unavailable.
	icmpConn, icmpErr := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if icmpErr != nil {
		slog.WarnContext(ctx, "ICMP unavailable, TCP-only liveness probing will be used",
			"error", icmpErr)
	} else {
		defer icmpConn.Close()
	}

	sem := make(chan struct{}, concurrency)
	var mu sync.Mutex
	hosts := make([]DiscoveredHost, 0)

	var wg sync.WaitGroup

	for _, ip := range allIPs {
		select {
		case <-ctx.Done():
			break
		default:
		}

		wg.Add(1)
		ipStr := ip.String()

		go func(ipStr string) {
			defer wg.Done()

			sem <- struct{}{}
			defer func() { <-sem }()

			host := probeHost(ctx, ipStr, ports, icmpConn, probeTimeout)
			if host == nil {
				return
			}

			mu.Lock()
			hosts = append(hosts, *host)
			mu.Unlock()
		}(ipStr)
	}

	wg.Wait()

	return hosts, nil
}

// probeHost probes a single host with ICMP and TCP and returns a DiscoveredHost
// if it responds to any probe, or nil if it's unreachable.
func probeHost(
	ctx context.Context,
	ipStr string,
	ports []int,
	icmpConn *icmp.PacketConn,
	timeout time.Duration,
) *DiscoveredHost {
	var (
		icmpReachable bool
		openPorts     []int
		wg            sync.WaitGroup
		mu            sync.Mutex
	)

	// ICMP probe.
	if icmpConn != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if pingHost(icmpConn, ipStr, timeout) {
				mu.Lock()
				icmpReachable = true
				mu.Unlock()
			}
		}()
	}

	// TCP probes (concurrent per port).
	for _, port := range ports {
		wg.Add(1)
		p := port
		go func() {
			defer wg.Done()
			if dialTCP(ctx, ipStr, p, timeout) {
				mu.Lock()
				openPorts = append(openPorts, p)
				mu.Unlock()
			}
		}()
	}

	wg.Wait()

	if !icmpReachable && len(openPorts) == 0 {
		return nil
	}

	// Best-effort reverse DNS.
	hostname := resolveHostname(ipStr)

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

	if err := conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		return false
	}

	if _, err := conn.WriteTo(data, dst); err != nil {
		return false
	}

	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return false
	}

	buf := make([]byte, 1500)
	n, _, err := conn.ReadFrom(buf)
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
	conn, err := (&net.Dialer{}).DialContext(timeoutCtx(ctx, timeout), "tcp", addr)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// timeoutCtx returns a context that expires after the given duration, bounded
// by the parent context.
func timeoutCtx(parent context.Context, d time.Duration) context.Context {
	ctx, cancel := context.WithTimeout(parent, d)
	_ = cancel // The caller controls the parent context; the deadline will fire automatically.
	return ctx
}

// resolveHostname performs a best-effort reverse DNS lookup with a bounded timeout.
func resolveHostname(ipStr string) string {
	ctx, cancel := context.WithTimeout(context.Background(), reverseDNSTimeout)
	defer cancel()

	names, err := net.DefaultResolver.LookupAddr(ctx, ipStr)
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
