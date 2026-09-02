package heartbeat_test

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/heartbeatpush"
)

// lockedBuffer is a concurrency-safe sink for slog output.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

// captureLogs installs a DEBUG-level slog default for the duration of the test
// and returns the buffer everything lands in. DEBUG on purpose: the rejection
// path logs at DEBUG, so a capture at INFO would see nothing and the whole
// test would pass vacuously.
func captureLogs(t *testing.T) *lockedBuffer {
	t.Helper()

	sink := &lockedBuffer{}
	previous := slog.Default()

	slog.SetDefault(slog.New(slog.NewTextHandler(sink, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	return sink
}

// TestPushLoggingNeverLeaksCredentials is the guard the "secrets are never
// logged" claim needs to survive a future change.
//
// Today it is true by inspection. Inspection does not run in CI: a well-meant
// "let's log the annotation while we debug this" would ship a credential sink
// on an unauthenticated, internet-facing port, and nothing would fail. This
// drives both a rejected and an accepted beat, in both message forms, and
// asserts the token and the MAC appear NOWHERE in the captured output.
//
//nolint:paralleltest // mutates the process-wide slog default
func TestPushLoggingNeverLeaksCredentials(t *testing.T) {
	r := require.New(t)

	s := newHeartbeatSetup(t)
	sink := captureLogs(t)
	slug := s.checkSlug()

	// A signed beat, so a MAC exists to leak, and its counter is captured for
	// the replay below.
	signed := heartbeatpush.SignSP2(s.org.Slug, slug, testToken, 0, 77, "started volts=3.71")
	mac := signed[len(signed)-32:]

	// Accepted beats, both forms.
	r.True(s.handle(t, sp1(s.org.Slug, slug, testToken, "started volts=3.71 fw=1.4.2")))
	r.True(s.handle(t, signed))

	// Rejections across every reason the ingest has: wrong token, bad MAC,
	// replay, unknown org, unknown check.
	r.False(s.handle(t, sp1(s.org.Slug, slug, "a-wrong-token-value", "")))
	r.False(s.handle(t, heartbeatpush.SignSP2(s.org.Slug, slug, "a-wrong-signing-key", 0, 78, "")))
	r.False(s.handle(t, signed))
	r.False(s.handle(t, sp1("no-such-org", slug, testToken, "")))
	r.False(s.handle(t, sp1(s.org.Slug, "no-such-check", testToken, "")))

	logged := sink.String()

	// Positive control: the capture is live and the rejection path really did
	// write to it. Without this the assertions below would pass on an empty
	// buffer, which is the classic way a test like this proves nothing.
	r.Contains(logged, "Heartbeat push beat rejected")
	r.Contains(logged, "counter not strictly greater")

	// The actual assertions.
	r.NotContains(logged, testToken, "the check token must never reach a log line")
	r.NotContains(logged, "a-wrong-token-value", "not even a WRONG token — it may be a real one for another check")
	r.NotContains(logged, "a-wrong-signing-key")
	r.NotContains(logged, mac, "the MAC must never reach a log line")
	r.NotContains(logged, signed, "the raw beat line carries the credential, so it must not be logged whole")
}

// TestPushLoggingStripsControlCharactersFromTheTarget — the org and identifier
// are attacker-controlled bytes off an unauthenticated socket, and they ARE
// logged (a rejection you cannot attribute is useless). Nothing that could
// forge a log line or drive a terminal may survive into the record.
//
//nolint:paralleltest // mutates the process-wide slog default
func TestPushLoggingStripsControlCharactersFromTheTarget(t *testing.T) {
	r := require.New(t)

	s := newHeartbeatSetup(t)
	sink := captureLogs(t)

	// A forged target carrying an ANSI escape and an embedded newline. It is
	// rejected as an unknown org, which is exactly the path that logs it.
	beat := &heartbeatpush.Beat{
		Version:    1,
		Org:        "ev\x1b[31mil\nlevel=ERROR msg=\"forged\"",
		Identifier: "sen\x07sor",
		Token:      testToken,
	}

	accepted, err := s.svc.HandleBeat(t.Context(), beat, "203.0.113.7", "udp")
	r.NoError(err)
	r.False(accepted)

	logged := sink.String()

	// Positive control: the line was written and the target really was echoed
	// into it, so the assertions below are live rather than passing on an
	// empty buffer. "ev" and "il" survive; only the ESC byte between them is
	// stripped, which is exactly the point.
	r.Contains(logged, "Heartbeat push beat rejected")
	r.Contains(logged, "unknown target")
	r.Contains(logged, "ev[31mil")

	r.NotContains(logged, "\x1b", "an ANSI escape must not survive into a log line")
	r.NotContains(logged, "\x07")
	// The raw newline is gone, so the forged `level=ERROR` record the attacker
	// tried to inject stays inside the rejection line's own org attribute
	// instead of becoming a log record of its own.
	r.NotContains(logged, "il\nlevel=ERROR")

	var forgedLines int

	for _, line := range strings.Split(strings.TrimSpace(logged), "\n") {
		if strings.Contains(line, "forged") {
			forgedLines++

			r.Contains(line, "Heartbeat push beat rejected",
				"the injected text must stay inside the rejection record, never start one of its own")
		}
	}

	r.Equal(1, forgedLines)
}

// TestListenerLoggingNeverLeaksCredentials covers the other half of the claim:
// the listeners themselves log around the ingest, and they see the raw line.
//
//nolint:paralleltest // mutates the process-wide slog default
func TestListenerLoggingNeverLeaksCredentials(t *testing.T) {
	r := require.New(t)

	w := newWireSetup(t)
	sink := captureLogs(t)
	slug := w.checkSlug()

	signed := heartbeatpush.SignSP2(w.org.Slug, slug, testToken, 0, 5, "")

	r.Equal("OK", string(w.sendUDP(t, signed)))
	r.Nil(w.sendUDP(t, signed))                                       // replay
	r.Nil(w.sendUDP(t, sp1(w.org.Slug, slug, "a-wrong-token", "")))   // bad token
	r.Nil(w.sendUDP(t, "SP1 "+w.org.Slug+"/"+slug+" "+testToken+"x")) // near-miss token

	logged := sink.String()

	// Positive control: the capture is live and the listener path really did
	// write to it. (The "listener started" line is not usable here — it is
	// emitted by newWireSetup, before the capture is installed.)
	r.Contains(logged, "Heartbeat push beat rejected")
	r.Contains(logged, "token mismatch")

	r.NotContains(logged, testToken)
	r.NotContains(logged, "a-wrong-token")
	r.NotContains(logged, signed[len(signed)-32:])
}
