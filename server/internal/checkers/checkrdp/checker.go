// Package checkrdp provides active Remote Desktop Protocol (RDP) checks. It
// performs the pre-auth X.224 negotiation exchange (MS-RDPBCGR §2.2.1.1 /
// §2.2.1.2) — no credentials, no session — which proves the RDP listener is
// alive, reveals the negotiated security protocol (optionally enforcing NLA),
// and, when a TLS-based protocol is selected, exposes the server certificate
// so expiry can be graded SSL-checker-style.
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

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const microsecondsPerMilli = 1000.0

// RDPChecker implements the Checker interface for RDP pre-auth checks.
type RDPChecker struct{}

// Type returns the check type identifier.
func (c *RDPChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeRDP
}

// Validate checks if the configuration is valid (no network operations).
func (c *RDPChecker) Validate(spec *checkerdef.CheckSpec) error {
	cfg := &RDPConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	if spec.Name == "" {
		spec.Name = "RDP: " + cfg.Host
	}

	if spec.Slug == "" {
		spec.Slug = "rdp-" + sanitizeSlug(cfg.Host)
	}

	return nil
}

// sanitizeSlug turns a host into a slug-friendly fragment.
func sanitizeSlug(host string) string {
	out := make([]rune, 0, len(host))

	for _, r := range host {
		switch r {
		case '.', ':':
			out = append(out, '-')
		default:
			out = append(out, r)
		}
	}

	return string(out)
}

// Execute performs the RDP check and returns the result.
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

// dialTarget opens the TCP connection and applies the context deadline to all
// subsequent reads/writes. It records connect_ms on success.
func dialTarget(ctx context.Context, host string, port int, metrics map[string]any) (net.Conn, error) {
	dialer := &net.Dialer{}

	connectStart := time.Now()

	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
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
