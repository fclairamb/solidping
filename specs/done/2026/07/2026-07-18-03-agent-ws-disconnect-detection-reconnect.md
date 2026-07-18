---
model: opus
effort: high
---

# Deported agents don't notice a lost WS connection — no log, no proactive reconnect

## Problem

The deported agent's WebSocket backend (`server/internal/checkworker/backend/ws.go`)
only discovers a lost connection lazily, and says nothing when it happens:

1. **Silent detection.** `readPump` (ws.go:499) exits on the first read error and
   calls `dropConn` (ws.go:536) — neither logs anything. The server side logs the
   close (agentws/handler.go:386, at Debug), but the agent's own logs show
   nothing at all when the link to the master dies. An operator tailing agent
   logs cannot tell the agent is disconnected.

2. **Lazy reconnect.** Reconnection only happens inside `ensureConn` (ws.go:308),
   which is only called from the next `request()` — i.e. the next claim poll or
   result submit. While disconnected, the agent receives no `jobs-available`
   hint frames, so the express dispatch path (`expressLoop` in
   `checkworker/worker.go:547`) is dead and new checks silently wait for the
   periodic fetcher poll. Nothing actively tries to restore the connection the
   moment it drops.

3. **Half-open connections hang.** The server pings every 25s
   (agentws/handler.go:57) and closes dead peers, but the agent never pings.
   On a silent network drop (NAT timeout, cable pull, node kill) the agent's
   `wsjson.Read` can block until the OS TCP timeout, and an in-flight
   `request()` write can land in a dead socket and burn the full 30s
   `requestTimeout` (ws.go:39) before anything gives up. In-flight waiters are
   also not failed when the conn is dropped — they always wait out the timeout.

## Proposal

All changes are agent-side, in `backend/ws.go` (plus tests):

1. **Report the loss.** When `readPump` exits on a read error, log at Warn
   (e.g. `"agent connection lost"` with the error and how long the connection
   was up). Keep the existing `"agent reconnected"` / `"agent enrolled"` Info
   logs so the recovery is visible too.

2. **Proactive reconnect loop.** When the active connection drops (readPump
   error or ping failure), immediately start reconnect attempts instead of
   waiting for the next claim: a loop calling the existing dial path, paced by
   `reconnectBackoff` (consider exponential backoff with a cap ~1min and
   jitter; first retry can be immediate). Each failed attempt logs at Warn with
   the next delay. The loop must stop on context cancellation and must not
   race `ensureConn` — reuse the same `mu`/`lastDial` pacing so only one dial
   runs at a time, whichever path triggers it.

3. **Agent-side liveness ping.** Run a keepalive ticker on each live
   connection (e.g. `conn.Ping` every ~20–30s with a short timeout, mirroring
   the server's `pingInterval/2` pattern in agentws/handler.go:408). A failed
   ping means the link is half-open: drop the conn, log the loss, and let the
   reconnect loop take over. This bounds detection latency to seconds instead
   of the OS TCP timeout.

4. **Fail fast on drop.** When `dropConn` retires a connection, wake any
   `pending` waiters with an error (close their channels or deliver an error
   frame) so in-flight claims/results fail immediately rather than waiting the
   full 30s `requestTimeout`, letting the worker loop retry on the fresh
   connection sooner.

### Testing

Extend the backend/agent WS tests (see `agentws/handler_test.go` and the
existing agent-mode e2e coverage):
- server closes the connection → agent logs the loss and re-establishes
  without any claim being issued; a hint sent after recovery reaches
  `Hints()` subscribers (proves the express path survives a drop).
- half-open simulation (server stops responding to pings without closing) →
  agent detects via ping failure within the ping interval and reconnects.
- a `request()` in flight when the conn drops returns promptly, not after 30s.

### Non-goals

- Server-side changes: the agentws handler's ping/close behavior already works.
- Offline job buffering: results that fail to submit keep today's semantics
  (lease expiry / retry by the worker loop).

## Implementation Plan

All changes live in `server/internal/checkworker/backend/ws.go` plus a new
test file; `agentws/handler.go` is a non-goal and is left untouched.

### Connection lifecycle rework (ws.go)

1. **Single-flight dial (`dialMu` + `lastDial`).** Extract the dial decision
   into `dialOnce(ctx, paced)`, guarded by a new `dialMu` so at most one dial
   runs at a time no matter which path triggers it (lazy `request()` →
   `ensureConn`, or the proactive supervisor). `ensureConn` becomes thin: it
   starts the reconnect supervisor once (capturing the long-lived worker ctx as
   a goroutine parameter — never stored on the struct, to satisfy
   `containedctx`) and delegates to `dialOnce(ctx, paced=true)`. The lazy path
   keeps today's `lastDial`-based min-spacing (`reconnectBackoff`); the
   supervisor passes `paced=false` because it owns its own backoff schedule so
   its first retry is immediate.

2. **Report the loss + fail fast (`retire`).** Replace `dropConn` with `retire(ctx,
   conn, reason, cause)`: idempotent per connection (acts only while
   `b.conn == conn`), it (a) logs `"agent connection lost"` at **Warn** with the
   reason, error, and connection uptime (tracked via a new `connectedAt`);
   (b) wakes every in-flight `pending` waiter with a synthetic
   `AGENT_CONN_LOST` error frame so `request()` returns immediately instead of
   burning the 30s `requestTimeout` (new `ErrConnLost`); and (c) signals the
   reconnect supervisor. `readPump`, the write-error path in `request()`, and
   the ping loop all funnel through `retire`.

3. **Proactive reconnect supervisor.** A single long-lived goroutine
   (`reconnectSupervisor`) started on first `ensureConn`, driven by a buffered
   `reconnectSignal` channel. On a drop signal it runs `reconnectUntilConnected`:
   loop calling `dialOnce(ctx, false)` until connected or ctx-cancelled, paced by
   an exponential backoff (`nextReconnectDelay`: double from `reconnectBackoff`,
   cap `maxReconnectBackoff` ~1min, ±20% jitter, first attempt immediate),
   logging each failed attempt at **Warn** with the next delay. A single
   supervisor + buffered signal closes the spawn race (a drop during
   reconnection is coalesced and re-checked) and stops cleanly on ctx
   cancellation.

4. **Agent-side liveness ping (`pingLoop`).** Each activated connection spawns a
   keepalive ticker (`conn.Ping` every `pingInterval`, default 25s mirroring the
   server, with a `pingInterval/2` timeout). A failed ping means a half-open
   link → `retire` → the supervisor reconnects, bounding detection to seconds.
   `activateConn` centralises publishing a fresh conn + spawning
   `readPump`/`pingLoop` (both on `WithoutCancel` so a per-request ctx cancel
   can't kill the shared conn), shared by `dialEnroll` and `dialReconnect`.

5. **Test seams.** Add functional options `WithLogger` (capture logs) and
   `WithPingInterval` (fast half-open detection) to `NewWSBackend` — variadic,
   so the three existing call sites are unchanged.

### Tests (`ws_reconnect_test.go`, package `backend_test`)

A controllable fake agent-facing WS server (handshake for enroll + signed
reconnect, per-connection behaviour hooks, claim counter, hint injection) drives
`WSBackend` directly:
- **server closes** → agent logs the loss at Warn and re-establishes with **no
  claim issued**; a `jobs-available` sent on the reconnected conn reaches a
  `Hints()` subscriber (express path survives the drop).
- **half-open** (server goes silent, never ponging) → agent detects via ping
  failure within the (shortened) ping interval and reconnects.
- **request in flight** when the conn drops returns promptly (well under the 30s
  `requestTimeout`), not after the full timeout.
