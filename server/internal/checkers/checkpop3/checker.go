// Package checkpop3 provides POP3 server availability checks.
package checkpop3

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
	errNoIPAddresses = errors.New("no IP addresses found for host")
	errSTLSRejected  = errors.New("STLS rejected by server")
	errUSERRejected  = errors.New("USER rejected")
	errPASSRejected  = errors.New("PASS rejected")
)

// POP3Checker implements the Checker interface for POP3 server checks.
type POP3Checker struct{}

// Type returns the check type identifier.
func (c *POP3Checker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypePOP3
}

// Validate checks if the configuration is valid.
func (c *POP3Checker) Validate(spec *checkerdef.CheckSpec) error {
	cfg := &POP3Config{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	if spec.Name == "" {
		spec.Name = "POP3: " + cfg.Host
	}

	if spec.Slug == "" {
		spec.Slug = "pop3-" + cfg.Host
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

func newExecParams(cfg *POP3Config) execParams {
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

// Execute performs the POP3 check and returns the result.
//
//nolint:funlen,cyclop // POP3 protocol flow requires comprehensive logic
func (c *POP3Checker) Execute(
	ctx context.Context, config checkerdef.Config,
) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*POP3Config](config)
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

	// Wrap in textproto for POP3 line protocol
	textConn := textproto.NewConn(conn)

	// Read greeting (expect "+OK" prefix)
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

	if !strings.HasPrefix(greeting, "+OK") {
		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: time.Since(start),
			Metrics:  metrics,
			Output: map[string]any{
				"host":     targetIP.String(),
				"port":     params.port,
				"error":    "greeting does not start with +OK",
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
		tlsConn, err := c.doSTARTTLS(ctx, textConn, conn, params.serverName, cfg.TLSVerify)
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

	// Perform USER/PASS auth if credentials provided
	if cfg.Username != "" {
		authStart := time.Now()

		if err := c.doAuth(ctx, textConn, cfg); err != nil {
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

		metrics["auth_time_ms"] = durationMs(time.Since(authStart))
		output["authenticated"] = true
	}

	// Send QUIT
	_ = textConn.PrintfLine("QUIT")

	metrics["total_time_ms"] = durationMs(time.Since(start))

	return &checkerdef.Result{
		Status:   checkerdef.StatusUp,
		Duration: time.Since(start),
		Metrics:  metrics,
		Output:   output,
	}, nil
}

// doAuth performs POP3 USER/PASS authentication.
func (c *POP3Checker) doAuth(
	_ context.Context, textConn *textproto.Conn, cfg *POP3Config,
) error {
	// Send USER
	if err := textConn.PrintfLine("USER %s", cfg.Username); err != nil {
		return fmt.Errorf("failed to send USER: %w", err)
	}

	userResp, err := textConn.ReadLine()
	if err != nil {
		return fmt.Errorf("USER response read failed: %w", err)
	}

	if !strings.HasPrefix(userResp, "+OK") {
		return fmt.Errorf("%w: %s", errUSERRejected, userResp)
	}

	// Send PASS
	passErr := textConn.PrintfLine("PASS %s", cfg.Password)
	if passErr != nil {
		return fmt.Errorf("failed to send PASS: %w", passErr)
	}

	passResp, passErr := textConn.ReadLine()
	if passErr != nil {
		return fmt.Errorf("PASS response read failed: %w", passErr)
	}

	if !strings.HasPrefix(passResp, "+OK") {
		return fmt.Errorf("%w: %s", errPASSRejected, passResp)
	}

	return nil
}

// doSTARTTLS performs the STARTTLS upgrade using POP3 STLS command.
func (c *POP3Checker) doSTARTTLS(
	ctx context.Context,
	textConn *textproto.Conn,
	conn net.Conn,
	serverName string,
	tlsVerify bool,
) (*tls.Conn, error) {
	err := textConn.PrintfLine("STLS")
	if err != nil {
		return nil, fmt.Errorf("failed to send STLS: %w", err)
	}

	response, err := textConn.ReadLine()
	if err != nil {
		return nil, fmt.Errorf("STLS response read failed: %w", err)
	}

	if !strings.HasPrefix(response, "+OK") {
		return nil, fmt.Errorf("%w: %s", errSTLSRejected, response)
	}

	tlsConn := tls.Client(conn, &tls.Config{
		ServerName:         serverName,
		InsecureSkipVerify: !tlsVerify,
	})

	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return nil, fmt.Errorf("STLS handshake failed: %w", err)
	}

	return tlsConn, nil
}

// dial establishes a connection, optionally with implicit TLS.
func (c *POP3Checker) dial(
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
