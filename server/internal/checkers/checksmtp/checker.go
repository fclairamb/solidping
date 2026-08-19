// Package checksmtp provides SMTP server connectivity checks.
package checksmtp

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/textproto"
	"os"
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
	cfg, err := checkerdef.AssertConfig[*SMTPConfig](config)
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
			// A genuine resolve failure stays StatusError, as it always has; an
			// address-family failure gets the shared verdict so it reads the
			// same here as on a tcp/http check.
			return &checkerdef.Result{
				Status:   checkerdef.ResolveFailureStatus(resErr, checkerdef.StatusError),
				Duration: time.Since(start),
				Output:   map[string]any{checkerdef.OutputKeyError: resErr.Error()},
			}, nil
		}

		hostLabel = targetIP.String()
	}

	target := net.JoinHostPort(hostLabel, strconv.Itoa(params.port))
	useImplicitTLS := params.port == implicitTLSPort && !cfg.StartTLS

	metrics := map[string]any{}
	output := map[string]any{
		checkerdef.OutputKeyHost: hostLabel,
		checkerdef.OutputKeyPort: params.port,
	}

	if tunneled {
		output["tunneled"] = true
	}

	// Establish connection
	conn, connTime, err := c.dial(ctx, dialer, target, params.serverName, useImplicitTLS, cfg.TLSVerify)
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
		if ctx.Err() != nil || errors.Is(err, os.ErrDeadlineExceeded) {
			return timeoutResult(start), nil
		}

		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: time.Since(start),
			Metrics:  metrics,
			Output: map[string]any{
				checkerdef.OutputKeyHost:  hostLabel,
				checkerdef.OutputKeyPort:  params.port,
				checkerdef.OutputKeyError: fmt.Sprintf("greeting rejected: %d %s", code, greeting),
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
				checkerdef.OutputKeyHost:  hostLabel,
				checkerdef.OutputKeyPort:  params.port,
				checkerdef.OutputKeyError: fmt.Sprintf("greeting does not contain expected substring %q", cfg.ExpectGreeting),
				"greeting":                greeting,
			},
		}, nil
	}

	// Send EHLO
	ehloStart := time.Now()

	caps, err := c.sendEHLO(textConn, params.ehloDomain)
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, os.ErrDeadlineExceeded) {
			return timeoutResult(start), nil
		}

		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: time.Since(start),
			Metrics:  metrics,
			Output: map[string]any{
				checkerdef.OutputKeyHost:  hostLabel,
				checkerdef.OutputKeyPort:  params.port,
				checkerdef.OutputKeyError: fmt.Sprintf("EHLO rejected: %v", err),
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
			if ctx.Err() != nil || errors.Is(err, os.ErrDeadlineExceeded) {
				return timeoutResult(start), nil
			}

			return &checkerdef.Result{
				Status:   checkerdef.StatusDown,
				Duration: time.Since(start),
				Metrics:  metrics,
				Output: map[string]any{
					checkerdef.OutputKeyHost:  hostLabel,
					checkerdef.OutputKeyPort:  params.port,
					checkerdef.OutputKeyError: err.Error(),
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
			if ctx.Err() != nil || errors.Is(authErr, os.ErrDeadlineExceeded) {
				return timeoutResult(start), nil
			}

			return &checkerdef.Result{
				Status:   checkerdef.StatusDown,
				Duration: time.Since(start),
				Metrics:  metrics,
				Output: map[string]any{
					checkerdef.OutputKeyHost:  hostLabel,
					checkerdef.OutputKeyPort:  params.port,
					checkerdef.OutputKeyError: fmt.Sprintf("AUTH failed: %v", authErr),
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
				checkerdef.OutputKeyHost:  hostLabel,
				checkerdef.OutputKeyPort:  params.port,
				checkerdef.OutputKeyError: "AUTH not advertised by server",
			},
		}, nil
	}

	// Send-mode: submit a system-generated probe email to the paired email
	// check's tokenized address instead of the normal bare QUIT. The result
	// reflects submission only (spec 2026-08-19-04) — this replaces the
	// generic "handshake succeeded" Up result below with a send-specific one.
	if cfg.SendEmail {
		return c.executeSendMode(ctx, textConn, cfg, hostLabel, params.port, start, metrics), nil
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

// probeMessageSubject and probeMessageBody are fixed, system-generated text —
// there is no user-supplied subject or body, ever (spec 2026-08-19-04): a
// customizable message here would turn the feature into a spam template
// engine. Only the attribution headers vary per send.
const (
	probeMessageSubject = "SolidPing delivery probe"
	probeMessageBody    = "This is an automated delivery probe sent by SolidPing to verify SMTP " +
		"submission and delivery. No action is required.\r\n"
	headerSourceCheckUID = "X-SolidPing-Check"
	headerSentAt         = "X-SolidPing-Sent-At"
	smtpCodeOK           = 250
	smtpCodeStartData    = 354
)

// executeSendMode issues MAIL FROM / RCPT TO / DATA / QUIT for a send-mode
// SMTP check, after the normal EHLO/STARTTLS/AUTH sequence above already
// succeeded. It replaces the generic post-handshake Up result: the check's
// own result reflects submission only — a 250 after DATA is Up (with
// submission_ms in metrics), any rejection is Down with the server's reply.
//
// When no delivery recipient was resolved onto ctx, this returns an explicit
// StatusError result rather than silently skipping the send — see
// checkerdef.SMTPDeliveryRecipientFrom for why that can happen (a deleted
// reference at dispatch time, or an Execute call outside normal job
// dispatch such as a JS check's sub-check helper).
func (c *SMTPChecker) executeSendMode(
	ctx context.Context,
	textConn *textproto.Conn,
	cfg *SMTPConfig,
	hostLabel string,
	port int,
	start time.Time,
	metrics map[string]any,
) *checkerdef.Result {
	baseOutput := map[string]any{
		checkerdef.OutputKeyHost: hostLabel,
		checkerdef.OutputKeyPort: port,
	}

	info, ok := checkerdef.SMTPDeliveryRecipientFrom(ctx)
	if !ok {
		baseOutput[checkerdef.OutputKeyError] = "send_email is set but no delivery recipient was resolved; " +
			"this config was executed outside normal job dispatch"

		return &checkerdef.Result{
			Status:   checkerdef.StatusError,
			Duration: time.Since(start),
			Metrics:  metrics,
			Output:   baseOutput,
		}
	}

	sendStart := time.Now()

	if err := doSendEmail(textConn, cfg.MailFrom, info); err != nil {
		_ = textConn.PrintfLine("QUIT")

		baseOutput[checkerdef.OutputKeyError] = err.Error()

		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: time.Since(start),
			Metrics:  metrics,
			Output:   baseOutput,
		}
	}

	metrics["submission_ms"] = durationMs(time.Since(sendStart))
	metrics["total_time_ms"] = durationMs(time.Since(start))

	_ = textConn.PrintfLine("QUIT")

	baseOutput["submitted"] = true

	return &checkerdef.Result{
		Status:   checkerdef.StatusUp,
		Duration: time.Since(start),
		Metrics:  metrics,
		Output:   baseOutput,
	}
}

// doSendEmail issues MAIL FROM / RCPT TO / DATA and writes the fixed,
// system-generated probe message. Returns an error naming which step was
// rejected (and by what server reply) so executeSendMode's Down result stays
// diagnostic.
func doSendEmail(textConn *textproto.Conn, mailFrom string, info checkerdef.SMTPDeliveryInfo) error {
	if _, err := smtpCommand(textConn, smtpCodeOK, "MAIL FROM:<%s>", mailFrom); err != nil {
		return fmt.Errorf("MAIL FROM rejected: %w", err)
	}

	if _, err := smtpCommand(textConn, smtpCodeOK, "RCPT TO:<%s>", info.Recipient); err != nil {
		return fmt.Errorf("RCPT TO rejected: %w", err)
	}

	if _, err := smtpCommand(textConn, smtpCodeStartData, "DATA"); err != nil {
		return fmt.Errorf("DATA rejected: %w", err)
	}

	dw := textConn.DotWriter()
	_, writeErr := dw.Write([]byte(buildProbeMessage(mailFrom, info.Recipient, info.SourceCheckUID, time.Now())))
	closeErr := dw.Close()

	if writeErr != nil {
		return fmt.Errorf("failed to write message body: %w", writeErr)
	}

	if closeErr != nil {
		return fmt.Errorf("failed to send message body: %w", closeErr)
	}

	if _, _, err := textConn.ReadResponse(smtpCodeOK); err != nil {
		return fmt.Errorf("message rejected: %w", err)
	}

	return nil
}

// smtpCommand sends an SMTP command and reads the expected response code,
// mirroring the Cmd/StartResponse/ReadResponse/EndResponse sequence used by
// sendEHLO/doAUTH/doSTARTTLS above.
func smtpCommand(textConn *textproto.Conn, expectCode int, format string, args ...any) (string, error) {
	id, err := textConn.Cmd(format, args...)
	if err != nil {
		return "", fmt.Errorf("failed to send command: %w", err)
	}

	textConn.StartResponse(id)
	defer textConn.EndResponse(id)

	_, msg, err := textConn.ReadResponse(expectCode)
	if err != nil {
		return "", err
	}

	return msg, nil
}

// buildProbeMessage renders the fixed, system-generated probe email. Only the
// From/To addresses and the two attribution headers vary per send — subject
// and body are constants (see probeMessageSubject/probeMessageBody).
func buildProbeMessage(mailFrom, recipient, sourceCheckUID string, sentAt time.Time) string {
	var b strings.Builder

	fmt.Fprintf(&b, "From: <%s>\r\n", mailFrom)
	fmt.Fprintf(&b, "To: <%s>\r\n", recipient)
	fmt.Fprintf(&b, "Subject: %s\r\n", probeMessageSubject)
	fmt.Fprintf(&b, "Date: %s\r\n", sentAt.Format(time.RFC1123Z))
	fmt.Fprintf(&b, "%s: %s\r\n", headerSourceCheckUID, sourceCheckUID)
	fmt.Fprintf(&b, "%s: %s\r\n", headerSentAt, sentAt.Format(time.RFC3339))
	b.WriteString("\r\n")
	b.WriteString(probeMessageBody)

	return b.String()
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
	dialer checkerdef.ContextDialer,
	target, serverName string,
	implicitTLS, tlsVerify bool,
) (net.Conn, time.Duration, error) {
	connectStart := time.Now()

	conn, err := dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		return nil, time.Since(connectStart), err
	}

	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
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

func handleDialError(ctx context.Context, err error, start time.Time) *checkerdef.Result {
	if ctx.Err() != nil {
		return timeoutResult(start)
	}

	return &checkerdef.Result{
		Status:   checkerdef.StatusDown,
		Duration: time.Since(start),
		Output:   map[string]any{checkerdef.OutputKeyError: fmt.Sprintf("connection failed: %v", err)},
	}
}

func timeoutResult(start time.Time) *checkerdef.Result {
	return &checkerdef.Result{
		Status:   checkerdef.StatusTimeout,
		Duration: time.Since(start),
		Output:   map[string]any{checkerdef.OutputKeyError: "connection timeout"},
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
