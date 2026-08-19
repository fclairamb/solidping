package checksmtp

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// sendModeRecording captures what a fake SMTP server actually received during
// a send-mode probe (the envelope and the message body/headers), so tests can
// assert on what was SENT over the wire — not merely on the checker's return
// value or a builder's intermediate output.
type sendModeRecording struct {
	mu       sync.Mutex
	mailFrom string
	rcptTo   string
	body     string
	gotData  bool
}

func (r *sendModeRecording) record(mailFrom, rcptTo, body string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.mailFrom = mailFrom
	r.rcptTo = rcptTo
	r.body = body
	r.gotData = true
}

// sendModeSnapshot is a point-in-time copy of what the fake server has
// recorded, safe to inspect after releasing the recording's lock.
type sendModeSnapshot struct {
	mailFrom string
	rcptTo   string
	body     string
	gotData  bool
}

func (r *sendModeRecording) snapshot() sendModeSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()

	return sendModeSnapshot{
		mailFrom: r.mailFrom,
		rcptTo:   r.rcptTo,
		body:     r.body,
		gotData:  r.gotData,
	}
}

type sendModeFakeOpts struct {
	rejectMailFrom bool
	rejectRcptTo   bool
	rejectData     bool
}

// startSendModeFakeSMTPServer starts a minimal, line-oriented fake SMTP server
// understanding EHLO/MAIL FROM/RCPT TO/DATA/QUIT — enough to exercise
// send-mode end to end and record exactly what the checker transmitted. It is
// deliberately separate from startFakeSMTPServer above (which only handles
// EHLO/QUIT) to avoid touching that shared, heavily-used helper.
func startSendModeFakeSMTPServer(t *testing.T, opts sendModeFakeOpts) (string, int, *sendModeRecording) {
	t.Helper()

	rec := &sendModeRecording{}

	lc := &net.ListenConfig{}

	listener, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	_, portStr, _ := net.SplitHostPort(listener.Addr().String())

	var port int

	_, _ = fmt.Sscanf(portStr, "%d", &port)

	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}

			go handleSendModeFakeSMTP(conn, opts, rec)
		}
	}()

	return "127.0.0.1", port, rec
}

func handleSendModeFakeSMTP(conn net.Conn, opts sendModeFakeOpts, rec *sendModeRecording) {
	defer func() { _ = conn.Close() }()

	reader := bufio.NewReader(conn)

	_, _ = fmt.Fprintf(conn, "220 fake.smtp.local ESMTP Fake\r\n")

	var mailFrom, rcptTo string

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}

		line = strings.TrimRight(line, "\r\n")
		upper := strings.ToUpper(line)

		switch {
		case strings.HasPrefix(upper, "EHLO"):
			_, _ = fmt.Fprintf(conn, "250-fake.smtp.local\r\n250 PIPELINING\r\n")

		case strings.HasPrefix(upper, "MAIL FROM:"):
			if opts.rejectMailFrom {
				_, _ = fmt.Fprintf(conn, "550 Sender rejected\r\n")

				continue
			}

			mailFrom = extractAngleAddr(line)
			_, _ = fmt.Fprintf(conn, "250 OK\r\n")

		case strings.HasPrefix(upper, "RCPT TO:"):
			if opts.rejectRcptTo {
				_, _ = fmt.Fprintf(conn, "550 Recipient rejected\r\n")

				continue
			}

			rcptTo = extractAngleAddr(line)
			_, _ = fmt.Fprintf(conn, "250 OK\r\n")

		case strings.HasPrefix(upper, "DATA"):
			if opts.rejectData {
				_, _ = fmt.Fprintf(conn, "550 Data rejected\r\n")

				continue
			}

			_, _ = fmt.Fprintf(conn, "354 Start mail input\r\n")

			body := readDotTerminatedBody(reader)
			rec.record(mailFrom, rcptTo, body)
			_, _ = fmt.Fprintf(conn, "250 OK: queued\r\n")

		case strings.HasPrefix(upper, "QUIT"):
			_, _ = fmt.Fprintf(conn, "221 Bye\r\n")

			return

		default:
			_, _ = fmt.Fprintf(conn, "500 Unknown command\r\n")
		}
	}
}

// readDotTerminatedBody reads lines until the standalone "." terminator,
// undoing SMTP dot-stuffing on the way (a line starting with ".." represents
// a literal line starting with "." in the original message).
func readDotTerminatedBody(reader *bufio.Reader) string {
	var body strings.Builder

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return body.String()
		}

		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "." {
			return body.String()
		}

		if strings.HasPrefix(trimmed, "..") {
			trimmed = trimmed[1:]
		}

		body.WriteString(trimmed)
		body.WriteString("\n")
	}
}

func extractAngleAddr(line string) string {
	start := strings.Index(line, "<")
	end := strings.Index(line, ">")

	if start == -1 || end == -1 || end < start {
		return ""
	}

	return line[start+1 : end]
}

// sendModeConfig builds a send-mode SMTPConfig. DeliveryCheckUID is a fixed
// placeholder — the checker itself never dereferences it (that resolution
// happens server-side before dispatch; see checkerdef.SMTPDeliveryInfo), so
// its exact value is irrelevant to every test in this file.
func sendModeConfig(host string, port int) *SMTPConfig {
	return &SMTPConfig{
		Host:             host,
		Port:             port,
		Timeout:          2 * time.Second,
		SendEmail:        true,
		MailFrom:         "prober@example.com",
		DeliveryCheckUID: "delivery-check-uid",
	}
}

// TestSMTPChecker_SendMode_PositiveControl is THE round-trip proof: send mode
// actually issues MAIL FROM/RCPT TO/DATA against the fake server, and the
// envelope plus both attribution headers the fake server RECEIVED are
// asserted directly — not merely the checker's return value.
func TestSMTPChecker_SendMode_PositiveControl(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	host, port, rec := startSendModeFakeSMTPServer(t, sendModeFakeOpts{})

	cfg := sendModeConfig(host, port)
	ctx := checkerdef.WithSMTPDeliveryRecipient(context.Background(), checkerdef.SMTPDeliveryInfo{
		Recipient:      "deadbeef000000000000000000000000000000000000aa@inbox.example.com",
		SourceCheckUID: "sending-check-uid",
	})

	checker := &SMTPChecker{}
	result, err := checker.Execute(ctx, cfg)
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, result.Status)
	r.Contains(result.Metrics, "submission_ms")

	snap := rec.snapshot()
	r.True(snap.gotData, "the fake server must have received a DATA phase")
	r.Equal("prober@example.com", snap.mailFrom)
	r.Equal("deadbeef000000000000000000000000000000000000aa@inbox.example.com", snap.rcptTo)
	r.Contains(snap.body, "X-SolidPing-Check: sending-check-uid")
	r.Contains(snap.body, "X-SolidPing-Sent-At: ")
	// The message is entirely system-generated — no user-supplied subject or
	// body ever reaches the wire.
	r.Contains(snap.body, "Subject: SolidPing delivery probe")
}

// TestSMTPChecker_SendMode_NegativeControl_Disabled is the required negative
// control alongside the positive one above: with send_email false (or the
// zero value — every existing config), the checker must issue NO mail
// commands at all, proving a broken checker cannot pass both tests the same
// way.
func TestSMTPChecker_SendMode_NegativeControl_Disabled(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	host, port, rec := startSendModeFakeSMTPServer(t, sendModeFakeOpts{})

	cfg := &SMTPConfig{Host: host, Port: port, Timeout: 2 * time.Second}
	ctx := checkerdef.WithSMTPDeliveryRecipient(context.Background(), checkerdef.SMTPDeliveryInfo{
		Recipient:      "should-never-be-used@inbox.example.com",
		SourceCheckUID: "sending-check-uid",
	})

	checker := &SMTPChecker{}
	result, err := checker.Execute(ctx, cfg)
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, result.Status)

	snap := rec.snapshot()
	r.False(snap.gotData, "send_email:false must never issue MAIL FROM/RCPT TO/DATA")
}

// TestSMTPChecker_SendMode_NoResolvedRecipient is the guardrail test: this is
// exactly what a JS check's sub-check helper would produce (send_email:true
// in a raw config map, executed directly with a context that never went
// through worker.go's job dispatch and therefore never carries
// WithSMTPDeliveryRecipient). It must fail loudly — StatusError — and must
// never issue MAIL FROM/RCPT TO/DATA, never a silent skip and never a send to
// nowhere.
func TestSMTPChecker_SendMode_NoResolvedRecipient(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	host, port, rec := startSendModeFakeSMTPServer(t, sendModeFakeOpts{})

	cfg := sendModeConfig(host, port)

	checker := &SMTPChecker{}
	result, err := checker.Execute(context.Background(), cfg)
	r.NoError(err)
	r.Equal(checkerdef.StatusError, result.Status)
	r.Contains(result.Output["error"], "no delivery recipient was resolved")

	snap := rec.snapshot()
	r.False(snap.gotData, "with no resolved recipient, the checker must never reach DATA")
}

// TestSMTPChecker_SendMode_MailFromRejected pins the Down-with-server-reply
// contract for a submission rejection.
func TestSMTPChecker_SendMode_MailFromRejected(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	host, port, _ := startSendModeFakeSMTPServer(t, sendModeFakeOpts{rejectMailFrom: true})

	cfg := sendModeConfig(host, port)
	ctx := checkerdef.WithSMTPDeliveryRecipient(context.Background(), checkerdef.SMTPDeliveryInfo{
		Recipient:      "deadbeef@inbox.example.com",
		SourceCheckUID: "sending-check-uid",
	})

	checker := &SMTPChecker{}
	result, err := checker.Execute(ctx, cfg)
	r.NoError(err)
	r.Equal(checkerdef.StatusDown, result.Status)
	r.Contains(result.Output["error"], "MAIL FROM rejected")
}

// TestSMTPChecker_SendMode_DataRejected pins the same contract for a
// rejection at the DATA phase.
func TestSMTPChecker_SendMode_DataRejected(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	host, port, _ := startSendModeFakeSMTPServer(t, sendModeFakeOpts{rejectData: true})

	cfg := sendModeConfig(host, port)
	ctx := checkerdef.WithSMTPDeliveryRecipient(context.Background(), checkerdef.SMTPDeliveryInfo{
		Recipient:      "deadbeef@inbox.example.com",
		SourceCheckUID: "sending-check-uid",
	})

	checker := &SMTPChecker{}
	result, err := checker.Execute(ctx, cfg)
	r.NoError(err)
	r.Equal(checkerdef.StatusDown, result.Status)
	r.Contains(result.Output["error"], "DATA rejected")
}

// smtpInjectionMailFroms are mail_from values crafted to smuggle an extra
// SMTP command (via CRLF in the MAIL FROM command line) or an injected
// message header (via CRLF in the From: header) if ever spliced onto the
// wire unescaped. Every one of these must be rejected at validation — this
// is the exact property the spec's delivery_check_uid indirection exists to
// guarantee, and the injection this feature must never become a vector for.
func smtpInjectionMailFroms() []string {
	return []string{
		"prober@example.com>\r\nBcc: attacker@evil.com\r\nSubject: totally not a probe",
		"prober@example.com\r\nRCPT TO:<victim@evil.com>",
		"prober@example.com\nBcc: attacker@evil.com",
		"prober@example.com\rDATA",
	}
}

// TestValidateMailFrom_RejectsInjection is the validation-level proof: every
// CRLF/LF/CR-carrying payload above must fail ValidateMailFrom, which is the
// single gate both the checker's own Validate() and checks/service.go's
// validateSMTPDeliveryConfig call — so a config carrying one of these can
// never be saved, on either the create or the PATCH path.
func TestValidateMailFrom_RejectsInjection(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	for _, mailFrom := range smtpInjectionMailFroms() {
		err := ValidateMailFrom(mailFrom)
		r.Error(err, "must reject mail_from %q", mailFrom)
	}
}

// TestSMTPConfig_Validate_RejectsMailFromInjection pins the same guarantee
// through the checker's public Validate() entry point (the create-time gate).
func TestSMTPConfig_Validate_RejectsMailFromInjection(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	for _, mailFrom := range smtpInjectionMailFroms() {
		cfg := &SMTPConfig{
			Host:             "mail.example.com",
			SendEmail:        true,
			MailFrom:         mailFrom,
			DeliveryCheckUID: "delivery-check-uid",
		}
		err := cfg.Validate()
		r.Error(err, "must reject mail_from %q", mailFrom)
	}
}

// TestSMTPChecker_SendMode_MailFromInjection_NeverReachesWire is the
// belt-and-braces proof at the send layer itself: even a config carrying an
// injection payload (as if it had somehow bypassed write-time validation)
// must never let the smuggled command or header reach the wire. The fake
// server records exactly what it received; a broken defense would show up
// either as a second command the checker never intended (an extra RCPT TO)
// or as the raw CRLF payload landing in the DATA body.
func TestSMTPChecker_SendMode_MailFromInjection_NeverReachesWire(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	host, port, rec := startSendModeFakeSMTPServer(t, sendModeFakeOpts{})

	for _, mailFrom := range smtpInjectionMailFroms() {
		cfg := &SMTPConfig{
			Host:             host,
			Port:             port,
			Timeout:          2 * time.Second,
			SendEmail:        true,
			MailFrom:         mailFrom,
			DeliveryCheckUID: "delivery-check-uid",
		}
		ctx := checkerdef.WithSMTPDeliveryRecipient(context.Background(), checkerdef.SMTPDeliveryInfo{
			Recipient:      "deadbeef@inbox.example.com",
			SourceCheckUID: "sending-check-uid",
		})

		checker := &SMTPChecker{}
		result, err := checker.Execute(ctx, cfg)
		r.NoError(err)
		r.Equal(checkerdef.StatusError, result.Status, "mail_from %q must be rejected, not sent", mailFrom)
		r.Contains(result.Output["error"], "invalid mail_from")

		snap := rec.snapshot()
		r.False(snap.gotData, "an injection payload must never reach the DATA phase for %q", mailFrom)
		r.Empty(snap.rcptTo, "no RCPT TO — smuggled or otherwise — may reach the wire for %q", mailFrom)
	}
}
