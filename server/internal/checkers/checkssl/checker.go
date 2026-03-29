// Package checkssl provides SSL/TLS certificate validation checks.
package checkssl

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const microsecondsPerMilli = 1000.0

var errNoIPAddresses = errors.New("no IP addresses found for host")

// SSLChecker implements the Checker interface for SSL certificate checks.
type SSLChecker struct{}

// Type returns the check type identifier.
func (c *SSLChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeSSL
}

// Validate checks if the configuration is valid.
func (c *SSLChecker) Validate(spec *checkerdef.CheckSpec) error {
	cfg := &SSLConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return checkerdef.NewConfigError("host", err.Error())
	}

	if spec.Name == "" {
		spec.Name = "SSL: " + cfg.Host
	}

	if spec.Slug == "" {
		spec.Slug = "ssl-" + cfg.Host
	}

	return nil
}

// resolveHost resolves the hostname to an IP address, preferring IPv4.
func resolveHost(ctx context.Context, host string) (net.IP, error) {
	resolver := &net.Resolver{}

	addrs, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve hostname: %w", err)
	}

	if len(addrs) == 0 {
		return nil, errNoIPAddresses
	}

	for i := range addrs {
		if addrs[i].IP.To4() != nil {
			return addrs[i].IP, nil
		}
	}

	return addrs[0].IP, nil
}

// tlsConnect establishes a TCP connection and performs a TLS handshake.
type tlsConnResult struct {
	connectionTime time.Duration
	handshakeTime  time.Duration
	state          tls.ConnectionState
	conn           net.Conn
}

func tlsConnect(ctx context.Context, target, serverName string) (*tlsConnResult, error) {
	connectStart := time.Now()

	dialer := &net.Dialer{}

	conn, err := dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}

	connectionTime := time.Since(connectStart)

	tlsStart := time.Now()

	tlsConn := tls.Client(conn, &tls.Config{
		ServerName: serverName,
	})

	if err := tlsConn.HandshakeContext(ctx); err != nil {
		_ = conn.Close()

		return &tlsConnResult{
			connectionTime: connectionTime,
			handshakeTime:  time.Since(tlsStart),
		}, fmt.Errorf("handshake: %w", err)
	}

	return &tlsConnResult{
		connectionTime: connectionTime,
		handshakeTime:  time.Since(tlsStart),
		state:          tlsConn.ConnectionState(),
		conn:           conn,
	}, nil
}

func durationMs(d time.Duration) float64 {
	return float64(d.Microseconds()) / microsecondsPerMilli
}

func buildCertOutput(cert *x509.Certificate, output map[string]any) int {
	daysRemaining := int(time.Until(cert.NotAfter).Hours() / 24)

	output["subject"] = cert.Subject.CommonName
	output["issuer"] = cert.Issuer.CommonName
	output["not_before"] = cert.NotBefore.Format(time.RFC3339)
	output["not_after"] = cert.NotAfter.Format(time.RFC3339)
	output["days_remaining"] = daysRemaining
	output["serial_number"] = cert.SerialNumber.String()
	output["dns_names"] = cert.DNSNames

	return daysRemaining
}

type execParams struct {
	port       int
	threshold  int
	timeout    time.Duration
	serverName string
	host       string
}

func newExecParams(cfg *SSLConfig) execParams {
	params := execParams{
		port:       cfg.Port,
		threshold:  cfg.ThresholdDays,
		timeout:    cfg.Timeout,
		serverName: cfg.ServerName,
		host:       cfg.Host,
	}

	if params.port == 0 {
		params.port = defaultPort
	}

	if params.threshold <= 0 {
		params.threshold = defaultThresholdDays
	}

	if params.timeout == 0 {
		params.timeout = defaultTimeout
	}

	if params.serverName == "" {
		params.serverName = cfg.Host
	}

	return params
}

// Execute performs the SSL certificate check and returns the result.
func (c *SSLChecker) Execute(ctx context.Context, config checkerdef.Config) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*SSLConfig](config)
	if err != nil {
		return nil, err
	}

	params := newExecParams(cfg)

	ctx, cancel := context.WithTimeout(ctx, params.timeout)
	defer cancel()

	start := time.Now()

	targetIP, err := resolveHost(ctx, params.host)
	if err != nil {
		return &checkerdef.Result{
			Status:   checkerdef.StatusError,
			Duration: time.Since(start),
			Output:   map[string]any{"error": err.Error()},
		}, nil
	}

	target := net.JoinHostPort(targetIP.String(), strconv.Itoa(params.port))

	result, err := tlsConnect(ctx, target, params.serverName)
	if err != nil {
		return c.handleConnectError(ctx, err, result, targetIP, params.port, start), nil
	}

	defer func() { _ = result.conn.Close() }()

	return c.buildResult(result, targetIP, params, start), nil
}

func (c *SSLChecker) buildResult(
	result *tlsConnResult, targetIP net.IP, params execParams, start time.Time,
) *checkerdef.Result {
	duration := time.Since(start)

	metrics := map[string]any{
		"connection_time_ms": durationMs(result.connectionTime),
		"handshake_time_ms":  durationMs(result.handshakeTime),
		"duration_ms":        durationMs(duration),
	}

	output := map[string]any{
		"host":        targetIP.String(),
		"port":        params.port,
		"tls_version": tlsVersionString(result.state.Version),
	}

	if len(result.state.PeerCertificates) == 0 {
		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: duration,
			Metrics:  metrics,
			Output: map[string]any{
				"host":  targetIP.String(),
				"port":  params.port,
				"error": "no peer certificates presented",
			},
		}
	}

	daysRemaining := buildCertOutput(result.state.PeerCertificates[0], output)
	metrics["days_remaining"] = daysRemaining

	status := checkerdef.StatusUp
	if daysRemaining <= params.threshold {
		status = checkerdef.StatusDown
	}

	return &checkerdef.Result{
		Status:   status,
		Duration: duration,
		Metrics:  metrics,
		Output:   output,
	}
}

func (c *SSLChecker) handleConnectError(
	ctx context.Context, err error, result *tlsConnResult, targetIP net.IP, port int, start time.Time,
) *checkerdef.Result {
	duration := time.Since(start)

	// TCP connection failed
	if result == nil {
		if ctx.Err() != nil {
			return &checkerdef.Result{
				Status:   checkerdef.StatusTimeout,
				Duration: duration,
				Output:   map[string]any{"error": "connection timeout"},
			}
		}

		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: duration,
			Output:   map[string]any{"error": fmt.Sprintf("connection failed: %v", err)},
		}
	}

	// TLS handshake failed
	return &checkerdef.Result{
		Status:   checkerdef.StatusDown,
		Duration: duration,
		Metrics: map[string]any{
			"connection_time_ms": durationMs(result.connectionTime),
			"duration_ms":        durationMs(duration),
		},
		Output: map[string]any{
			"host":  targetIP.String(),
			"port":  port,
			"error": fmt.Sprintf("TLS handshake failed: %v", err),
		},
	}
}

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
