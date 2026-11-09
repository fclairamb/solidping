package email

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/textproto"
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
