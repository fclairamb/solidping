// Package checktcp provides TCP port connectivity checks.
package checktcp

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	// Default values from spec.
	defaultTimeout       = 5 * time.Second
	defaultTLSVerify     = true
	maxReadSize          = 4 * 1024 // 4KB buffer for reading response
	maxOutputDataSize    = 1024     // 1KB max for output data
	microsecondsPerMilli = 1000.0   // Conversion factor for microseconds to milliseconds
)

// TCPChecker implements the Checker interface for TCP connection checks.
type TCPChecker struct{}

// Type returns the check type identifier.
func (c *TCPChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeTCP
}

// Validate checks if the configuration is valid.
func (c *TCPChecker) Validate(spec *checkerdef.CheckSpec) error {
	cfg := &TCPConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	// Validate Host
	if cfg.Host == "" {
		return checkerdef.NewConfigError("host", "is required")
	}

	// Validate Port
	if cfg.Port == 0 {
		return checkerdef.NewConfigError("port", "is required")
	}

	if cfg.Port < 1 || cfg.Port > 65535 {
		return checkerdef.NewConfigErrorf("port", "must be between 1 and 65535, got %d", cfg.Port)
	}

	// Validate Timeout (> 0 and <= 60s) - check the original value if set
	if cfg.Timeout != 0 && (cfg.Timeout <= 0 || cfg.Timeout > 60*time.Second) {
		return checkerdef.NewConfigErrorf("timeout", "must be > 0 and <= 60s, got %s", cfg.Timeout.String())
	}

	return nil
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

	// Resolve hostname
	resolver := &net.Resolver{}

	addrs, err := resolver.LookupIPAddr(ctx, cfg.Host)
	if err != nil {
		return &checkerdef.Result{
			Status:   checkerdef.StatusError,
			Duration: time.Since(start),
			Output: map[string]any{
				"error": fmt.Sprintf("failed to resolve hostname: %v", err),
			},
		}, nil
	}

	if len(addrs) == 0 {
		return &checkerdef.Result{
			Status:   checkerdef.StatusError,
			Duration: time.Since(start),
			Output: map[string]any{
				"error": "no IP addresses found for host",
			},
		}, nil
	}

	// Use any of the resolved addresses (prefer IPv4 if available)
	var targetIP net.IP

	var isIPv6 bool

	for i := range addrs {
		if addrs[i].IP.To4() != nil {
			targetIP = addrs[i].IP
			isIPv6 = false

			break
		}
	}

	// Fall back to IPv6 if no IPv4 found
	if targetIP == nil {
		targetIP = addrs[0].IP
		isIPv6 = targetIP.To4() == nil
	}

	// Execute TCP connection
	result := c.connect(ctx, targetIP, isIPv6, cfg, timeout, tlsVerify)
	result.Duration = time.Since(start)

	// Add host info to output
	ipVersion := "ipv4"
	if isIPv6 {
		ipVersion = "ipv6"
	}

	if result.Output == nil {
		result.Output = make(map[string]any)
	}

	result.Output["host"] = targetIP.String()
	result.Output["port"] = cfg.Port
	result.Output["ip_version"] = ipVersion
	result.Output["tls_enabled"] = cfg.TLS

	return &result, nil
}

// connect performs the actual TCP connection operation.
//
//nolint:funlen,cyclop,gocognit // TCP connection requires comprehensive logic
func (c *TCPChecker) connect(
	ctx context.Context,
	targetIP net.IP,
	_ bool,
	cfg *TCPConfig,
	timeout time.Duration,
	tlsVerify bool,
) checkerdef.Result {
	// Create context with timeout
	ctxWithTimeout, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Prepare target address
	target := net.JoinHostPort(targetIP.String(), strconv.Itoa(cfg.Port))

	// Track timing
	connectStart := time.Now()

	// Create TCP connection
	dialer := &net.Dialer{}

	conn, err := dialer.DialContext(ctxWithTimeout, "tcp", target)
	if err != nil {
		// Determine if this is a timeout or connection refused
		if ctxWithTimeout.Err() != nil {
			return checkerdef.Result{
				Status: checkerdef.StatusTimeout,
				Output: map[string]any{
					"error": "connection timeout",
				},
			}
		}

		return checkerdef.Result{
			Status: checkerdef.StatusDown,
			Output: map[string]any{
				"error": fmt.Sprintf("connection refused: %v", err),
			},
		}
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
			return checkerdef.Result{
				Status:  checkerdef.StatusDown,
				Metrics: metrics,
				Output: map[string]any{
					"error": fmt.Sprintf("TLS handshake failed: %v", err),
				},
			}
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

	// Send data if specified
	var bytesSent int

	if cfg.SendData != "" {
		if err := conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
			return checkerdef.Result{
				Status:  checkerdef.StatusError,
				Metrics: metrics,
				Output: map[string]any{
					"error": fmt.Sprintf("failed to set write deadline: %v", err),
				},
			}
		}

		n, err := conn.Write([]byte(cfg.SendData))
		if err != nil {
			return checkerdef.Result{
				Status:  checkerdef.StatusDown,
				Metrics: metrics,
				Output: map[string]any{
					"error": fmt.Sprintf("failed to send data: %v", err),
				},
			}
		}

		bytesSent = n
		metrics["bytes_sent"] = bytesSent
	}

	// Read response if expect_data is specified
	var bytesReceived int

	var receivedData string

	//nolint:nestif // Conditional logic complexity is acceptable for data validation
	if cfg.ExpectData != "" || cfg.SendData != "" {
		if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
			return checkerdef.Result{
				Status:  checkerdef.StatusError,
				Metrics: metrics,
				Output: map[string]any{
					"error": fmt.Sprintf("failed to set read deadline: %v", err),
				},
			}
		}

		buf := make([]byte, maxReadSize)

		n, err := conn.Read(buf)
		if err != nil && !errors.Is(err, io.EOF) {
			// If we sent data and expected a response, this is a failure
			if cfg.ExpectData != "" {
				return checkerdef.Result{
					Status:  checkerdef.StatusDown,
					Metrics: metrics,
					Output: map[string]any{
						"error": fmt.Sprintf("failed to read response: %v", err),
					},
				}
			}
			// Otherwise, it's just a note in the output
		} else {
			bytesReceived = n
			metrics["bytes_received"] = bytesReceived

			// Store first 1KB of received data
			if n > maxOutputDataSize {
				receivedData = string(buf[:maxOutputDataSize])
			} else {
				receivedData = string(buf[:n])
			}

			output["received_data"] = receivedData
		}

		// Validate expected data if specified
		if cfg.ExpectData != "" {
			if !strings.Contains(receivedData, cfg.ExpectData) {
				return checkerdef.Result{
					Status:  checkerdef.StatusDown,
					Metrics: metrics,
					Output: map[string]any{
						"error":         fmt.Sprintf("expected data not found: '%s'", cfg.ExpectData),
						"received_data": receivedData,
					},
				}
			}
		}
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

// tlsVersionString converts TLS version constant to string.
func tlsVersionString(version uint16) string {
	switch version {
	case tls.VersionTLS10:
		return "TLS 1.0"
	case tls.VersionTLS11:
		return "TLS 1.1"
	case tls.VersionTLS12:
		return "TLS 1.2"
	case tls.VersionTLS13:
		return "TLS 1.3"
	default:
		return fmt.Sprintf("Unknown (0x%04x)", version)
	}
}
