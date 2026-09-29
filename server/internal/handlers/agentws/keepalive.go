package agentws

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
)

// Connection liveness (spec 2026-09-28-03).
//
// Liveness is a state machine over OBSERVED traffic, not a blocking round
// trip. Every proof of life the read path can make — any client frame, the
// peer's own ping, a pong to our probe — is recorded by the connection's
// liveness sensor; a pinger goroutine probes on a ticker and reports each
// outcome to the event loop, which alone decides to close:
//
//	live ──probe unanswered──▶ stale ──a second silent cycle──▶ dead (ping_timeout)
//	  ▲                          │
//	  └──── any observation ─────┘
//
// A delayed pong can therefore never destroy a connection whose frames keep
// arriving: silence is the only thing that kills it.

// framesBufferSize buffers the reader → event-loop frame handoff.
//
// Invariant: the reader must return to Read even while the loop is busy,
// because Read is where liveness is observed (coder/websocket dispatches
// pongs, and auto-replies to the peer's pings, only from inside Read). An
// unbuffered handoff parked the reader behind the loop, the pong behind the
// parked frame, and killed healthy connections as ping_timeout. 64 is larger
// than one loop iteration can fall behind (an agent has at most its runner
// count of requests in flight, each frame capped at maxFrameBytes); an agent
// that fills it only delays its own request handling, never the sensor.
const framesBufferSize = 64

// Observation kinds, as logged in last_observation.
const (
	observedHandshake = "handshake"
	observedFrame     = "frame"
	observedPong      = "pong"
	observedPeerPing  = "peer_ping"
)

// liveness is one connection's sensor. It is written from inside Read (the
// library's ping/pong callbacks) and by the reader goroutine, and read by the
// event loop — hence the mutex. Nothing here ever blocks.
type liveness struct {
	mu       sync.Mutex
	seq      uint64 // bumped by every observation
	lastKind string
	lastAt   time.Time
	// handoffSince is when the reader started handing a frame to the loop;
	// zero while it is back in (or on its way to) Read.
	handoffSince time.Time
	// observed nudges the loop (capacity 1, non-blocking send) so a stale
	// connection flips back to live as soon as anything is observed.
	observed chan struct{}
}

func newLiveness() *liveness {
	return &liveness{
		lastKind: observedHandshake,
		lastAt:   time.Now(),
		observed: make(chan struct{}, 1),
	}
}

// acceptOptions wires the sensor into the connection: pongs and the peer's
// pings are observed synchronously from inside Read, before Read returns.
func (l *liveness) acceptOptions() *websocket.AcceptOptions {
	return &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled,
		OnPingReceived: func(context.Context, []byte) bool {
			l.observe(observedPeerPing)

			return true // keep the automatic pong
		},
		OnPongReceived: func(context.Context, []byte) {
			l.observe(observedPong)
		},
	}
}

func (l *liveness) observe(kind string) {
	l.mu.Lock()
	l.seq++
	l.lastKind = kind
	l.lastAt = time.Now()
	l.mu.Unlock()

	select {
	case l.observed <- struct{}{}:
	default:
	}
}

func (l *liveness) handoffStarted() {
	l.mu.Lock()
	l.handoffSince = time.Now()
	l.mu.Unlock()
}

func (l *liveness) handoffDone() {
	l.mu.Lock()
	l.handoffSince = time.Time{}
	l.mu.Unlock()
}

// livenessSnapshot is a consistent read of the sensor.
type livenessSnapshot struct {
	seq           uint64
	lastKind      string
	lastAge       time.Duration
	readerBlocked time.Duration
}

func (l *liveness) snapshot() livenessSnapshot {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	snap := livenessSnapshot{seq: l.seq, lastKind: l.lastKind, lastAge: now.Sub(l.lastAt)}

	if !l.handoffSince.IsZero() {
		snap.readerBlocked = now.Sub(l.handoffSince)
	}

	return snap
}

// keepalive is the event loop's side of the state machine: whether the
// connection is stale, and the sensor sequence it went stale at.
type keepalive struct {
	sensor   *liveness
	stale    bool
	staleSeq uint64
}

// runPinger probes the peer every interval, OUTSIDE the event loop: Ping must
// never occupy the goroutine that drains frames, and a silent-but-healthy
// agent (nothing to claim, nothing to submit) still needs a probe to be
// observable at all. Each outcome (nil = answered) is handed to the loop.
func runPinger(ctx context.Context, conn *websocket.Conn, interval time.Duration, probes chan<- error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		pingCtx, cancel := context.WithTimeout(ctx, interval/2)
		err := conn.Ping(pingCtx)

		cancel()

		select {
		case probes <- err:
		case <-ctx.Done():
			return
		}
	}
}

// handleObserved flips a stale connection back to live: something was
// observed since it went stale.
func (h *Handler) handleObserved(ctx context.Context, state *connState) {
	if state.keepalive.stale && state.keepalive.sensor.snapshot().seq != state.keepalive.staleSeq {
		state.keepalive.stale = false

		h.logger.DebugContext(ctx, "agent connection live again", "agent", state.agent.UID)
	}
}

// handleProbe feeds one probe outcome into the state machine, refreshes
// last_seen_at on an answered probe, and enforces revocation. Returns false to
// end the connection.
func (h *Handler) handleProbe(ctx context.Context, conn *websocket.Conn, state *connState, probeErr error) bool {
	keep := &state.keepalive

	switch {
	case probeErr == nil:
		keep.stale = false

		_ = h.dbService.UpdateAgentLastSeen(ctx, state.agent.UID, time.Now())
	case errors.Is(probeErr, net.ErrClosed):
		// The socket is gone; the reader reports it with the real reason.
		return true
	default:
		snap := keep.sensor.snapshot()

		if keep.stale && snap.seq == keep.staleSeq {
			// Second consecutive probe cycle with no observation at all.
			h.logger.WarnContext(ctx, "closing silent agent connection",
				"agent", state.agent.UID, "region", state.agent.Region,
				"last_observation", snap.lastKind, "last_observation_age", snap.lastAge,
				"reader_blocked_for", snap.readerBlocked)

			state.closeReason = models.AgentDisconnectReasonPingTimeout
			_ = conn.Close(websocket.StatusGoingAway, "ping timeout")

			return false
		}

		// live → stale (or stale → live → stale when something was observed
		// since the previous miss).
		keep.stale = true
		keep.staleSeq = snap.seq

		prommetrics.RecordAgentWSStale()
		h.logger.WarnContext(ctx, "agent connection stale: keepalive probe unanswered",
			"agent", state.agent.UID, "region", state.agent.Region,
			"last_observation", snap.lastKind, "last_observation_age", snap.lastAge,
			"reader_blocked_for", snap.readerBlocked, "error", probeErr)
	}

	// A revoked agent's live connection is closed on the next probe.
	return h.agentStillActive(ctx, conn, state)
}

// Close begins the agent-connection shutdown: every live connection's loop is
// canceled and ends with reason server_shutdown, and new connections are
// refused. Idempotent. Hijacked WebSocket connections are invisible to
// http.Server.Shutdown, so without this nothing would ever end them before
// the process exits — and their disconnect would never be recorded.
func (h *Handler) Close() {
	h.lifecycleMu.Lock()
	defer h.lifecycleMu.Unlock()

	h.closed = true
	h.stopConns()
}

// WaitForEvents waits until every agent connection has finished its teardown
// and every pending agent.connected / agent.disconnected write has landed, or
// ctx ends. Call it after Close (a live connection only finishes once it
// closes), before the database goes away.
func (h *Handler) WaitForEvents(ctx context.Context) error {
	done := make(chan struct{})

	go func() {
		h.connsWG.Wait()
		h.eventsWG.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// cancelOnClose arranges for cancel to run when Close is called, and returns
// the function that disarms it.
func (h *Handler) cancelOnClose(cancel context.CancelFunc) func() bool {
	if h.shutdownCtx == nil { // a Handler not built by NewHandler (unit tests)
		return func() bool { return false }
	}

	return context.AfterFunc(h.shutdownCtx, cancel)
}

// beginConnection registers a connection with the shutdown machinery. It
// returns false once Close was called; otherwise the caller must call
// h.connsWG.Done when the connection's teardown (including its synchronous
// shutdown event) is over.
func (h *Handler) beginConnection() bool {
	h.lifecycleMu.Lock()
	defer h.lifecycleMu.Unlock()

	if h.closed {
		return false
	}

	h.connsWG.Add(1)

	return true
}
