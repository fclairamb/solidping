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

// resolveHost resolves the hostname and picks the address to dial. The pick
// itself lives in checkerdef.SelectIPAddr — IPv4-first by default, or the family
// the check pinned via `ipVersion` — so this checker cannot drift from the
// others.
func resolveHost(ctx context.Context, host string) (net.IP, error) {
	addrs, err := checkerdef.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve hostname: %w", err)
	}

	if len(addrs) == 0 {
		return nil, errNoIPAddresses
	}

	return checkerdef.SelectIPAddr(host, addrs, checkerdef.IPVersionFrom(ctx))
}

// tlsConnect establishes a TCP connection and performs a TLS handshake.
type tlsConnResult struct {
	connectionTime time.Duration
	handshakeTime  time.Duration
	state          tls.ConnectionState
	conn           net.Conn
}

func tlsConnect(
	ctx context.Context, dialer checkerdef.ContextDialer, target, serverName string,
) (*tlsConnResult, error) {
	connectStart := time.Now()

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

func certDaysRemaining(cert *x509.Certificate) int {
	return int(time.Until(cert.NotAfter).Hours() / 24)
}

// buildCertOutput writes the leaf-certificate fields (kept for back-compat) and
// returns the leaf's days-remaining.
func buildCertOutput(cert *x509.Certificate, output map[string]any) int {
	daysRemaining := certDaysRemaining(cert)

	output["subject"] = cert.Subject.CommonName
	output["issuer"] = cert.Issuer.CommonName
	output["not_before"] = cert.NotBefore.Format(time.RFC3339)
	output["not_after"] = cert.NotAfter.Format(time.RFC3339)
	output["days_remaining"] = daysRemaining
	output["serial_number"] = cert.SerialNumber.String()
	output["dns_names"] = cert.DNSNames

	return daysRemaining
}

// chainCertReport is one entry in the reported certificate chain.
type chainCertReport struct {
	Subject       string `json:"subject"`
	Issuer        string `json:"issuer"`
	NotAfter      string `json:"notAfter"`
	DaysRemaining int    `json:"daysRemaining"`
}

// chainExpiry summarizes the whole presented chain: its per-cert report plus the
// soonest-expiring cert (the minimum days-remaining drives the status decision).
type chainExpiry struct {
	chain       []chainCertReport
	minDays     int
	soonestCert *x509.Certificate
}

// analyzeChain iterates the presented certificates (leaf + intermediates),
// building the per-cert report and tracking the minimum days-remaining across
// the whole chain — an intermediate that expires before the leaf is a real,
// common failure that leaf-only inspection misses.
func analyzeChain(certs []*x509.Certificate) chainExpiry {
	report := make([]chainCertReport, 0, len(certs))
	result := chainExpiry{minDays: certDaysRemaining(certs[0]), soonestCert: certs[0]}

	for _, cert := range certs {
		days := certDaysRemaining(cert)
		report = append(report, chainCertReport{
			Subject:       cert.Subject.CommonName,
			Issuer:        cert.Issuer.CommonName,
			NotAfter:      cert.NotAfter.Format(time.RFC3339),
			DaysRemaining: days,
		})

		if days < result.minDays {
			result.minDays = days
			result.soonestCert = cert
		}
	}

	result.chain = report

	return result
}

type execParams struct {
	port         int
	warningDays  int
	criticalDays int
	timeout      time.Duration
	serverName   string
	host         string
}

func newExecParams(cfg *SSLConfig) execParams {
	warning, critical := cfg.effectiveThresholds()

	params := execParams{
		port:         cfg.Port,
		warningDays:  warning,
		criticalDays: critical,
		timeout:      cfg.Timeout,
		serverName:   cfg.ServerName,
		host:         cfg.Host,
	}

	if params.port == 0 {
		params.port = defaultPort
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

	// Tunneled: dial the raw host:port through the bastion and SKIP local name
	// resolution — the bastion resolves the hostname (the whole point for
	// private names). The untunneled path is byte-for-byte unchanged.
	tunnelDialer := checkerdef.TunnelDialerFrom(ctx)
	tunneled := tunnelDialer != nil

	var dialer checkerdef.ContextDialer = &net.Dialer{}

	hostLabel := params.host

	if tunneled {
		dialer = tunnelDialer
	} else {
		targetIP, resErr := resolveHost(ctx, params.host)
		if resErr != nil {
			return &checkerdef.Result{
				Status:   checkerdef.StatusError,
				Duration: time.Since(start),
				Output:   map[string]any{checkerdef.OutputKeyError: resErr.Error()},
			}, nil
		}

		hostLabel = targetIP.String()
	}

	target := net.JoinHostPort(hostLabel, strconv.Itoa(params.port))

	result, err := tlsConnect(ctx, dialer, target, params.serverName)
	if err != nil {
		return c.handleConnectError(ctx, err, result, hostLabel, params.port, start), nil
	}

	defer func() { _ = result.conn.Close() }()

	return c.buildResult(result, hostLabel, params, tunneled, start), nil
}

func (c *SSLChecker) buildResult(
	result *tlsConnResult, hostLabel string, params execParams, tunneled bool, start time.Time,
) *checkerdef.Result {
	duration := time.Since(start)

	metrics := map[string]any{
		"connection_time_ms": durationMs(result.connectionTime),
		"handshake_time_ms":  durationMs(result.handshakeTime),
		"duration_ms":        durationMs(duration),
	}

	output := map[string]any{
		checkerdef.OutputKeyHost: hostLabel,
		checkerdef.OutputKeyPort: params.port,
		"tls_version":            tlsVersionString(result.state.Version),
	}

	if tunneled {
		output["tunneled"] = true
	}

	if len(result.state.PeerCertificates) == 0 {
		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: duration,
			Metrics:  metrics,
			Output: map[string]any{
				checkerdef.OutputKeyHost:  hostLabel,
				checkerdef.OutputKeyPort:  params.port,
				checkerdef.OutputKeyError: "no peer certificates presented",
			},
		}
	}

	// Leaf fields (back-compat). days_remaining metric becomes the chain minimum below.
	buildCertOutput(result.state.PeerCertificates[0], output)

	expiry := analyzeChain(result.state.PeerCertificates)
	metrics["days_remaining"] = expiry.minDays

	status, severity := checkerdef.GradedExpiryStatus(expiry.minDays, params.warningDays, params.criticalDays)

	output["chain"] = expiry.chain
	output["chainLength"] = len(result.state.PeerCertificates)
	output["chainVerified"] = len(result.state.VerifiedChains) > 0
	output["soonestExpiring"] = map[string]any{
		"subject":       expiry.soonestCert.Subject.CommonName,
		"daysRemaining": expiry.minDays,
	}
	output["severity"] = severity

	if severity == checkerdef.SeverityWarning {
		output["certExpiryWarning"] = true
	}

	return &checkerdef.Result{
		Status:   status,
		Duration: duration,
		Metrics:  metrics,
		Output:   output,
	}
}

func (c *SSLChecker) handleConnectError(
	ctx context.Context, err error, result *tlsConnResult, hostLabel string, port int, start time.Time,
) *checkerdef.Result {
	duration := time.Since(start)

	// TCP connection failed
	if result == nil {
		if ctx.Err() != nil {
			return &checkerdef.Result{
				Status:   checkerdef.StatusTimeout,
				Duration: duration,
				Output:   map[string]any{checkerdef.OutputKeyError: "connection timeout"},
			}
		}

		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: duration,
			Output:   map[string]any{checkerdef.OutputKeyError: fmt.Sprintf("connection failed: %v", err)},
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
			checkerdef.OutputKeyHost:  hostLabel,
			checkerdef.OutputKeyPort:  port,
			checkerdef.OutputKeyError: fmt.Sprintf("TLS handshake failed: %v", err),
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
