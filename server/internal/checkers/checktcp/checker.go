// Package checktcp provides TCP port connectivity checks.
package checktcp

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checktcp/config"
)

const (
	// Default values from spec.
	defaultTimeout       = 5 * time.Second
	defaultTLSVerify     = true
	microsecondsPerMilli = 1000.0 // Conversion factor for microseconds to milliseconds
)

// TCPChecker implements the Checker interface for TCP connection checks.
type TCPChecker struct{}

// Type returns the check type identifier.
func (c *TCPChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeTCP
}

// Validate checks if the configuration is valid. The whole rule set lives in
// the light `config` sub-package so an offline validator (`sp checks validate`)
// can run it without linking this checker's execution client.
func (c *TCPChecker) Validate(spec *checkerdef.CheckSpec) error {
	return checkconfig.ValidateSpec(spec)
}

// Execute performs the TCP connection check and returns the result.
func (c *TCPChecker) Execute(ctx context.Context, config checkerdef.Config) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*TCPConfig](config)
	if err != nil {
		return nil, err
	}

	// Apply defaults
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}

	tlsVerify := cfg.TLSVerify
	if cfg.TLS && !cfg.TLSVerify {
		tlsVerify = false
	} else if cfg.TLS {
		tlsVerify = defaultTLSVerify
	}

	start := time.Now()

	// Tunneled check: the probe is dialed through the SSH bastion on the
	// context. Local name resolution is deliberately SKIPPED — the direct-tcpip
	// request carries the hostname and the *bastion* resolves it. That is the
	// whole point: a private hostname (an internal service, a VPC-only record)
	// resolves on the far side, where it means something, and would only ever
	// fail here. The untunneled path below is byte-for-byte unchanged.
	if dialer := checkerdef.TunnelDialerFrom(ctx); dialer != nil {
		return c.executeTunneled(ctx, dialer, cfg, timeout, tlsVerify, start), nil
	}

	return c.executeDirect(ctx, cfg, timeout, tlsVerify, start), nil
}

// executeDirect is the untunneled path: resolve the hostname locally, pick an
// address, dial it. Byte-for-byte the behavior that predates tunnel support.
func (c *TCPChecker) executeDirect(
	ctx context.Context,
	cfg *TCPConfig,
	timeout time.Duration,
	tlsVerify bool,
	start time.Time,
) *checkerdef.Result {
	// Resolve hostname
	addrs, err := checkerdef.LookupIPAddr(ctx, cfg.Host)
	if err != nil {
		return &checkerdef.Result{
			Status:   checkerdef.StatusError,
			Duration: time.Since(start),
			Output: map[string]any{
				checkerdef.OutputKeyError: fmt.Sprintf("failed to resolve hostname: %v", err),
			},
		}
	}

	if len(addrs) == 0 {
		return &checkerdef.Result{
			Status:   checkerdef.StatusError,
			Duration: time.Since(start),
			Output: map[string]any{
				checkerdef.OutputKeyError: "no IP addresses found for host",
			},
		}
	}

	// Pick the address to dial. The choice — IPv4-first by default, or the
	// family this check pins via `ipVersion` — lives in checkerdef so every
	// checker makes it identically.
	targetIP, selectErr := checkerdef.SelectIPAddr(cfg.Host, addrs, checkerdef.IPVersionFrom(ctx))
	if selectErr != nil {
		return &checkerdef.Result{
			Status:   checkerdef.IPVersionFailureStatus(selectErr),
			Duration: time.Since(start),
			Output: map[string]any{
				checkerdef.OutputKeyError: selectErr.Error(),
			},
		}
	}

	// Execute TCP connection
	target := net.JoinHostPort(targetIP.String(), strconv.Itoa(cfg.Port))
	result := c.connect(ctx, &net.Dialer{}, target, cfg, timeout, tlsVerify)
	result.Duration = time.Since(start)

	// Add host info to output
	ipVersion := checkerdef.IPVersionOf(targetIP).String()

	if result.Output == nil {
		result.Output = make(map[string]any)
	}

	result.Output["host"] = targetIP.String()
	result.Output["port"] = cfg.Port
	result.Output[checkerdef.OutputKeyIPVersion] = ipVersion
	result.Output["tls_enabled"] = cfg.TLS

	// The connect helper knows the failure CLASS but not what it was dialing;
	// only this path resolved an address, so only this path can name it.
	checkerdef.LocateNetworkFailure(&result, cfg.Host, targetIP.String(), cfg.Port)

	return &result
}

// The dialer is the seam that makes tunneling work: a plain *net.Dialer for a
// direct check, the SSH port-forward dialer for a tunneled one. `target` is
// already-resolved `ip:port` in the former case and the raw configured
// `host:port` in the latter (remote-side resolution).
//
//nolint:funlen // TCP connection requires comprehensive logic
func (c *TCPChecker) connect(
	ctx context.Context,
	dialer checkerdef.ContextDialer,
	target string,
	cfg *TCPConfig,
	timeout time.Duration,
	tlsVerify bool,
) checkerdef.Result {
	// Create context with timeout. Its deadline bounds the WHOLE exchange —
	// dial, TLS handshake, write and the wait for the reply — instead of each
	// stage re-arming `now + timeout` for itself.
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

	// Track timing
	connectStart := time.Now()

	conn, err := dialer.DialContext(ctxWithTimeout, "tcp", target)
	if err != nil {
		// Determine if this is a timeout or connection refused
		if ctxWithTimeout.Err() != nil {
			out := checkerdef.Result{
				Status: checkerdef.StatusTimeout,
				Output: map[string]any{
					checkerdef.OutputKeyError: "connection timeout",
				},
			}
			out.SetNetworkFailure(checkerdef.NewNetworkFailure(
				checkerdef.ClassifyDialError(err, true), "", "", 0))

			return out
		}

		out := checkerdef.Result{
			Status: checkerdef.StatusDown,
			Output: map[string]any{
				checkerdef.OutputKeyError: fmt.Sprintf("connection refused: %v", err),
			},
		}
		out.SetNetworkFailure(checkerdef.NewNetworkFailure(
			checkerdef.ClassifyDialError(err, false), "", "", 0))

		return out
	}

	defer func() { _ = conn.Close() }()

	connectTime := time.Since(connectStart)

	metrics := map[string]any{
		"connection_time_ms": float64(connectTime.Microseconds()) / microsecondsPerMilli,
	}

	output := map[string]any{}

	// Upgrade to TLS if requested
	var tlsHandshakeTime time.Duration

	if cfg.TLS {
		tlsStart := time.Now()

		serverName := cfg.TLSServerName
		if serverName == "" {
			serverName = cfg.Host
		}

		tlsConfig := &tls.Config{
			ServerName:         serverName,
			InsecureSkipVerify: !tlsVerify,
		}

		tlsConn := tls.Client(conn, tlsConfig)

		if err := tlsConn.HandshakeContext(ctxWithTimeout); err != nil {
			out := checkerdef.Result{
				Status:  checkerdef.StatusDown,
				Metrics: metrics,
				Output: map[string]any{
					checkerdef.OutputKeyError: fmt.Sprintf("TLS handshake failed: %v", err),
				},
			}
			out.SetNetworkFailure(checkerdef.NewNetworkFailure(
				checkerdef.ClassifyTLSHandshakeError(err, ctxWithTimeout.Err() != nil), "", "", 0))

			return out
		}

		tlsHandshakeTime = time.Since(tlsStart)
		metrics["tls_handshake_time_ms"] = float64(tlsHandshakeTime.Microseconds()) / 1000.0

		// Get TLS connection state
		state := tlsConn.ConnectionState()
		output["tls_version"] = tlsVersionString(state.Version)
		output["tls_cipher_suite"] = tls.CipherSuiteName(state.CipherSuite)

		// Use TLS connection for subsequent operations
		conn = tlsConn
	}

	// Send the payload, then read until the expectation is satisfied — under
	// the single deadline computed above.
	if failure := exchange.Run(conn, metrics, output); failure != nil {
		return *failure
	}

	// Calculate total time
	totalTime := connectTime + tlsHandshakeTime
	metrics["total_time_ms"] = float64(totalTime.Microseconds()) / 1000.0

	return checkerdef.Result{
		Status:  checkerdef.StatusUp,
		Metrics: metrics,
		Output:  output,
	}
}

// executeTunneled runs the TCP probe through a tunnel dialer against the raw
// configured host:port. No `ip_version` is reported: the worker never learns
// which address the bastion picked, and inventing one would be a lie. The
// `tunneled` flag says why it is absent.
func (c *TCPChecker) executeTunneled(
	ctx context.Context,
	dialer checkerdef.ContextDialer,
	cfg *TCPConfig,
	timeout time.Duration,
	tlsVerify bool,
	start time.Time,
) *checkerdef.Result {
	target := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))

	result := c.connect(ctx, dialer, target, cfg, timeout, tlsVerify)
	result.Duration = time.Since(start)

	if result.Output == nil {
		result.Output = make(map[string]any)
	}

	result.Output[checkerdef.OutputKeyHost] = cfg.Host
	result.Output[checkerdef.OutputKeyPort] = cfg.Port
	result.Output["tunneled"] = true
	result.Output["tls_enabled"] = cfg.TLS

	// A tunneled probe failed on the FAR side of an SSH bastion. Tracing the
	// path from this worker would describe a route the probe never took, so the
	// class the connect helper recorded is dropped rather than published.
	checkerdef.DropNetworkFailure(&result)

	return &result
}

// tlsVersionString converts TLS version constant to string. The rendering
// itself lives in checkerdef so the JS `tcp.connect()` handle reports the
// same strings this check does.
func tlsVersionString(version uint16) string {
	return checkerdef.TLSVersionString(version)
}
