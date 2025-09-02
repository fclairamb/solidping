package checkimap

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const microsecondsPerMilli = 1000.0

var (
	errNoIPAddresses    = errors.New("no IP addresses found for host")
	errSTARTTLSRejected = errors.New("STARTTLS rejected by server")
	errLOGINFailed      = errors.New("LOGIN failed")
)

// IMAPChecker implements the Checker interface for IMAP server checks.
type IMAPChecker struct{}

// Type returns the check type identifier.
func (c *IMAPChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeIMAP
}

// Validate checks if the configuration is valid.
func (c *IMAPChecker) Validate(spec *checkerdef.CheckSpec) error {
	cfg := &IMAPConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	if spec.Name == "" {
		spec.Name = "IMAP: " + cfg.Host
	}

	if spec.Slug == "" {
		spec.Slug = "imap-" + cfg.Host
	}

	return nil
}

// execParams holds resolved execution parameters with defaults applied.
type execParams struct {
	host       string
	port       int
	timeout    time.Duration
	serverName string
}

func newExecParams(cfg *IMAPConfig) execParams {
	params := execParams{
		host:       cfg.Host,
		port:       cfg.Port,
		serverName: cfg.TLSServerName,
	}

	if params.port == 0 {
		if cfg.TLS {
			params.port = implicitTLSPort
		} else {
			params.port = defaultPort
		}
	}

	params.timeout = cfg.Timeout
	if params.timeout == 0 {
		params.timeout = defaultTimeout
	}

	if params.serverName == "" {
		params.serverName = cfg.Host
	}

	return params
}

// Execute performs the IMAP check and returns the result.
//
//nolint:funlen,cyclop // IMAP protocol flow requires comprehensive logic
func (c *IMAPChecker) Execute(
	ctx context.Context, config checkerdef.Config,
) (*checkerdef.Result, error) {
	cfg, ok := config.(*IMAPConfig)
	if !ok {
		return nil, ErrInvalidConfigType
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

	metrics := map[string]any{}
	output := map[string]any{
		"host": targetIP.String(),
		"port": params.port,
	}

	// Establish connection
	conn, connTime, err := c.dial(ctx, target, params.serverName, cfg.TLS, cfg.TLSVerify)
	if err != nil {
		return handleDialError(ctx, err, start), nil
	}

	defer func() { _ = conn.Close() }()

	metrics["connection_time_ms"] = durationMs(connTime)

	if cfg.TLS {
		if tlsConn, ok := conn.(*tls.Conn); ok {
			state := tlsConn.ConnectionState()
			output["tls_version"] = tlsVersionString(state.Version)
			output["tls_cipher"] = tls.CipherSuiteName(state.CipherSuite)
		}
	}

	// Wrap in textproto for IMAP line protocol
	textConn := textproto.NewConn(conn)

	// Read greeting (expect "* OK" prefix)
	greetingStart := time.Now()

	greeting, err := textConn.ReadLine()
	if err != nil {
		if ctx.Err() != nil {
			return timeoutResult(start), nil
		}

		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: time.Since(start),
			Metrics:  metrics,
			Output: map[string]any{
				"host":  targetIP.String(),
				"port":  params.port,
				"error": fmt.Sprintf("failed to read greeting: %v", err),
			},
		}, nil
	}

	metrics["greeting_time_ms"] = durationMs(time.Since(greetingStart))
	output["greeting"] = greeting

	if !strings.HasPrefix(greeting, "* OK") {
		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: time.Since(start),
			Metrics:  metrics,
			Output: map[string]any{
				"host":     targetIP.String(),
				"port":     params.port,
				"error":    "greeting does not start with * OK",
				"greeting": greeting,
			},
		}, nil
	}

	// Check expected greeting
	if cfg.ExpectGreeting != "" && !strings.Contains(greeting, cfg.ExpectGreeting) {
		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: time.Since(start),
			Metrics:  metrics,
			Output: map[string]any{
				"host": targetIP.String(),
				"port": params.port,
				"error": fmt.Sprintf(
					"greeting does not contain expected substring %q",
					cfg.ExpectGreeting,
				),
				"greeting": greeting,
			},
		}, nil
	}

	// STARTTLS if requested
	if cfg.StartTLS {
		tlsConn, err := c.doSTARTTLS(
			ctx, textConn, conn, params.serverName, cfg.TLSVerify,
		)
		if err != nil {
			if ctx.Err() != nil {
				return timeoutResult(start), nil
			}

			return &checkerdef.Result{
				Status:   checkerdef.StatusDown,
				Duration: time.Since(start),
				Metrics:  metrics,
				Output: map[string]any{
					"host":  targetIP.String(),
					"port":  params.port,
					"error": err.Error(),
				},
			}, nil
		}

		state := tlsConn.ConnectionState()
		output["tls_version"] = tlsVersionString(state.Version)
		output["tls_cipher"] = tls.CipherSuiteName(state.CipherSuite)

		// Re-wrap with new textproto conn
		textConn = textproto.NewConn(tlsConn)
	}

	// Perform LOGIN if credentials provided
	if cfg.Username != "" {
		loginStart := time.Now()

		if err := c.doLogin(ctx, textConn, cfg); err != nil {
			if ctx.Err() != nil {
				return timeoutResult(start), nil
			}

			return &checkerdef.Result{
				Status:   checkerdef.StatusDown,
				Duration: time.Since(start),
				Metrics:  metrics,
				Output: map[string]any{
					"host":  targetIP.String(),
					"port":  params.port,
					"error": err.Error(),
				},
			}, nil
		}

		metrics["login_time_ms"] = durationMs(time.Since(loginStart))
		output["authenticated"] = true
	}

	// Send LOGOUT
	_ = textConn.PrintfLine("a003 LOGOUT")

	metrics["total_time_ms"] = durationMs(time.Since(start))

	return &checkerdef.Result{
		Status:   checkerdef.StatusUp,
		Duration: time.Since(start),
		Metrics:  metrics,
		Output:   output,
	}, nil
}

// doLogin performs IMAP LOGIN authentication.
func (c *IMAPChecker) doLogin(
	_ context.Context, textConn *textproto.Conn, cfg *IMAPConfig,
) error {
	err := textConn.PrintfLine("a002 LOGIN %s %s", cfg.Username, cfg.Password)
	if err != nil {
		return fmt.Errorf("failed to send LOGIN: %w", err)
	}

	response, err := textConn.ReadLine()
	if err != nil {
		return fmt.Errorf("LOGIN response read failed: %w", err)
	}

	if !strings.HasPrefix(response, "a002 OK") {
		return fmt.Errorf("%w: %s", errLOGINFailed, response)
	}

	return nil
}

// doSTARTTLS performs the STARTTLS upgrade using IMAP STARTTLS command.
func (c *IMAPChecker) doSTARTTLS(
	ctx context.Context,
	textConn *textproto.Conn,
	conn net.Conn,
	serverName string,
	tlsVerify bool,
) (*tls.Conn, error) {
	err := textConn.PrintfLine("a001 STARTTLS")
	if err != nil {
		return nil, fmt.Errorf("failed to send STARTTLS: %w", err)
	}

	response, err := textConn.ReadLine()
	if err != nil {
		return nil, fmt.Errorf("STARTTLS response read failed: %w", err)
	}

	if !strings.HasPrefix(response, "a001 OK") {
		return nil, fmt.Errorf("%w: %s", errSTARTTLSRejected, response)
	}

	tlsConn := tls.Client(conn, &tls.Config{
		ServerName:         serverName,
		InsecureSkipVerify: !tlsVerify,
	})

	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return nil, fmt.Errorf("STARTTLS handshake failed: %w", err)
	}

	return tlsConn, nil
}

// dial establishes a connection, optionally with implicit TLS.
func (c *IMAPChecker) dial(
	ctx context.Context,
	target, serverName string,
	implicitTLS, tlsVerify bool,
) (net.Conn, time.Duration, error) {
	connectStart := time.Now()
	dialer := &net.Dialer{}

	conn, err := dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		return nil, time.Since(connectStart), err
	}

	if implicitTLS {
		tlsConn := tls.Client(conn, &tls.Config{
			ServerName:         serverName,
			InsecureSkipVerify: !tlsVerify,
		})

		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()

			return nil, time.Since(connectStart), fmt.Errorf("TLS handshake failed: %w", err)
		}

		return tlsConn, time.Since(connectStart), nil
	}

	return conn, time.Since(connectStart), nil
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

func handleDialError(ctx context.Context, err error, start time.Time) *checkerdef.Result {
	if ctx.Err() != nil {
		return timeoutResult(start)
	}

	return &checkerdef.Result{
		Status:   checkerdef.StatusDown,
		Duration: time.Since(start),
		Output:   map[string]any{"error": fmt.Sprintf("connection failed: %v", err)},
	}
}

func timeoutResult(start time.Time) *checkerdef.Result {
	return &checkerdef.Result{
		Status:   checkerdef.StatusTimeout,
		Duration: time.Since(start),
		Output:   map[string]any{"error": "connection timeout"},
	}
}

func durationMs(dur time.Duration) float64 {
	return float64(dur.Microseconds()) / microsecondsPerMilli
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
