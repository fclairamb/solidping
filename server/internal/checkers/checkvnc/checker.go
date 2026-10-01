// Package checkvnc provides active VNC (RFB, RFC 6143) checks, with the Go
// standard library only.
//
// Without a password it performs the pre-auth handshake: version negotiation
// and the security-types list. That proves an RFB server answers and audits
// the offered authentication methods (requireAuth, on by default, fails a
// server that offers "None").
//
// With a password it authenticates with the strongest method both sides
// support (VeNCrypt X.509 TLS, Apple Remote Desktop, VNC authentication),
// reads ServerInit (desktop size and name) and optionally captures one frame
// as a PNG. It always attaches with the shared flag set, so an existing viewer
// is never disconnected.
package checkvnc

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkbrowser"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkvnc/config"
)

const (
	microsecondsPerMilli = 1000.0
	hoursPerDay          = 24

	outputFailureCode = "failure_code"
)

// VNCChecker implements the Checker interface for VNC checks. Zero-value usable.
type VNCChecker struct{}

// Type returns the check type identifier.
func (c *VNCChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeVNC
}

// Validate checks if the configuration is valid. Every rule lives in the light
// `config` sub-package so an offline validator (`sp checks validate`) can run it.
func (c *VNCChecker) Validate(spec *checkerdef.CheckSpec) error {
	return checkconfig.ValidateSpec(spec)
}

// run carries one execution's accumulating state.
type run struct {
	ctx     context.Context //nolint:containedctx // one execution's scope, never stored beyond it
	cfg     *VNCConfig
	start   time.Time
	metrics map[string]any
	output  map[string]any
	// cert is the VeNCrypt server certificate, graded by up().
	cert *x509.Certificate
}

// Execute performs the VNC check and returns the result.
func (c *VNCChecker) Execute(ctx context.Context, config checkerdef.Config) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*VNCConfig](config)
	if err != nil {
		return nil, err
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}

	port := cfg.Port
	if port == 0 {
		port = defaultPort
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	state := &run{
		ctx:     ctx,
		cfg:     cfg,
		start:   time.Now(),
		metrics: map[string]any{},
		output: map[string]any{
			checkerdef.OutputKeyHost: cfg.Host,
			checkerdef.OutputKeyPort: port,
		},
	}

	conn, err := dialTarget(ctx, cfg.Host, port, state.metrics)
	if err != nil {
		return state.fail(failure(FailureConnection, err, "connection failed: %v", err)), nil
	}

	defer func() { _ = conn.Close() }()

	handshakeStart := time.Now()

	offer, err := handshake(conn)
	if err != nil {
		return state.fail(classifyHandshakeError(err)), nil
	}

	state.metrics["handshake_ms"] = durationMs(time.Since(handshakeStart))
	state.output["rfbVersion"] = offer.version.String()
	state.output["serverRfbVersion"] = offer.serverVersion
	state.output["securityTypes"] = securityTypesOutput(offer.securityTypes)

	if cfg.RequiresAuth() && offer.offers(secNone) {
		return state.fail(failure(FailureNoAuthOffered, nil,
			"server offers security type None: anyone can attach without a password (offered: %s)",
			describeSecurityTypes(offer.securityTypes))), nil
	}

	if !cfg.Authenticated() {
		result := state.up()

		if checkerdef.ForcedCapture(ctx) {
			result.Diagnostics = &checkerdef.Diagnostics{
				ScreenshotError: "a screenshot needs a login: set a password on this check",
			}
		}

		return result, nil
	}

	return state.authenticated(conn, offer), nil
}

// authenticated runs the password path: auth, ServerInit, optional capture.
func (s *run) authenticated(conn net.Conn, offer *handshakeResult) *checkerdef.Result {
	authStart := time.Now()

	outcome, err := secure(s.ctx, conn, offer, s.cfg, s.redial)
	s.recordSecurity(outcome)

	if err != nil {
		return s.fail(err)
	}

	conn = outcome.conn
	defer func() { _ = conn.Close() }()

	init, err := clientServerInit(conn)
	if err != nil {
		return s.fail(err)
	}

	s.metrics["auth_ms"] = durationMs(time.Since(authStart))
	s.output["authenticated"] = true
	s.output["desktopName"] = init.name
	s.output["width"] = int(init.width)
	s.output["height"] = int(init.height)

	if !s.cfg.Screenshot && !checkerdef.ForcedCapture(s.ctx) {
		return s.up()
	}

	frameStart := time.Now()

	shot, err := captureFrame(conn, init)
	if err != nil {
		var classified *FailureError
		if !errors.As(err, &classified) {
			classified = failure(FailureNoFrame, err, "no frame received in time: %v", err)
		}

		if !s.cfg.Screenshot {
			// On-demand capture of a check that does not screenshot: the
			// login worked, the capture is detail, not a verdict.
			result := s.up()
			result.Diagnostics = &checkerdef.Diagnostics{ScreenshotError: classified.Error()}

			return result
		}

		// A configured screenshot that never arrives is a broken desktop.
		// Down rather than Timeout even when the deadline fired: the server
		// answered and authenticated, it just never drew.
		s.output[outputFailureCode] = string(classified.Code)
		s.output[checkerdef.OutputKeyError] = classified.Msg

		return &checkerdef.Result{
			Status:      checkerdef.StatusDown,
			Duration:    time.Since(s.start),
			Metrics:     s.metrics,
			Output:      s.output,
			Diagnostics: &checkerdef.Diagnostics{ScreenshotError: classified.Msg},
		}
	}

	s.metrics["first_frame_ms"] = durationMs(time.Since(frameStart))

	result := s.up()
	attachScreenshot(s.ctx, shot, result)

	return result
}

// redial opens a fresh connection for the fallback after an unusable
// VeNCrypt (see secure).
func (s *run) redial() (net.Conn, *handshakeResult, error) {
	port := s.cfg.Port
	if port == 0 {
		port = defaultPort
	}

	conn, err := dialTarget(s.ctx, s.cfg.Host, port, map[string]any{})
	if err != nil {
		return nil, nil, failure(FailureConnection, err, "connection failed: %v", err)
	}

	offer, err := handshake(conn)
	if err != nil {
		_ = conn.Close()

		return nil, nil, classifyHandshakeError(err)
	}

	return conn, offer, nil
}

// recordSecurity reports the negotiated method and, after VeNCrypt, the
// server certificate (kept even when the TLS handshake failed on it).
func (s *run) recordSecurity(outcome *authOutcome) {
	if outcome == nil {
		return
	}

	s.output["securityType"] = securityTypeName(outcome.secType)

	if outcome.subtype != 0 {
		s.output["vencryptSubtype"] = vencryptSubtypeName(outcome.subtype)
	}

	if outcome.cert == nil {
		return
	}

	s.cert = outcome.cert
	s.output["certSubject"] = outcome.cert.Subject.String()
	s.output["certIssuer"] = outcome.cert.Issuer.String()
	s.output["certExpiresAt"] = outcome.cert.NotAfter.Format(time.RFC3339)
	s.output["certSelfSigned"] = bytes.Equal(outcome.cert.RawSubject, outcome.cert.RawIssuer)
	s.metrics["days_remaining"] = certDaysRemaining(outcome.cert)
}

// certDaysRemaining returns whole days until the certificate's NotAfter.
func certDaysRemaining(cert *x509.Certificate) int {
	return int(time.Until(cert.NotAfter).Hours() / hoursPerDay)
}

// gradeCertificate applies the expiry thresholds (0 = disabled) to the
// VeNCrypt certificate, the SSL-style grading checkrdp uses. An expired
// certificate is always Down.
func gradeCertificate(cfg *VNCConfig, cert *x509.Certificate) (checkerdef.Status, string) {
	if cert == nil {
		return checkerdef.StatusUp, ""
	}

	days := certDaysRemaining(cert)

	switch {
	case time.Now().After(cert.NotAfter):
		return checkerdef.StatusDown, "server certificate has expired"
	case cfg.CriticalDays > 0 && days <= cfg.CriticalDays:
		return checkerdef.StatusDown, fmt.Sprintf(
			"server certificate expires in %d days (critical threshold: %d)", days, cfg.CriticalDays)
	case cfg.WarningDays > 0 && days <= cfg.WarningDays:
		return checkerdef.StatusWarning, fmt.Sprintf(
			"server certificate expires in %d days (warning threshold: %d)", days, cfg.WarningDays)
	default:
		return checkerdef.StatusUp, ""
	}
}

// captureFrame requests one full frame and PNG-encodes it.
func captureFrame(conn net.Conn, init *serverInit) ([]byte, error) {
	if err := requestFrame(conn, init.width, init.height); err != nil {
		return nil, err
	}

	img, err := readFrame(conn, init.width, init.height)
	if err != nil {
		return nil, err
	}

	return encodePNG(img)
}

// attachScreenshot hangs the capture on the result's Diagnostics, where the
// worker stores it and the dashboard lists it like any other capture.
func attachScreenshot(ctx context.Context, shot []byte, result *checkerdef.Result) {
	result.Diagnostics = &checkerdef.Diagnostics{}

	switch {
	case len(shot) == 0:
		result.Diagnostics.ScreenshotError = "the capture returned an empty image"
	case len(shot) > checkbrowser.MaxScreenshotBytes:
		result.Diagnostics.ScreenshotError = checkbrowser.OverCapMessage(len(shot))
	default:
		result.Diagnostics.Screenshot = &checkerdef.Screenshot{
			Image:      shot,
			Format:     checkerdef.ImageFormatPNG,
			CapturedAt: time.Now(),
		}
	}

	if result.Diagnostics.ScreenshotError != "" {
		slog.WarnContext(ctx, "vnc check: screenshot not kept", "reason", result.Diagnostics.ScreenshotError)
	}
}

// classifyHandshakeError maps a handshake error onto a stable failure.
func classifyHandshakeError(err error) *FailureError {
	var refused *ServerRefusedError

	switch {
	case errors.Is(err, errNotRFB):
		return failure(FailureNotRFB, err, "%v", err)
	case errors.As(err, &refused):
		if isLockoutReason(refused.Reason) {
			return failure(FailureTooManyAttempts, err, "%v", err)
		}

		return failure(FailureServerRefused, err, "%v", err)
	default:
		return failure(FailureProtocol, err, "RFB handshake failed: %v", err)
	}
}

// up builds a successful result, downgraded by the VeNCrypt certificate
// grade when it is expiring.
func (s *run) up() *checkerdef.Result {
	status, msg := gradeCertificate(s.cfg, s.cert)
	if status != checkerdef.StatusUp {
		s.output[outputFailureCode] = string(FailureCertExpiry)
		s.output[checkerdef.OutputKeyError] = msg
	}

	return &checkerdef.Result{
		Status: status, Duration: time.Since(s.start), Metrics: s.metrics, Output: s.output,
	}
}

// fail renders a failure: Timeout when the deadline fired, Down otherwise,
// with the stable code in `failure_code` when there is one.
func (s *run) fail(err error) *checkerdef.Result {
	var classified *FailureError
	if !errors.As(err, &classified) {
		classified = failure(FailureProtocol, err, "%v", err)
	}

	status := checkerdef.StatusDown
	msg := classified.Msg

	if isTimeout(s.ctx, classified.Cause) {
		status = checkerdef.StatusTimeout
		msg = "timeout: " + msg
	} else {
		s.output[outputFailureCode] = string(classified.Code)
	}

	s.output[checkerdef.OutputKeyError] = msg

	return &checkerdef.Result{
		Status: status, Duration: time.Since(s.start), Metrics: s.metrics, Output: s.output,
	}
}

// dialTarget opens the TCP connection and applies the context deadline to all
// subsequent reads/writes. It records connect_ms on success. A tunneled check
// dials through the tunnel; otherwise the egress guard applies.
func dialTarget(ctx context.Context, host string, port int, metrics map[string]any) (net.Conn, error) {
	dialer := checkerdef.GuardDialerOr(ctx, &net.Dialer{})

	connectStart := time.Now()
	address := net.JoinHostPort(host, strconv.Itoa(port))

	var (
		conn net.Conn
		err  error
	)

	if tunneled := checkerdef.TunnelDialerFrom(ctx); tunneled != nil {
		conn, err = tunneled.DialContext(ctx, "tcp", address)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", address)
	}

	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", address, err)
	}

	metrics["connect_ms"] = durationMs(time.Since(connectStart))

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	return conn, nil
}

// isTimeout reports whether the failure was a deadline expiry: the context
// itself, or an i/o timeout from the connection deadline derived from it.
func isTimeout(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}

	var netErr net.Error

	return errors.As(err, &netErr) && netErr.Timeout()
}

// durationMs converts a duration to milliseconds as a float.
func durationMs(d time.Duration) float64 {
	return float64(d.Microseconds()) / microsecondsPerMilli
}

// Ensure VNCChecker satisfies the Checker interface.
var _ checkerdef.Checker = (*VNCChecker)(nil)
