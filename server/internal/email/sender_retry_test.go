package email

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/textproto"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
)

func resetSendSlots() {
	sendSlotsOnce = sync.Once{}
	sendSlots = nil
}

//nolint:paralleltest // mutates the process-wide semaphore
func TestAcquireSendSlot_BoundsConcurrency(t *testing.T) {
	resetSendSlots()
	t.Cleanup(resetSendSlots)

	var current, peak atomic.Int32

	var wg sync.WaitGroup

	for range 10 {
		wg.Go(func() {
			release, err := acquireSendSlot(t.Context(), 2)
			if err != nil {
				return
			}

			n := current.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}

			time.Sleep(10 * time.Millisecond)
			current.Add(-1)
			release()
		})
	}

	wg.Wait()
	require.LessOrEqual(t, peak.Load(), int32(2))
	require.Positive(t, peak.Load())
}

//nolint:paralleltest // mutates the process-wide semaphore
func TestAcquireSendSlot_ContextCancelled(t *testing.T) {
	resetSendSlots()
	t.Cleanup(resetSendSlots)

	release, err := acquireSendSlot(t.Context(), 1)
	require.NoError(t, err)

	defer release()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err = acquireSendSlot(ctx, 1)
	require.Error(t, err)
}

func TestIsRetryableSendError(t *testing.T) {
	t.Parallel()

	require.True(t, IsRetryableSendError(io.EOF))
	require.True(t, IsRetryableSendError(&textproto.Error{Code: 421}))
	require.False(t, IsRetryableSendError(&textproto.Error{Code: 550}))
	require.False(t, IsRetryableSendError(io.ErrClosedPipe))
}

//nolint:paralleltest // mutates the process-wide semaphore
func TestSend_EOFLoggedAsWarnAndRetryable(t *testing.T) {
	resetSendSlots()
	t.Cleanup(resetSendSlots)

	lis, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	defer func() { _ = lis.Close() }()

	go func() {
		for {
			conn, acceptErr := lis.Accept()
			if acceptErr != nil {
				return
			}

			_ = conn.Close()
		}
	}()

	var buf bytes.Buffer

	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	port := lis.Addr().(*net.TCPAddr).Port //nolint:forcetypeassert // TCP listener
	sender := NewSender(&config.EmailConfig{
		Enabled: true, Host: "127.0.0.1", Port: port, From: "a@acme.test",
		Protocol: "none", AuthType: "noauth",
	}, logger)

	_, err = sender.Send(t.Context(), &Message{
		Recipients: Recipients{To: []string{"b@acme.test"}}, Subject: "s", Text: "t",
	})
	require.Error(t, err)
	require.True(t, IsRetryableSendError(err), err.Error())
	require.Contains(t, buf.String(), "level=WARN msg=\"failed to send email\"")
	require.NotContains(t, buf.String(), "level=ERROR")
}

func TestIsRecipientRejection(t *testing.T) {
	t.Parallel()

	require.True(t, IsRecipientRejection(&textproto.Error{Code: 550, Msg: "5.1.1 User unknown"}))
	require.True(t, IsRecipientRejection(&textproto.Error{Code: 552, Msg: "5.2.2 Mailbox full"}))
	require.False(t, IsRecipientRejection(&textproto.Error{Code: 550, Msg: "5.7.1 Relaying denied"}))
	require.False(t, IsRecipientRejection(&textproto.Error{Code: 553, Msg: "5.1.7 Bad sender address"}))
	require.False(t, IsRecipientRejection(&textproto.Error{Code: 535, Msg: "5.7.8 Authentication failed"}))
	require.False(t, IsRecipientRejection(&textproto.Error{Code: 450, Msg: "4.2.1 Try again later"}))
	require.False(t, IsRecipientRejection(&textproto.Error{Code: 554, Msg: "Transaction failed"}))
	require.False(t, IsRecipientRejection(io.EOF))
	require.False(t, IsRecipientRejection(nil))
}

// fakeSMTPRejectingRcpt serves one SMTP session that refuses every recipient
// with replyToRcpt.
func fakeSMTPRejectingRcpt(t *testing.T, enhanced bool, replyToRcpt string) int {
	t.Helper()

	lis, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = lis.Close() })

	go func() {
		conn, acceptErr := lis.Accept()
		if acceptErr != nil {
			return
		}

		defer func() { _ = conn.Close() }()

		tp := textproto.NewConn(conn)
		_ = tp.PrintfLine("220 acme.test ESMTP")

		for {
			line, readErr := tp.ReadLine()
			if readErr != nil {
				return
			}

			switch cmd := strings.ToUpper(strings.SplitN(line, " ", 2)[0]); cmd {
			case "EHLO":
				if enhanced {
					_ = tp.PrintfLine("250-acme.test")
					_ = tp.PrintfLine("250 ENHANCEDSTATUSCODES")
				} else {
					_ = tp.PrintfLine("250 acme.test")
				}
			case "RCPT":
				_ = tp.PrintfLine("%s", replyToRcpt)
			case "QUIT":
				_ = tp.PrintfLine("221 bye")

				return
			default:
				_ = tp.PrintfLine("250 OK")
			}
		}
	}()

	return lis.Addr().(*net.TCPAddr).Port //nolint:forcetypeassert // TCP listener
}

//nolint:paralleltest // mutates the process-wide semaphore
func TestSend_RecipientRejectedLoggedAsWarn(t *testing.T) {
	for _, enhanced := range []bool{true, false} {
		resetSendSlots()

		port := fakeSMTPRejectingRcpt(t, enhanced, "550 5.1.2 Mailbox does not exist")

		var buf bytes.Buffer

		logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
		sender := NewSender(&config.EmailConfig{
			Enabled: true, Host: "127.0.0.1", Port: port, From: "a@acme.test",
			Protocol: "none", AuthType: "noauth",
		}, logger)

		_, err := sender.Send(t.Context(), &Message{
			Recipients: Recipients{To: []string{"nobody@acme.test"}}, Subject: "s", Text: "t",
		})
		require.Error(t, err)
		require.False(t, IsRetryableSendError(err), err.Error())
		require.True(t, IsRecipientRejection(err), err.Error())
		require.Contains(t, buf.String(), "level=WARN msg=\"failed to send email\"")
		require.NotContains(t, buf.String(), "level=ERROR")
	}

	resetSendSlots()
}

//nolint:paralleltest // mutates the process-wide semaphore
func TestSend_RateLimitReplyIsRetryableAndRedacted(t *testing.T) {
	for _, enhanced := range []bool{true, false} {
		resetSendSlots()

		port := fakeSMTPRejectingRcpt(t, enhanced, `452 4.4.5 Rate limit exceeded, try again later.`)

		var buf bytes.Buffer

		logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
		sender := NewSender(&config.EmailConfig{
			Enabled: true, Host: "127.0.0.1", Port: port, From: "a@acme.test",
			Protocol: "none", AuthType: "noauth",
		}, logger)

		_, err := sender.Send(t.Context(), &Message{
			Recipients: Recipients{To: []string{"alerts@acme.test"}}, Subject: "s", Text: "t",
		})
		require.Error(t, err)
		require.True(t, IsRetryableSendError(err), err.Error())
		require.False(t, IsRecipientRejection(err), err.Error())
		require.Contains(t, err.Error(), "452")
		require.NotContains(t, err.Error(), "alerts@acme.test")

		logged := buf.String()
		require.Contains(t, logged, "level=WARN msg=\"failed to send email\"")
		require.NotContains(t, logged, "level=ERROR")
		require.NotContains(t, logged, "affected recipient")
	}

	resetSendSlots()
}

func TestRedactRecipients(t *testing.T) {
	t.Parallel()

	require.NoError(t, redactRecipients(nil))
	require.Equal(t, io.EOF, redactRecipients(io.EOF))

	err := redactRecipients(&textproto.Error{
		Code: 452,
		Msg:  "rate limited, affected recipient(s): a@acme.test, b@acme.test, affected message ID: <x@acme.test>",
	})
	require.Equal(t, `452 "rate limited, affected message ID: <x@acme.test>"`, err.Error())

	var tpErr *textproto.Error
	require.ErrorAs(t, err, &tpErr)
}
