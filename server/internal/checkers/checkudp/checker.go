// Package checkudp provides UDP port reachability checks.
package checkudp

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	defaultTimeout       = 5 * time.Second
	microsecondsPerMilli = 1000.0
)

// UDPChecker implements the Checker interface for UDP port checks.
type UDPChecker struct{}

// Type returns the check type identifier.
func (c *UDPChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeUDP
}

// Validate checks if the configuration is valid.
func (c *UDPChecker) Validate(spec *checkerdef.CheckSpec) error {
	cfg := &UDPConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if cfg.Host == "" {
		return checkerdef.NewConfigError("host", "is required")
	}

	if cfg.Port == 0 {
		return checkerdef.NewConfigError("port", "is required")
	}

	if cfg.Port < 1 || cfg.Port > 65535 {
		return checkerdef.NewConfigErrorf("port", "must be between 1 and 65535, got %d", cfg.Port)
	}

	if cfg.Timeout != 0 && (cfg.Timeout <= 0 || cfg.Timeout > 30*time.Second) {
		return checkerdef.NewConfigErrorf("timeout", "must be > 0 and <= 30s, got %s", cfg.Timeout.String())
	}

	// A bad encoding or an uncompilable `expect_pattern` is a VALIDATION_ERROR
	// on save, never a check that errors forever at runtime.
	if err := cfg.exchangeFields().Validate(); err != nil {
		return err
	}

	// Auto-generate name and slug from host if not provided
	if spec.Name == "" {
		spec.Name = fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	}

	if spec.Slug == "" {
		spec.Slug = "udp-" + strings.ReplaceAll(cfg.Host, ".", "-")
	}

	return nil
}

// Execute performs the UDP check and returns the result.
func (c *UDPChecker) Execute(ctx context.Context, config checkerdef.Config) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*UDPConfig](config)
	if err != nil {
		return nil, err
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}

	start := time.Now()

	// Resolve hostname
	addrs, err := checkerdef.LookupIPAddr(ctx, cfg.Host)
	if err != nil {
		return &checkerdef.Result{
			Status:   checkerdef.StatusError,
			Duration: time.Since(start),
			Output: map[string]any{
				checkerdef.OutputKeyError: fmt.Sprintf("failed to resolve hostname: %v", err),
			},
		}, nil
	}

	if len(addrs) == 0 {
		return &checkerdef.Result{
			Status:   checkerdef.StatusError,
			Duration: time.Since(start),
			Output: map[string]any{
				checkerdef.OutputKeyError: "no IP addresses found for host",
			},
		}, nil
	}

	// Pick the address to dial — IPv4-first by default, or the family this
	// check pins via `ipVersion`. One shared implementation for every checker.
	targetIP, selectErr := checkerdef.SelectIPAddr(cfg.Host, addrs, checkerdef.IPVersionFrom(ctx))
	if selectErr != nil {
		return &checkerdef.Result{
			Status:   checkerdef.IPVersionFailureStatus(selectErr),
			Duration: time.Since(start),
			Output: map[string]any{
				checkerdef.OutputKeyError: selectErr.Error(),
			},
		}, nil
	}

	result := c.connect(ctx, targetIP, cfg, timeout)
	result.Duration = time.Since(start)

	ipVersion := checkerdef.IPVersionOf(targetIP).String()

	if result.Output == nil {
		result.Output = make(map[string]any)
	}

	result.Output["host"] = targetIP.String()
	result.Output["port"] = cfg.Port
	result.Output[checkerdef.OutputKeyIPVersion] = ipVersion

	return &result, nil
}

// connect performs the actual UDP operation.
func (c *UDPChecker) connect(
	ctx context.Context,
	targetIP net.IP,
	cfg *UDPConfig,
	timeout time.Duration,
) checkerdef.Result {
	// One deadline for the whole exchange — dial, write and the wait for the
	// reply — rather than `now + timeout` re-armed at each stage.
	ctxWithTimeout, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	deadline, hasDeadline := ctxWithTimeout.Deadline()
	if !hasDeadline {
		deadline = time.Now().Add(timeout)
	}

	exchange, exchangeErr := checkerdef.NewExchange(
		cfg.SendData, cfg.SendEncoding,
		cfg.ExpectData, cfg.ExpectEncoding,
		cfg.ExpectPattern, timeout, deadline,
	)
	if exchangeErr != nil {
		return checkerdef.Result{
			Status: checkerdef.StatusError,
			Output: map[string]any{
				checkerdef.OutputKeyError: fmt.Sprintf("invalid payload configuration: %v", exchangeErr),
			},
		}
	}

	target := net.JoinHostPort(targetIP.String(), strconv.Itoa(cfg.Port))

	dialer := &net.Dialer{}

	conn, err := dialer.DialContext(ctxWithTimeout, "udp", target)
	if err != nil {
		if ctxWithTimeout.Err() != nil {
			return checkerdef.Result{
				Status: checkerdef.StatusTimeout,
				Output: map[string]any{checkerdef.OutputKeyError: "connection timeout"},
			}
		}

		return checkerdef.Result{
			Status: checkerdef.StatusDown,
			Output: map[string]any{checkerdef.OutputKeyError: fmt.Sprintf("dial failed: %v", err)},
		}
	}

	defer func() { _ = conn.Close() }()

	metrics := map[string]any{}
	output := map[string]any{}

	// Send the payload, then read datagrams until the expectation is satisfied.
	// Each Read is one datagram; they accumulate in the same buffer, so a reply
	// split across two datagrams matches exactly like a split TCP segment does.
	if failure := exchange.Run(conn, metrics, output); failure != nil {
		return *failure
	}

	return checkerdef.Result{
		Status:  checkerdef.StatusUp,
		Metrics: metrics,
		Output:  output,
	}
}
