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

func (r *sendModeRecording) snapshot() (mailFrom, rcptTo, body string, gotData bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.mailFrom, r.rcptTo, r.body, r.gotData
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

//nolint:cyclop // straight-line fake protocol handler, mirrors handleFakeSMTP's shape
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

func sendModeConfig(host string, port int, deliveryCheckUID string) *SMTPConfig {
	return &SMTPConfig{
		Host:             host,
		Port:             port,
		Timeout:          2 * time.Second,
		SendEmail:        true,
		MailFrom:         "prober@example.com",
		DeliveryCheckUID: deliveryCheckUID,
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

	cfg := sendModeConfig(host, port, "delivery-check-uid")
	ctx := checkerdef.WithSMTPDeliveryRecipient(context.Background(), checkerdef.SMTPDeliveryInfo{
		Recipient:      "deadbeef000000000000000000000000000000000000aa@inbox.example.com",
		SourceCheckUID: "sending-check-uid",
	})

	checker := &SMTPChecker{}
	result, err := checker.Execute(ctx, cfg)
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, result.Status)
	r.Contains(result.Metrics, "submission_ms")

	mailFrom, rcptTo, body, gotData := rec.snapshot()
	r.True(gotData, "the fake server must have received a DATA phase")
	r.Equal("prober@example.com", mailFrom)
	r.Equal("deadbeef000000000000000000000000000000000000aa@inbox.example.com", rcptTo)
	r.Contains(body, "X-SolidPing-Check: sending-check-uid")
	r.Contains(body, "X-SolidPing-Sent-At: ")
	// The message is entirely system-generated — no user-supplied subject or
	// body ever reaches the wire.
	r.Contains(body, "Subject: SolidPing delivery probe")
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

	_, _, _, gotData := rec.snapshot()
	r.False(gotData, "send_email:false must never issue MAIL FROM/RCPT TO/DATA")
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

	cfg := sendModeConfig(host, port, "delivery-check-uid")

	checker := &SMTPChecker{}
	result, err := checker.Execute(context.Background(), cfg)
	r.NoError(err)
	r.Equal(checkerdef.StatusError, result.Status)
	r.Contains(result.Output["error"], "no delivery recipient was resolved")

	_, _, _, gotData := rec.snapshot()
	r.False(gotData, "with no resolved recipient, the checker must never reach DATA")
}

// TestSMTPChecker_SendMode_MailFromRejected pins the Down-with-server-reply
// contract for a submission rejection.
func TestSMTPChecker_SendMode_MailFromRejected(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	host, port, _ := startSendModeFakeSMTPServer(t, sendModeFakeOpts{rejectMailFrom: true})

	cfg := sendModeConfig(host, port, "delivery-check-uid")
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

	cfg := sendModeConfig(host, port, "delivery-check-uid")
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
