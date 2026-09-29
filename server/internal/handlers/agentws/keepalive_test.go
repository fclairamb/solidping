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

// survivalPingInterval is the probe cadence of the test asserting a healthy
// frame-sending connection is NEVER closed. Its only wall-clock dependency is
// scheduler latency on the path ping → client callback → server reader, which
// must stay under 1.5 intervals for each probe-driven frame. Measured under
// `-cpu=1 -parallel=64` plus CPU burners, a goroutine could wait over 1 s to
// run (reader_blocked_for=0s: CPU starvation, not a parked reader), so 2 s is
// used. The tests assert behavior, not a speed.
const survivalPingInterval = 2 * time.Second

// answeredPingInterval is the cadence of the test asserting that NO probe ever
// goes unanswered: every pong must be read within interval/2, so it gets
// 1.5 s — above the > 1 s goroutine starvation measured under that same load.
const answeredPingInterval = 3 * time.Second

// Log lines the keepalive state machine emits (asserted verbatim).
const (
	logStale       = "agent connection stale: keepalive probe unanswered"
	logSilentClose = "closing silent agent connection"
	// logConnectionClosed is the handler's DEBUG line when the socket ends.
	logConnectionClosed = "agent connection closed"
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

// dump renders every record as one line, for failure diagnostics.
func (l *logRecorder) dump() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	out := make([]string, 0, len(l.records))

	for i := range l.records {
		line := l.records[i].Time.Format("15:04:05.000000") + " " +
			l.records[i].Level.String() + " " + l.records[i].Message
		l.records[i].Attrs(func(a slog.Attr) bool {
			line += " " + a.Key + "=" + a.Value.String()

			return true
		})

		out = append(out, line)
	}

	return out
}

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

	return newKeepaliveEnvWith(t, fastPingInterval)
}

// newKeepaliveEnvWith is newKeepaliveEnv with an explicit probe interval.
func newKeepaliveEnvWith(t *testing.T, interval time.Duration) (*env, *logRecorder) {
	t.Helper()

	logs := &logRecorder{}

	// On failure, replay what the connection path logged (with timestamps):
	// the stale/close lines carry the last observation and its age.
	t.Cleanup(func() {
		if t.Failed() {
			for _, line := range logs.dump() {
				t.Log(line)
			}
		}
	})

	e := newEnvConfigured(t, nil, func(h *agentws.Handler) {
		h.SetPingInterval(interval)
		h.SetLogger(slog.New(logs))
	})

	return e, logs
}

// probeCounter counts the server probes (pings) a fake agent has received and
// lets a test block until a given count is reached — driven by the pings
// themselves, never by sleeping.
type probeCounter struct {
	mu       sync.Mutex
	n        int
	changed  chan struct{}
	interval time.Duration
}

// newProbeCounter counts the probes of a connection probing every interval.
func newProbeCounter(interval time.Duration) *probeCounter {
	return &probeCounter{changed: make(chan struct{}), interval: interval}
}

func (p *probeCounter) inc() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.n++
	close(p.changed)
	p.changed = make(chan struct{})
}

// count is the number of probes received so far.
func (p *probeCounter) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.n
}

// await blocks until at least want probes were received, failing the test if
// the connection ends first (readErr) or it takes unreasonably long.
func (p *probeCounter) await(t *testing.T, want int, readErr <-chan error) {
	t.Helper()

	// Generous: the probes themselves pace this, the deadline only turns a
	// dead pinger into a failure instead of a hang.
	deadline := time.After(10*time.Second + time.Duration(want)*p.interval)

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

// connectedEventsFor returns the agent.connected events of one agent, once
// the (detached) connect write has landed.
func (e *env) connectedEventsFor(agentUID string) []*models.Event {
	e.t.Helper()

	e.waitForEvent(models.EventTypeAgentConnected)

	var out []*models.Event

	for _, event := range e.agentEvents(models.EventTypeAgentConnected) {
		if event.Payload["target_uid"] == agentUID {
			out = append(out, event)
		}
	}

	return out
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
//
// A second phase stops the claims: probes alone, answered, must keep
// last_seen_at moving (claim frames refresh it too, so the first phase cannot
// prove the probe-driven refresh on its own).
func TestFrameAheadOfPongDoesNotKillTheConnection(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// This test asserts that NO probe ever goes unanswered: see
	// answeredPingInterval.
	e, logs := newKeepaliveEnvWith(t, answeredPingInterval)

	var (
		claiming atomic.Bool
		sent     atomic.Int64
		answered atomic.Int64
		client   atomic.Pointer[websocket.Conn]
	)

	probes := newProbeCounter(answeredPingInterval)

	conn, _, enrolled := e.enrollWith(e.mintToken(), "office-1", &websocket.DialOptions{
		OnPingReceived: func(ctx context.Context, _ []byte) bool {
			probes.inc()

			if !claiming.Load() {
				return true
			}

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
	claiming.Store(true)

	readErr := make(chan error, 1)

	go func() {
		for {
			var frame agentcrypto.ServerFrame
			if err := wsjson.Read(t.Context(), conn, &frame); err != nil {
				readErr <- err

				return
			}

			if strings.HasPrefix(frame.ID, "c-") {
				answered.Add(1)
			}
		}
	}()

	// Phase 1 — ≥ 3 full probe cycles survived with a frame ahead of every
	// pong: the 4th probe only goes out if the connection is still open after
	// the first three.
	probes.await(t, 4, readErr)
	r.Positive(sent.Load(), "claims were written ahead of the pongs")

	// Phase 2 — stop claiming. Ping callbacks run one at a time on the
	// client's reader, so once the next probe was seen no callback can still
	// be writing a claim; then wait for every claim to be answered, so no
	// claim-driven last_seen_at refresh is left in flight.
	claiming.Store(false)
	probes.await(t, probes.count()+1, readErr)
	require.Eventually(t, func() bool {
		return answered.Load() == sent.Load()
	}, 10*time.Second, 5*time.Millisecond, "every claim answered")

	claimsSent := sent.Load()
	quietSeen := e.agentLastSeen(enrolled.AgentUID)

	// One more answered probe cycle, no frames at all.
	probes.await(t, probes.count()+1, readErr)
	require.Eventually(t, func() bool {
		return e.agentLastSeen(enrolled.AgentUID).After(quietSeen)
	}, 5*time.Second, 5*time.Millisecond, "an answered probe must refresh last_seen_at on its own")
	r.Equal(claimsSent, sent.Load(), "no claim was sent during the quiet phase")

	// Event recording works (positive control), and nothing disconnected.
	r.Len(e.connectedEventsFor(enrolled.AgentUID), 1)
	r.Empty(e.agentEvents(models.EventTypeAgentDisconnected), "no disconnect for a healthy agent")

	// Every probe was answered: never stale, never closed.
	r.Empty(logs.messages(logStale), "the agent answered every probe")
	r.Empty(logs.messages(logSilentClose))

	// Positive control for the log capture: the same logger records the
	// connection ending once the agent goes away.
	r.NoError(conn.CloseNow())
	require.Eventually(t, func() bool {
		return len(logs.messages(logConnectionClosed)) > 0
	}, 10*time.Second, 5*time.Millisecond, "the log capture must see the handler's own lines")
	r.Empty(logs.messages(logStale))

	// Join the disconnect write before the test's database closes.
	waitCtx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	r.NoError(e.handler.WaitForEvents(waitCtx))
}

// TestFramesKeepAConnectionAliveWithoutPongs pins the any-frame rule: an agent
// that keeps sending frames but NEVER answers a ping is alive by construction.
// It goes stale on every unanswered probe and back to live on its next frame,
// and is never closed — this must not regress into "pong or die".
//
// Frames are driven by the probes themselves: the agent writes one claim from
// inside every ping callback (and never pongs), so every probe cycle carries a
// frame observed after that probe was sent — no server round trip, no DB work
// and no timer sits between a probe and its frame.
func TestFramesKeepAConnectionAliveWithoutPongs(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e, logs := newKeepaliveEnvWith(t, survivalPingInterval)

	var (
		armed  atomic.Bool
		client atomic.Pointer[websocket.Conn]
		sent   atomic.Int64
	)

	probes := newProbeCounter(survivalPingInterval)

	conn, _, enrolled := e.enrollWith(e.mintToken(), "office-1", &websocket.DialOptions{
		OnPingReceived: func(ctx context.Context, _ []byte) bool {
			if !armed.Load() {
				return false
			}

			probes.inc()

			id := fmt.Sprintf("c-%d", sent.Add(1))
			_ = wsjson.Write(ctx, client.Load(), agentcrypto.ClientFrame{
				Type: agentcrypto.MsgTypeClaim, ID: id, MaxJobs: 1,
			})

			return false // never pong
		},
	})
	client.Store(conn)
	armed.Store(true)

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

	// Five probe cycles survived, none of them answered.
	probes.await(t, 6, readErr)

	// Positive control: the machine DID see the missing pongs — the connection
	// survived because of its frames, not because a probe succeeded.
	r.NotEmpty(logs.messages(logStale), "unanswered probes must mark the connection stale")
	r.Empty(logs.messages(logSilentClose))

	// Event recording works (positive control), and nothing disconnected.
	r.Len(e.connectedEventsFor(enrolled.AgentUID), 1)
	r.Empty(e.agentEvents(models.EventTypeAgentDisconnected))
}

// TestSilentAgentIsClosedAfterTwoMissedProbes: detection stays bounded. An
// agent that sends nothing at all — no frame, no ping, no pong — is closed as
// ping_timeout after exactly two consecutive unanswered probes, the first of
// which logged the stale WARN naming the agent and its last observation.
//
// The fake agent keeps a reader running but its ping callback refuses every
// pong, so the server observes exactly the silence of a dead peer, while the
// test still receives the server's close frame the instant it is written (a
// client that stopped reading would have to catch it inside the server's
// 5 s close-handshake window, a wall-clock race under load).
func TestSilentAgentIsClosedAfterTwoMissedProbes(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e, logs := newKeepaliveEnv(t)

	conn, _, enrolled := e.enrollWith(e.mintToken(), "office-1", &websocket.DialOptions{
		OnPingReceived: func(context.Context, []byte) bool { return false }, // never pong
	})

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

	// The server closes the connection on its own.
	var closeErr error

	select {
	case closeErr = <-readErr:
	case <-time.After(30 * time.Second):
		r.FailNow("the silent agent was never closed")
	}

	r.Equal(websocket.StatusGoingAway, websocket.CloseStatus(closeErr), "closed by the server: %v", closeErr)

	// Join the connection's teardown and its detached event write.
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	r.NoError(e.handler.WaitForEvents(ctx))

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

	disconnected := e.agentEvents(models.EventTypeAgentDisconnected)
	r.Len(disconnected, 1)
	r.Equal(models.AgentDisconnectReasonPingTimeout, disconnected[0].Payload[models.AgentEventPayloadReason])
}
