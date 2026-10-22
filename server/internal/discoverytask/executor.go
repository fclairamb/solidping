package discoverytask

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkworker/tasks"
	disc "github.com/fclairamb/solidping/server/internal/discovery"
	"github.com/fclairamb/solidping/server/internal/egress"
	"github.com/fclairamb/solidping/server/internal/workertasks"
)

// Agent-side caps. The server sends no concurrency or timeout: these are the
// agent's own.
const (
	scanConcurrency = 64
	probeTimeout    = "1s"
	chunkDeadline   = 10 * time.Minute
	// maxChunkHostBits bounds a prefix before it is expanded: a /20 is
	// discovery.MaxAddresses.
	maxChunkHostBits = 12
	maxPort          = 65535
)

var (
	errInvalidChunk = errors.New("invalid discovery chunk")
	errNotAllowed   = errors.New("discovery chunk outside the allow-list")
)

// Executor is the agent side of the discovery kind. It is only registered when
// the agent's operator enabled discovery, with the allow-list they set.
type Executor struct {
	allowed []netip.Prefix
	logger  *slog.Logger
}

var _ tasks.Executor = (*Executor)(nil)

// NewExecutor builds the executor over the operator's allow-list (config
// AgentConfig.DiscoveryPrefixes).
func NewExecutor(allowed []netip.Prefix) *Executor {
	return &Executor{allowed: allowed, logger: slog.Default().With("component", "discovery_task")}
}

// Kind implements tasks.Executor.
func (*Executor) Kind() string { return Kind }

// Capability implements tasks.Executor.
func (*Executor) Capability() string { return Capability }

// Run implements tasks.Executor: check the chunk, scan it, report the facts.
// A refused chunk finishes with its terminal code: retrying it cannot help.
func (e *Executor) Run(ctx context.Context, claim *workertasks.Claim, rep tasks.Reporter) error {
	if len(e.allowed) == 0 {
		return e.refuse(ctx, claim, rep, CodeDisabled, errNotAllowed)
	}

	var input Input
	if err := json.Unmarshal(claim.Input, &input); err != nil {
		return e.refuse(ctx, claim, rep, CodeInvalidChunk, err)
	}

	prefixes, addrs, err := e.validate(input.CIDRs)
	if err != nil {
		code := CodeInvalidChunk
		if errors.Is(err, errNotAllowed) {
			code = CodeNotAllowed
		}

		return e.refuse(ctx, claim, rep, code, err)
	}

	ports, err := validatePorts(input.Ports)
	if err != nil {
		return e.refuse(ctx, claim, rep, CodeInvalidChunk, err)
	}

	// An enforcing guard would skip every private target and report an empty
	// success: say so instead.
	if egress.FromContext(ctx).Enforcing() {
		return e.refuse(ctx, claim, rep, CodeEgressRefused, egress.ErrDenied)
	}

	hosts := make([]disc.HostInput, 0, len(addrs))
	for _, addr := range addrs {
		hosts = append(hosts, disc.HostInput{IP: net.IP(addr.AsSlice())})
	}

	scanCtx, cancel := context.WithTimeout(ctx, chunkDeadline)
	defer cancel()

	res, err := disc.ScanHosts(scanCtx, hosts, &disc.Config{
		Ports: ports, Concurrency: scanConcurrency, Timeout: probeTimeout, SkipSuggestions: true,
	})
	if err != nil {
		return fmt.Errorf("scan discovery chunk: %w", err)
	}

	body := Result{
		Hosts:         make([]HostFact, 0, len(res.Hosts)),
		Partial:       scanCtx.Err() != nil,
		ICMPAvailable: res.ICMPAvailable,
	}

	for i := range res.Hosts {
		host := &res.Hosts[i]
		body.Hosts = append(body.Hosts, HostFact{
			IP: host.IP, Hostname: host.Hostname, ICMPReachable: host.ICMPReachable, OpenPorts: host.OpenPorts,
		})
	}

	e.logger.InfoContext(ctx, "Discovery chunk scanned", "task", claim.UID, "cidrs", prefixStrings(prefixes),
		"port_count", len(ports), "host_count", len(body.Hosts), "partial", body.Partial)

	return finish(ctx, rep, &body)
}

func (e *Executor) refuse(
	ctx context.Context, claim *workertasks.Claim, rep tasks.Reporter, code string, cause error,
) error {
	e.logger.WarnContext(ctx, "Discovery chunk refused", "task", claim.UID, "code", code, "error", cause)

	return finish(ctx, rep, &Result{Hosts: []HostFact{}, ErrorCode: code})
}

func finish(ctx context.Context, rep tasks.Reporter, body *Result) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode discovery result: %w", err)
	}

	return rep.Finish(ctx, raw)
}

// validate parses every CIDR once, requires a masked IPv4 prefix (a 4-in-6
// form is not IPv4) inside some allowed prefix, and expands the chunk to a
// deduplicated address set of at most discovery.MaxAddresses.
func (e *Executor) validate(cidrs []string) ([]netip.Prefix, []netip.Addr, error) {
	if len(cidrs) == 0 {
		return nil, nil, fmt.Errorf("%w: no cidr", errInvalidChunk)
	}

	prefixes := make([]netip.Prefix, 0, len(cidrs))
	seenPrefix := make(map[netip.Prefix]bool, len(cidrs))

	for _, cidr := range cidrs {
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil || !prefix.Addr().Is4() || prefix != prefix.Masked() || 32-prefix.Bits() > maxChunkHostBits {
			return nil, nil, fmt.Errorf("%w: %q", errInvalidChunk, cidr)
		}

		if !e.isAllowed(prefix) {
			return nil, nil, fmt.Errorf("%w: %s", errNotAllowed, prefix)
		}

		if !seenPrefix[prefix] {
			seenPrefix[prefix] = true
			prefixes = append(prefixes, prefix)
		}
	}

	seen := make(map[netip.Addr]bool)
	addrs := make([]netip.Addr, 0)

	for i := range prefixes {
		prefix := &prefixes[i]
		for addr := prefix.Addr(); prefix.Contains(addr); addr = addr.Next() {
			if seen[addr] {
				continue
			}

			if len(addrs) >= disc.MaxAddresses {
				return nil, nil, fmt.Errorf("%w: more than %d addresses", errInvalidChunk, disc.MaxAddresses)
			}

			seen[addr] = true
			addrs = append(addrs, addr)
		}
	}

	return prefixes, addrs, nil
}

func (e *Executor) isAllowed(prefix netip.Prefix) bool {
	for i := range e.allowed {
		if e.allowed[i].Bits() <= prefix.Bits() && e.allowed[i].Contains(prefix.Addr()) {
			return true
		}
	}

	return false
}

// validatePorts requires 1..65535, deduplicated, at most MaxPorts.
func validatePorts(ports []int) ([]int, error) {
	if len(ports) == 0 {
		return nil, fmt.Errorf("%w: no port", errInvalidChunk)
	}

	out := make([]int, 0, len(ports))
	seen := make(map[int]bool, len(ports))

	for _, port := range ports {
		if port < 1 || port > maxPort {
			return nil, fmt.Errorf("%w: port %d", errInvalidChunk, port)
		}

		if !seen[port] {
			seen[port] = true
			out = append(out, port)
		}
	}

	if len(out) > MaxPorts {
		return nil, fmt.Errorf("%w: %d ports (max %d)", errInvalidChunk, len(out), MaxPorts)
	}

	return out, nil
}

func prefixStrings(prefixes []netip.Prefix) []string {
	out := make([]string, 0, len(prefixes))
	for i := range prefixes {
		out = append(out, prefixes[i].String())
	}

	return out
}
