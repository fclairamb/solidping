package checksmtp

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/version"
)

const microsecondsPerMilli = 1000.0

var (
	errSTARTTLSNotAdvertised = errors.New("STARTTLS not advertised by server")
	errNoIPAddresses         = errors.New("no IP addresses found for host")
)

// SMTPChecker implements the Checker interface for SMTP server checks.
type SMTPChecker struct{}

// Type returns the check type identifier.
func (c *SMTPChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeSMTP
}

// Validate checks if the configuration is valid.
func (c *SMTPChecker) Validate(spec *checkerdef.CheckSpec) error {
	cfg := &SMTPConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	if spec.Name == "" {
		spec.Name = "SMTP: " + cfg.Host
	}

	if spec.Slug == "" {
		spec.Slug = "smtp-" + cfg.Host
	}

	return nil
}

// execParams holds resolved execution parameters with defaults applied.
type execParams struct {
	host       string
	port       int
	timeout    time.Duration
	serverName string
	ehloDomain string
}

func newExecParams(cfg *SMTPConfig) execParams {
	params := execParams{
		host:       cfg.Host,
		port:       cfg.Port,
		serverName: cfg.TLSServerName,
		ehloDomain: cfg.EHLODomain,
	}

	if params.port == 0 {
		params.port = defaultPort
	}

	params.timeout = cfg.Timeout
	if params.timeout == 0 {
		params.timeout = defaultTimeout
	}

	if params.serverName == "" {
		params.serverName = cfg.Host
	}

	if params.ehloDomain == "" {
		params.ehloDomain = version.UserAgent
	}

	return params
}

// Execute performs the SMTP check and returns the result.
//
//nolint:funlen,cyclop,gocognit // SMTP protocol flow requires comprehensive logic
func (c *SMTPChecker) Execute(ctx context.Context, config checkerdef.Config) (*checkerdef.Result, error) {
	cfg, ok := config.(*SMTPConfig)
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
	useImplicitTLS := params.port == implicitTLSPort && !cfg.StartTLS

	metrics := map[string]any{}
	output := map[string]any{
		"host": targetIP.String(),
		"port": params.port,
	}

	// Establish connection
	conn, connTime, err := c.dial(ctx, target, params.serverName, useImplicitTLS, cfg.TLSVerify)
	if err != nil {
		return handleDialError(ctx, err, start), nil
	}

	defer func() { _ = conn.Close() }()

	metrics["connection_time_ms"] = durationMs(connTime)

	if useImplicitTLS {
		if tlsConn, ok := conn.(*tls.Conn); ok {
			state := tlsConn.ConnectionState()
			output["tls_version"] = tlsVersionString(state.Version)
			output["tls_cipher"] = tls.CipherSuiteName(state.CipherSuite)
		}
	}

	// Wrap in textproto for SMTP line protocol
	textConn := textproto.NewConn(conn)

	// Read 220 greeting
	greetingStart := time.Now()

	code, greeting, err := textConn.ReadResponse(220)
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
				"error": fmt.Sprintf("greeting rejected: %d %s", code, greeting),
			},
		}, nil
	}

	metrics["greeting_time_ms"] = durationMs(time.Since(greetingStart))
	output["greeting"] = greeting

	// Check expected greeting
	if cfg.ExpectGreeting != "" && !strings.Contains(greeting, cfg.ExpectGreeting) {
		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: time.Since(start),
			Metrics:  metrics,
			Output: map[string]any{
				"host":     targetIP.String(),
				"port":     params.port,
				"error":    fmt.Sprintf("greeting does not contain expected substring %q", cfg.ExpectGreeting),
				"greeting": greeting,
			},
		}, nil
	}

	// Send EHLO
	ehloStart := time.Now()

	caps, err := c.sendEHLO(textConn, params.ehloDomain)
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
				"error": fmt.Sprintf("EHLO rejected: %v", err),
			},
		}, nil
	}

	metrics["ehlo_time_ms"] = durationMs(time.Since(ehloStart))
	output["ehlo_capabilities"] = caps.names
	output["auth_mechanisms"] = caps.authMechanisms

	// STARTTLS if requested
	if cfg.StartTLS {
		starttlsStart := time.Now()

		tlsConn, err := c.doSTARTTLS(ctx, textConn, conn, params.serverName, cfg.TLSVerify, caps)
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

		metrics["starttls_time_ms"] = durationMs(time.Since(starttlsStart))

		state := tlsConn.ConnectionState()
		output["tls_version"] = tlsVersionString(state.Version)
		output["tls_cipher"] = tls.CipherSuiteName(state.CipherSuite)

		// Re-wrap with new textproto conn and re-EHLO per RFC
		textConn = textproto.NewConn(tlsConn)

		newCaps, err := c.sendEHLO(textConn, params.ehloDomain)
		if err == nil {
			caps = newCaps
			output["ehlo_capabilities"] = caps.names
			output["auth_mechanisms"] = caps.authMechanisms
		}
	}

	// Perform SMTP AUTH if credentials provided
	if cfg.Username != "" {
		authStart := time.Now()

		authErr := c.doAUTH(textConn, cfg.Username, cfg.Password)
		if authErr != nil {
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
					"error": fmt.Sprintf("AUTH failed: %v", authErr),
				},
			}, nil
		}

		metrics["auth_time_ms"] = durationMs(time.Since(authStart))
		output["authenticated"] = true
	}

	// Check AUTH if requested
	if cfg.CheckAuth && !caps.hasAuth {
		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: time.Since(start),
			Metrics:  metrics,
			Output: map[string]any{
				"host":  targetIP.String(),
				"port":  params.port,
				"error": "AUTH not advertised by server",
			},
		}, nil
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

// doAUTH performs SMTP AUTH PLAIN authentication.
func (c *SMTPChecker) doAUTH(
	textConn *textproto.Conn,
	username, password string,
) error {
	// AUTH PLAIN: base64("\x00" + username + "\x00" + password)
	authStr := base64.StdEncoding.EncodeToString(
		[]byte("\x00" + username + "\x00" + password),
	)

	id, err := textConn.Cmd("AUTH PLAIN %s", authStr)
	if err != nil {
		return fmt.Errorf("failed to send AUTH: %w", err)
	}

	textConn.StartResponse(id)
	defer textConn.EndResponse(id)

	_, _, err = textConn.ReadResponse(235)
	if err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	return nil
}

// ehloCapabilities holds parsed EHLO response data.
type ehloCapabilities struct {
	names          []string
	authMechanisms []string
	hasStartTLS    bool
	hasAuth        bool
}

// sendEHLO sends the EHLO command and parses the response capabilities.
func (c *SMTPChecker) sendEHLO(textConn *textproto.Conn, domain string) (ehloCapabilities, error) {
	id, err := textConn.Cmd("EHLO %s", domain)
	if err != nil {
		return ehloCapabilities{}, fmt.Errorf("failed to send EHLO: %w", err)
	}

	textConn.StartResponse(id)
	defer textConn.EndResponse(id)

	_, msg, err := textConn.ReadResponse(250)
	if err != nil {
		return ehloCapabilities{}, fmt.Errorf("EHLO failed: %w", err)
	}

	return parseEHLOResponse(msg), nil
}

// parseEHLOResponse parses the multi-line EHLO response into capabilities.
func parseEHLOResponse(msg string) ehloCapabilities {
	caps := ehloCapabilities{}

	lines := strings.Split(msg, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parts := strings.SplitN(line, " ", 2)
		capName := strings.ToUpper(parts[0])

		caps.names = append(caps.names, capName)

		switch capName {
		case "STARTTLS":
			caps.hasStartTLS = true
		case "AUTH":
			caps.hasAuth = true
			if len(parts) > 1 {
				caps.authMechanisms = strings.Fields(parts[1])
			}
		}
	}

	return caps
}

// doSTARTTLS performs the STARTTLS upgrade.
func (c *SMTPChecker) doSTARTTLS(
	ctx context.Context,
	textConn *textproto.Conn,
	conn net.Conn,
	serverName string,
	tlsVerify bool,
	caps ehloCapabilities,
) (*tls.Conn, error) {
	if !caps.hasStartTLS {
		return nil, errSTARTTLSNotAdvertised
	}

	id, err := textConn.Cmd("STARTTLS")
	if err != nil {
		return nil, fmt.Errorf("failed to send STARTTLS: %w", err)
	}

	textConn.StartResponse(id)

	_, _, err = textConn.ReadResponse(220)

	textConn.EndResponse(id)

	if err != nil {
		return nil, fmt.Errorf("STARTTLS rejected: %w", err)
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
func (c *SMTPChecker) dial(
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

func durationMs(d time.Duration) float64 {
	return float64(d.Microseconds()) / microsecondsPerMilli
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
