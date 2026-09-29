package agentws_test

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/agentws"
)

// Spec 2026-09-28-03: the server must never time out a healthy agent's
// keepalive because its own read path was parked on a busy event loop.

// fastPingInterval runs keepalive probe cycles fast enough for a test while
// leaving each probe interval/2 = 50 ms for its pong.
const fastPingInterval = 100 * time.Millisecond

// Log lines the keepalive state machine emits (asserted verbatim).
const (
	logStale       = "agent connection stale: keepalive probe unanswered"
	logSilentClose = "closing silent agent connection"
)

// logRecorder is a slog.Handler keeping every record, so a test can assert on
// what the connection path logged.
type logRecorder struct {
	mu      sync.Mutex
	records []slog.Record
}

func (l *logRecorder) Enabled(context.Context, slog.Level) bool { return true }

func (l *logRecorder) Handle(_ context.Context, record slog.Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.records = append(l.records, record.Clone())

	return nil
}

func (l *logRecorder) WithAttrs([]slog.Attr) slog.Handler { return l }

func (l *logRecorder) WithGroup(string) slog.Handler { return l }

// messages returns the attributes of every record logged with msg, in order.
func (l *logRecorder) messages(msg string) []map[string]string {
	l.mu.Lock()
	defer l.mu.Unlock()

	var out []map[string]string

	for i := range l.records {
		if l.records[i].Message != msg {
			continue
		}

		attrs := map[string]string{"level": l.records[i].Level.String()}
		l.records[i].Attrs(func(a slog.Attr) bool {
			attrs[a.Key] = a.Value.String()

			return true
		})

		out = append(out, attrs)
	}

	return out
}

// newKeepaliveEnv is an env whose connections run fast probe cycles and log
// into the returned recorder.
func newKeepaliveEnv(t *testing.T) (*env, *logRecorder) {
	t.Helper()

	logs := &logRecorder{}

	e := newEnvConfigured(t, nil, func(h *agentws.Handler) {
		h.SetPingInterval(fastPingInterval)
		h.SetLogger(slog.New(logs))
	})

	return e, logs
}

// probeCounter counts the server probes (pings) a fake agent has received and
// lets a test block until a given count is reached — driven by the pings
// themselves, never by sleeping.
type probeCounter struct {
	mu      sync.Mutex
	n       int
	changed chan struct{}
}

func newProbeCounter() *probeCounter {
	return &probeCounter{changed: make(chan struct{})}
}

func (p *probeCounter) inc() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.n++
	close(p.changed)
	p.changed = make(chan struct{})
}

// await blocks until at least want probes were received, failing the test if
// the connection ends first (readErr) or it takes unreasonably long.
func (p *probeCounter) await(t *testing.T, want int, readErr <-chan error) {
	t.Helper()

	deadline := time.After(10 * time.Second)

	for {
		p.mu.Lock()
		n, changed := p.n, p.changed
		p.mu.Unlock()

		if n >= want {
			return
		}

		select {
		case <-changed:
		case err := <-readErr:
			require.FailNow(t, "connection closed", "after %d of %d probes: %v", n, want, err)
		case <-deadline:
			require.FailNow(t, "probes stopped", "got %d of %d", n, want)
		}
	}
}

// agentLastSeen reads the agent's last_seen_at.
func (e *env) agentLastSeen(agentUID string) time.Time {
	e.t.Helper()

	agent, err := e.dbSvc.GetAgent(e.t.Context(), agentUID)
	require.NoError(e.t, err)
	require.NotNil(e.t, agent.LastSeenAt)

	return *agent.LastSeenAt
}

// TestFrameAheadOfPongDoesNotKillTheConnection is the regression test: the
// agent answers every server ping, but a claim frame always sits in the stream
// right AHEAD of its pong (the client writes it from inside its ping callback,
// before the library writes the pong). Before the fix the server's reader
// parked on the unbuffered frame handoff while the loop awaited the pong, the
// pong was never read, and the first probe closed a perfectly healthy
// connection as ping_timeout.
func TestFrameAheadOfPongDoesNotKillTheConnection(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e, logs := newKeepaliveEnv(t)

	var (
		armed  atomic.Bool
		sent   atomic.Int64
		client atomic.Pointer[websocket.Conn]
	)

	probes := newProbeCounter()

	conn, _, enrolled := e.enrollWith(e.mintToken(), "office-1", &websocket.DialOptions{
		OnPingReceived: func(ctx context.Context, _ []byte) bool {
			if !armed.Load() {
				return true
			}

			probes.inc()

			// Written synchronously, before this callback returns and the
			// library writes the pong: TCP order is claim, THEN pong.
			id := fmt.Sprintf("c-%d", sent.Add(1))
			_ = wsjson.Write(ctx, client.Load(), agentcrypto.ClientFrame{
				Type: agentcrypto.MsgTypeClaim, ID: id, MaxJobs: 1,
			})

			return true
		},
	})
	client.Store(conn)

	startSeen := e.agentLastSeen(enrolled.AgentUID)

	readErr := make(chan error, 1)

	go func() {
		for {
			var frame agentcrypto.ServerFrame
			if err := wsjson.Read(t.Context(), conn, &frame); err != nil {
				readErr <- err

				return
			}
		}
	}()

	armed.Store(true)

	// ≥ 3 full probe cycles survived: the 4th probe only goes out if the
	// connection is still open after the first three.
	probes.await(t, 4, readErr)

	require.Eventually(t, func() bool {
		return e.agentLastSeen(enrolled.AgentUID).After(startSeen)
	}, 5*time.Second, 10*time.Millisecond, "last_seen_at must keep moving")

	r.Empty(e.agentEvents(models.EventTypeAgentDisconnected), "no disconnect for a healthy agent")
	r.Empty(logs.messages(logSilentClose))
}

// TestFramesKeepAConnectionAliveWithoutPongs pins the any-frame rule: an agent
// that keeps sending frames but NEVER answers a ping is alive by construction.
// It goes stale on every unanswered probe and back to live on its next frame,
// and is never closed — this must not regress into "pong or die".
func TestFramesKeepAConnectionAliveWithoutPongs(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e, logs := newKeepaliveEnv(t)

	var armed atomic.Bool

	probes := newProbeCounter()

	conn, _, _ := e.enrollWith(e.mintToken(), "office-1", &websocket.DialOptions{
		OnPingReceived: func(context.Context, []byte) bool {
			if armed.Load() {
				probes.inc()
			}

			return false // never pong
		},
	})

	readErr := make(chan error, 1)

	// A continuous claim chain: every claim response triggers the next claim,
	// so frames keep flowing independently of the probe schedule.
	sendClaim := func(ctx context.Context, n int) error {
		return wsjson.Write(ctx, conn, agentcrypto.ClientFrame{
			Type: agentcrypto.MsgTypeClaim, ID: fmt.Sprintf("c-%d", n), MaxJobs: 1,
		})
	}

	armed.Store(true)
	r.NoError(sendClaim(t.Context(), 0))

	go func() {
		sent := 0

		for {
			var frame agentcrypto.ServerFrame
			if err := wsjson.Read(t.Context(), conn, &frame); err != nil {
				readErr <- err

				return
			}

			if strings.HasPrefix(frame.ID, "c-") {
				sent++
				if err := sendClaim(t.Context(), sent); err != nil {
					readErr <- err

					return
				}
			}
		}
	}()

	// Five probe cycles, none of them answered.
	probes.await(t, 6, readErr)

	// Positive control: the machine DID see the missing pongs — the connection
	// survived because of its frames, not because a probe succeeded.
	r.NotEmpty(logs.messages(logStale), "unanswered probes must mark the connection stale")
	r.Empty(logs.messages(logSilentClose))
	r.Empty(e.agentEvents(models.EventTypeAgentDisconnected))
}

// TestSilentAgentIsClosedAfterTwoMissedProbes: detection stays bounded. An
// agent that neither reads nor writes is closed as ping_timeout after exactly
// two consecutive unanswered probes, the first of which logged the stale WARN
// naming the agent and its last observation.
func TestSilentAgentIsClosedAfterTwoMissedProbes(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e, logs := newKeepaliveEnv(t)

	conn, _, enrolled := e.enroll(e.mintToken(), "office-1")

	// The client neither reads nor writes: nothing answers the server's probes.
	require.Eventually(t, func() bool {
		return len(logs.messages(logSilentClose)) > 0
	}, 10*time.Second, 5*time.Millisecond)

	stale := logs.messages(logStale)
	r.Len(stale, 1, "exactly one stale transition before the close: two consecutive missed probes")
	r.Equal("WARN", stale[0]["level"])
	r.Equal(enrolled.AgentUID, stale[0]["agent"])
	r.Equal(testRegion, stale[0]["region"])
	r.Equal("handshake", stale[0]["last_observation"], "nothing was observed since the connection opened")
	r.Contains(stale[0], "last_observation_age")
	r.Contains(stale[0], "reader_blocked_for")

	closing := logs.messages(logSilentClose)
	r.Len(closing, 1)
	r.Equal(enrolled.AgentUID, closing[0]["agent"])

	// Only now read: the server's close frame is waiting in the stream.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	var readErr error

	for readErr == nil {
		var frame agentcrypto.ServerFrame
		readErr = wsjson.Read(ctx, conn, &frame)
	}

	r.Equal(websocket.StatusGoingAway, websocket.CloseStatus(readErr), "closed by the server: %v", readErr)

	disconnected := e.waitForEvent(models.EventTypeAgentDisconnected)
	r.Len(disconnected, 1)
	r.Equal(models.AgentDisconnectReasonPingTimeout, disconnected[0].Payload[models.AgentEventPayloadReason])
}
