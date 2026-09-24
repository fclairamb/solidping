// Package checkrdp provides active Remote Desktop Protocol (RDP) checks.
//
// With no credentials it performs the pre-auth X.224 negotiation exchange
// (MS-RDPBCGR §2.2.1.1 / §2.2.1.2) — no session — which proves the RDP
// listener is alive, reveals the negotiated security protocol (optionally
// enforcing NLA), and, when a TLS-based protocol is selected, exposes the
// server certificate so expiry can be graded SSL-checker-style.
//
// With credentials set it performs a REAL interactive logon: CredSSP/NLA, the
// full connection sequence, a settle wait, an optional PNG capture, and an
// explicit session end (log off by default, or disconnect). Every such run
// has user-visible effects on the target — see checkrdp/config's
// AuthenticatedMinPeriod for why the period floor is 15 minutes.
package checkrdp

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkbrowser"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkrdp/config"
)

const microsecondsPerMilli = 1000.0

// RDPChecker implements the Checker interface for RDP checks. Zero-value
// usable; the authenticated path has its own test seams on the struct.
type RDPChecker struct {
	// authSession replaces the real authenticated session, for tests only.
	// Nil in production; spliced in AFTER the concurrency slot is acquired so
	// a test still exercises the slot it is meant to be a positive control
	// for (mirrors BrowserChecker.session).
	authSession func(
		ctx context.Context, cfg *RDPConfig, conn net.Conn,
	) (*authRunOutcome, error)

	// preDialedConn replaces the real TCP dial, for tests only. Nil in
	// production; the authenticated run hands the conn to the session so a
	// tunnel can carry it.
	preDialedConn func(ctx context.Context, cfg *RDPConfig) (net.Conn, error)
}

// authRunOutcome is what an authenticated run hands to the verdict layer.
type authRunOutcome struct {
	// screenshot is the PNG-encoded desktop capture, when one was requested
	// and a frame arrived in time. Nil otherwise.
	screenshot []byte
	// logoffErr is the explicit end-session failure, if any. Reported in the
	// output details but never turns an up run down: the logon itself
	// succeeded, and the session is being torn down by Close regardless.
	logoffErr error
	// shotError is a screenshot failure, when one was requested and the
	// capture did not happen. Detail, not a verdict — same rule as the
	// browser capture.
	shotError string
	// endSession is the mode the session was ended in — reported for
	// diagnosability.
	endSession string
}

// Type returns the check type identifier.
func (c *RDPChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeRDP
}

// Validate checks if the configuration is valid. Every rule lives in the light
// `config` sub-package so an offline validator (`sp checks validate`) can run it
// without linking this checker's execution client.
func (c *RDPChecker) Validate(spec *checkerdef.CheckSpec) error {
	return checkconfig.ValidateSpec(spec)
}

// Execute performs the RDP check and returns the result. An authenticated
// config (Username + Password) takes the interactive-logon path; everything
// else runs the pre-auth handshake exactly as before this spec.
func (c *RDPChecker) Execute(ctx context.Context, config checkerdef.Config) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*RDPConfig](config)
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

	start := time.Now()

	output := map[string]any{
		checkerdef.OutputKeyHost: cfg.Host,
		checkerdef.OutputKeyPort: port,
	}
	metrics := map[string]any{}

	if cfg.Authenticated() {
		return c.executeAuthenticated(ctx, cfg, start, metrics, output), nil
	}

	conn, err := dialTarget(ctx, cfg.Host, port, metrics)
	if err != nil {
		return errorResult(ctx, err, start, metrics, output, fmt.Sprintf("connection failed: %v", err)), nil
	}

	defer func() { _ = conn.Close() }()

	negotiation, err := negotiate(conn, metrics)
	if err != nil {
		return errorResult(ctx, err, start, metrics, output, fmt.Sprintf("RDP negotiation failed: %v", err)), nil
	}

	return c.classify(ctx, conn, cfg, start, negotiation, metrics, output), nil
}

// executeAuthenticated runs the interactive-logon path: slot, connect,
// CredSSP/NLA logon, settle wait, optional capture, explicit session end.
func (c *RDPChecker) executeAuthenticated(
	ctx context.Context,
	cfg *RDPConfig,
	start time.Time,
	metrics, output map[string]any,
) *checkerdef.Result {
	output["authenticated"] = true

	// The concurrency cap lives here, around the whole session, and its wait
	// counts against the check's own timeout — an execution that never gets a
	// slot reports a timeout rather than queueing invisibly (the same rule as
	// the browser slots).
	release, acquired := acquireRDPSlot(ctx)
	if !acquired {
		output[checkerdef.OutputKeyError] =
			"timed out waiting for a free RDP slot (at most 4 RDP logons run at a time on one worker)"

		return &checkerdef.Result{
			Status: checkerdef.StatusTimeout, Duration: time.Since(start), Metrics: metrics, Output: output,
		}
	}
	defer release()

	var conn net.Conn
	if c.preDialedConn != nil {
		var err error
		conn, err = c.preDialedConn(ctx, cfg)
		if err != nil {
			return errorResult(ctx, err, start, metrics, output, fmt.Sprintf("connection failed: %v", err))
		}

		defer func() { _ = conn.Close() }()

		metrics["connect_ms"] = durationMs(time.Since(start))
	} else {
		port := cfg.Port
		if port == 0 {
			port = defaultPort
		}

		var err error
		conn, err = dialTarget(ctx, cfg.Host, port, metrics)
		if err != nil {
			return errorResult(ctx, err, start, metrics, output, fmt.Sprintf("connection failed: %v", err))
		}
	}

	outcome, err := c.runAuthSession(ctx, cfg, conn)
	if err != nil {
		return authErrorResult(ctx, err, start, metrics, output)
	}

	// The logon succeeded. Everything after this point — capture, session
	// end — can add detail to the output but must not turn the verdict down.
	output["end_session"] = outcome.endSession
	metrics["logon_ms"] = durationMs(time.Since(start))

	if outcome.logoffErr != nil {
		output["logoff_error"] = fmt.Sprintf("%s: %v", msgLogoffFailed, outcome.logoffErr)
	}

	if outcome.shotError != "" {
		output["screenshot_error"] = outcome.shotError
	}

	if cfg.Screenshot {
		c.attachScreenshot(cfg, outcome.screenshot, output)
	}

	return &checkerdef.Result{
		Status: checkerdef.StatusUp, Duration: time.Since(start), Metrics: metrics, Output: output,
	}
}

// runAuthSession opens the session through the seam (or the real path) and
// finishes it per EndSession. The returned outcome describes the END of the
// session; the caller only gets here when the LOGON succeeded.
func (c *RDPChecker) runAuthSession(ctx context.Context, cfg *RDPConfig, conn net.Conn) (*authRunOutcome, error) {
	if c.authSession != nil {
		return c.authSession(ctx, cfg, conn)
	}

	session, err := openRDPSession(ctx, cfg, conn)
	if err != nil {
		return nil, err
	}

	outcome := &authRunOutcome{endSession: cfg.EndSession}

	if cfg.Screenshot {
		if shot, shotErr := session.screenshotPNG(); shotErr != nil {
			// A capture failure is detail, not a verdict: the logon itself
			// worked. The browser capture follows the same rule.
			outcome.logoffErr = nil

			outcome.shotError = shotErr.Error()
		} else {
			outcome.screenshot = shot
		}
	}

	if outcome.endSession == "" {
		outcome.endSession = checkconfig.EndSessionLogoff
	}

	if outcome.endSession == checkconfig.EndSessionDisconnect {
		session.disconnect()
	} else {
		outcome.logoffErr = session.logoff()
	}

	return outcome, nil
}

// authErrorResult renders an authenticated-run failure with its distinct
// machine code in the output.
func authErrorResult(
	ctx context.Context, err error, start time.Time, metrics, output map[string]any,
) *checkerdef.Result {
	var authErr *errAuthFailure

	var reason authFailure
	if errors.As(err, &authErr) {
		reason = authErr.reason
		output["failure_code"] = string(reason)
	}

	status := checkerdef.StatusDown
	if isTimeout(ctx, err) && reason == "" {
		status = checkerdef.StatusTimeout
	}

	output[checkerdef.OutputKeyError] = err.Error()

	return &checkerdef.Result{
		Status: status, Duration: time.Since(start), Metrics: metrics, Output: output,
	}
}

// attachScreenshot hangs the capture on the result through the same
// Diagnostics path the browser screenshots use, with the same size cap. A
// capture over the cap is dropped, never truncated; a failed capture is
// reported in the output only.
func (c *RDPChecker) attachScreenshot(cfg *RDPConfig, shot []byte, output map[string]any) {
	_ = cfg

	if len(shot) == 0 {
		return
	}

	if len(shot) > checkbrowser.MaxScreenshotBytes {
		output["screenshot_dropped"] = fmt.Sprintf(
			"capture of %d bytes exceeds the %d-byte cap", len(shot), checkbrowser.MaxScreenshotBytes)

		return
	}

	output["screenshot_bytes"] = len(shot)
}

// dialTarget opens the TCP connection and applies the context deadline to all
// subsequent reads/writes. It records connect_ms on success.
//
// When the check is tunneled the dialer on the context wins and local name
// resolution is skipped — the direct-tcpip request carries the hostname and
// the bastion resolves it (the same rule every tunnel-capable checker follows).
func dialTarget(ctx context.Context, host string, port int, metrics map[string]any) (net.Conn, error) {
	dialer := &net.Dialer{}

	connectStart := time.Now()

	var (
		conn net.Conn
		err  error
	)

	address := net.JoinHostPort(host, strconv.Itoa(port))

	if tunneled := checkerdef.TunnelDialerFrom(ctx); tunneled != nil {
		conn, err = tunneled.DialContext(ctx, "tcp", address)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", address)
	}

	if err != nil {
		return nil, err
	}

	metrics["connect_ms"] = durationMs(time.Since(connectStart))

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	return conn, nil
}

// negotiate writes the Connection Request and reads + parses the Connection
// Confirm, never reading past the declared (capped) TPKT length. It records
// handshake_ms on success.
func negotiate(conn net.Conn, metrics map[string]any) (*negotiationResult, error) {
	handshakeStart := time.Now()

	if _, err := conn.Write(buildConnectionRequest()); err != nil {
		return nil, fmt.Errorf("write connection request: %w", err)
	}

	header := make([]byte, tpktHeaderLen)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, fmt.Errorf("read response header: %w", err)
	}

	if header[0] != tpktVersion {
		return nil, errBadTPKTVersion
	}

	declaredLen := int(header[2])<<8 | int(header[3])
	if declaredLen < minResponseLen || declaredLen > maxResponseLen {
		return nil, errBadTPKTLength
	}

	rest := make([]byte, declaredLen-tpktHeaderLen)
	if _, err := io.ReadFull(conn, rest); err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	packet := make([]byte, 0, declaredLen)
	packet = append(packet, header...)
	packet = append(packet, rest...)

	result, err := parseConnectionConfirm(packet)
	if err != nil {
		return nil, err
	}

	metrics["handshake_ms"] = durationMs(time.Since(handshakeStart))

	return result, nil
}

// classify maps the parsed negotiation outcome (plus the optional TLS
// certificate inspection) onto a check result.
func (c *RDPChecker) classify(
	ctx context.Context,
	conn net.Conn,
	cfg *RDPConfig,
	start time.Time,
	negotiation *negotiationResult,
	metrics map[string]any,
	output map[string]any,
) *checkerdef.Result {
	if negotiation.Failure {
		output["failure_code"] = failureCodeName(negotiation.FailureCode)

		return errorResult(ctx, nil, start, metrics, output,
			"server rejected negotiation: "+failureCodeName(negotiation.FailureCode))
	}

	output["selected_protocol"] = protocolName(negotiation.SelectedProtocol)

	if negotiation.HasNegotiation {
		output["server_flags"] = serverFlagNames(negotiation.ServerFlags)
	}

	if cfg.RequireNLA && !isNLA(negotiation.SelectedProtocol) {
		output[checkerdef.OutputKeyError] = "NLA required but server selected " +
			protocolName(negotiation.SelectedProtocol)

		return &checkerdef.Result{
			Status: checkerdef.StatusDown, Duration: time.Since(start), Metrics: metrics, Output: output,
		}
	}

	if isTLSBased(negotiation.SelectedProtocol) {
		return c.inspectCertificate(ctx, conn, cfg, start, metrics, output)
	}

	return &checkerdef.Result{
		Status: checkerdef.StatusUp, Duration: time.Since(start), Metrics: metrics, Output: output,
	}
}

// inspectCertificate completes the TLS handshake the negotiated protocol
// mandates (without sending any CredSSP afterwards) and grades the leaf
// certificate's expiry. RDP certificates are routinely self-signed or from an
// internal CA, so the chain is deliberately NOT validated — the leaf is read
// with InsecureSkipVerify and inspected manually.
func (c *RDPChecker) inspectCertificate(
	ctx context.Context,
	conn net.Conn,
	cfg *RDPConfig,
	start time.Time,
	metrics map[string]any,
	output map[string]any,
) *checkerdef.Result {
	tlsStart := time.Now()

	// InsecureSkipVerify is leaf-only inspection by design: RDP certificates
	// are routinely self-signed, so chain validation would false-alarm.
	tlsConn := tls.Client(conn, &tls.Config{
		ServerName:         cfg.Host,
		InsecureSkipVerify: true,
	})

	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return errorResult(ctx, err, start, metrics, output, fmt.Sprintf("TLS handshake failed: %v", err))
	}

	metrics["tls_handshake_ms"] = durationMs(time.Since(tlsStart))

	state := tlsConn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return errorResult(ctx, nil, start, metrics, output, "no peer certificates presented")
	}

	leaf := state.PeerCertificates[0]
	daysRemaining := certDaysRemaining(leaf)

	metrics["days_remaining"] = daysRemaining
	output["cert_subject"] = leaf.Subject.CommonName
	output["cert_issuer"] = leaf.Issuer.CommonName
	output["cert_expires_at"] = leaf.NotAfter.Format(time.RFC3339)
	output["cert_self_signed"] = bytes.Equal(leaf.RawSubject, leaf.RawIssuer)

	status := gradeCertExpiry(cfg, daysRemaining, output)

	return &checkerdef.Result{
		Status: status, Duration: time.Since(start), Metrics: metrics, Output: output,
	}
}

// gradeCertExpiry applies the optional expiry thresholds (0 = disabled). An
// already-expired certificate is always Down, thresholds or not.
func gradeCertExpiry(cfg *RDPConfig, daysRemaining int, output map[string]any) checkerdef.Status {
	switch {
	case daysRemaining < 0:
		output[checkerdef.OutputKeyError] = "server certificate has expired"

		return checkerdef.StatusDown
	case cfg.CriticalDays > 0 && daysRemaining <= cfg.CriticalDays:
		output[checkerdef.OutputKeyError] = fmt.Sprintf(
			"server certificate expires in %d days (critical threshold: %d)", daysRemaining, cfg.CriticalDays,
		)

		return checkerdef.StatusDown
	case cfg.WarningDays > 0 && daysRemaining <= cfg.WarningDays:
		output[checkerdef.OutputKeyError] = fmt.Sprintf(
			"server certificate expires in %d days (warning threshold: %d)", daysRemaining, cfg.WarningDays,
		)

		return checkerdef.StatusWarning
	default:
		return checkerdef.StatusUp
	}
}

// certDaysRemaining returns whole days until the certificate's NotAfter.
func certDaysRemaining(cert *x509.Certificate) int {
	return int(time.Until(cert.NotAfter).Hours() / 24)
}

// errorResult maps a failure to Timeout (when the context deadline fired, or
// when the connection deadline derived from it did — the two timers race by a
// hair, so both count) or Down (refused, reset, malformed, non-RDP,
// negotiation failure).
func errorResult(
	ctx context.Context, err error, start time.Time, metrics, output map[string]any, msg string,
) *checkerdef.Result {
	status := checkerdef.StatusDown
	if isTimeout(ctx, err) {
		status = checkerdef.StatusTimeout
		msg = "timeout: " + msg
	}

	output[checkerdef.OutputKeyError] = msg

	return &checkerdef.Result{
		Status:   status,
		Duration: time.Since(start),
		Metrics:  metrics,
		Output:   output,
	}
}

// isTimeout reports whether the failure was a deadline expiry — either the
// context itself, or an i/o timeout from the connection deadline that was set
// from that same context (SetDeadline can fire marginally before the context
// timer is marked done).
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

// Ensure RDPChecker satisfies the Checker interface.
var _ checkerdef.Checker = (*RDPChecker)(nil)
