---
effort: high
---

# Every private-location agent flaps: the server times out its own keepalive pings

## Problem

Org `acme` on solidping.k8xp.com fills its events page with pairs like:

```
29s ago | Agent Disconnected | System | (nothing)
29s ago | Agent Connected    | System | (nothing)
3m ago  | Agent Disconnected | System | (nothing)
3m ago  | Agent Connected    | System | (nothing)
```

Querying the events table behind that page (org `acme`, all rows the type has
ever produced):

| agent | region | connects | disconnects | reasons |
|---|---|---|---|---|
| `74782dfcd3e5` | `@aws-paris` | 148 | 144 | **142 × `ping_timeout`**, 2 × `error` |
| `s3ns-paris-prod` | `@s3ns-paris-prod` | 5 | 1 | 1 × `error` |
| `s3ns-paris-nonprod` | `@s3ns-paris-nonprod` | 5 | 1 | 1 × `error` |

All three agents run the same build (0.34.0 against server 0.35.0 — the version
WARN in the server log fires for all three), the same org, the same server. One
flaps every 1–4 minutes; the other two have disconnected once each in 25 hours.

Four observations pin down what is happening:

1. **The flapping is entirely server-declared.** `ping_timeout` is written only by
   `handlePingTick` when *our* `conn.Ping` gets no pong
   ([handler.go:817-828](server/internal/handlers/agentws/handler.go#L817)).
   The agent-side failure mode (`pingLoop` → `retire(…, "ping failed", …)`,
   [ws.go:845-875](server/internal/checkworker/backend/ws.go#L845)) would show
   up as `error`, because the agent closes first. That accounts for only 4 of
   the 146 disconnects ever, and 3 of those 4 are one shared blip at 09:23 when
   *all three* agents errored within 18 s — a real event on the server side.
   None of the 142 flapping rows is `error`.
2. **The link is healthy.** Disconnect → reconnect is 60–100 ms
   (`23:14:17.264` → `23:14:17.327`), so both ends and the network were fine
   the instant after the server declared the connection dead.
3. **The lifetime of a connection is *always* `25k + 12.5 s`.** Every WS
   connection the current server pod has served and closed — 12 of them, all
   `@aws-paris` — ends at `duration=37.5s`, `1m2.5s`, `2m17.5s`, `3m32.5s`,
   `20m12.5s`, …: an integer number of 25 s ping cycles plus exactly one
   `pingInterval/2` ([handler.go:72, 818](server/internal/handlers/agentws/handler.go#L72)).
   Nothing else ends a connection on this pod: no mid-cycle read error, no
   close from the peer. The **first** ping that is not answered kills it.
4. **It tracks traffic, not geography or build.** `@aws-paris` wrote 12,197
   results in one hour (3.4/s) against 2,160 for `@s3ns-paris-prod` and 360 for
   `@s3ns-paris-nonprod` — and it is the only one that flaps. And no
   slow-SQL warning in the current pod's logs (which start at the 22:28 deploy)
   exceeded 1.2 s, so this is not a slow frame handler either.

A dead agent would reconnect slowly and flakily, and would be the one closing.
A dead network would break both directions. Neither fits. The server is killing
healthy connections, on schedule, all by itself.

## Root cause

The agent connection has **one goroutine that reads frames and one loop that
waits for pongs, coupled by an unbuffered channel**:

```go
frames := make(chan agentcrypto.ClientFrame)          // handler.go:673  (unbuffered)
go func() {                                            // :676-691 — the reader
    … wsjson.Read(loopCtx, conn, &frame) …
    select {
    case frames <- frame:                              // blocks while the loop is busy
    case <-loopCtx.Done(): return
    }
}()
…
case <-ping.C:                                         // :784-787 — the loop
    if !h.handlePingTick(ctx, conn, state) { … }       // conn.Ping, deadline 12.5 s
case frame := <-chans.frames:                          // :797-801
    … handleFrame (DB work) …
```

`coder/websocket` v1.8.15 processes control frames **only inside `Read`**: the
automatic pong it writes when it sees our ping, and the pong that resolves
`Conn.Ping`, are both dispatched from a `Read` call (`read.go`, `case opPing` /
`case opPong`; `Conn.Ping` blocks on a channel filled there, `conn.go:223-255`).
Nothing else reads control frames — there is no background read loop in the
library.

Put the two together:

1. The loop sends a ping and waits up to **12.5 s** for its pong.
2. While it waits, it is **not** selecting on `frames`, so the reader goroutine
   cannot hand over anything it reads.
3. If the agent has **any data frame in flight** — a result or claim it sent
   within one round-trip before our ping — the reader reads that frame first
   (TCP order) and blocks on `frames <- frame`.
4. The pong, which follows that frame in the stream, is now unreadable. Our
   `Conn.Ping` sits until its deadline, returns `context.DeadlineExceeded`, and
   `handlePingTick` closes the connection with `ping_timeout`
   ([handler.go:824-826](server/internal/handlers/agentws/handler.go#L824)).

So the collision is decided by **frame rate × RTT**: the busier the agent, the
likelier a frame sits in the ~RTT-wide window at ping time. `@aws-paris` at
3.4 frames/s collides every few pings; the two s3ns agents at 0.6/s and 0.1/s
essentially never do. That is the whole difference between the three rows — not
the network, not the build, not the region.

Why the *server* always reports it (142/142) and the agent never does: the
agent's `readPump` never blocks — every dispatch is a non-blocking send or a
spawned goroutine ([ws.go:793-839](server/internal/checkworker/backend/ws.go#L793))
— so its pong replies are always prompt. The server's reader is the one that
can get stuck, and when it does, the server is the one that times out. The
agent then sees our close frame, logs a read error and reconnects within
~100 ms: the "connected" event 63 ms after a "disconnected" event is the
signature of a connection we killed ourselves.

### Second defect found on the way: shutdown disconnects are never recorded

`reason='server_shutdown'` has **never** been written — all-time count 0 — even
though the logs show a restart at `22:28:43` today (three `agent.connected`
rows land 8 s later) and every agent in the table ends up with **exactly 4 more
connects than disconnects**: 1 connection legitimately still open, 3 silently
lost to restarts. `recordConnectionEvent`
runs in a detached goroutine with its own 5 s deadline
([handler.go:710-731](server/internal/handlers/agentws/handler.go#L710)) so it
can never block the connection path — but on shutdown nothing waits for it, and
`srv.Shutdown` does not track hijacked WebSocket connections
([app/server.go:3578-3595](server/internal/app/server.go#L3578) closes the
realtime hub; there is no agent-connection counterpart). The process exits, the
event is lost, and every agent shows connects it can never be matched to.

## Decisions

- **Liveness is a state machine over observed traffic, not a blocking round
  trip.** The loop must never *await* one specific answer (a pong to our ping).
  Every proof of life — **any** client frame, the peer's own ping, a pong to our
  probe — is an input to a per-connection machine that alone decides to close.
  This subsumes "tolerate one missed pong": a delayed pong is one input among
  several, and a busy agent is alive by construction (its claim/result frames
  say so).
- **Silence is the only thing that kills a connection.** Two consecutive probe
  cycles with no observation at all (~50 s at the current cadence) is a dead
  peer and closes with `ping_timeout`; anything less is `stale` and recovers on
  the next observation. Detection latency for a genuinely dead agent therefore
  moves from 12.5 s to ~50 s, well inside the 5-minute liveness window.
- **Fix the coupling too.** The read path must never depend on the loop being
  in `select` — `Read` is the only place the library observes pongs and answers
  the peer's pings, so a blocked reader is a blinded (and eventually
  unservable) connection. Raising the timeout alone would just make the same
  collision rarer and keep both design faults in place.
- **Keep the cadence.** `pingInterval` stays 25 s and the
  `last_seen_at` refresh semantics are untouched — the 5-minute liveness window
  (spec `2026-09-25-05`) must keep behaving exactly as documented.
- **No agent-side change required.** The agent's read path already never
  blocks, and the fix has to be safe for already-deployed 0.34.0 agents — so
  nothing in the wire protocol moves. Adopting the same state machine in
  `backend/ws.go`'s `pingLoop` is a welcome cleanup, but not a dependency of
  this spec.
- Out of scope: suppressing connect/disconnect pairs that land within N seconds
  of each other (revisit only if flapping recurs for a legitimate reason — a
  planned restart still deserves its pair); changing the ping cadence; the
  Slack `link_shared` nil-pointer panic visible in the same logs
  (`config.BaseURLHost`, `integrations/slack/events.go:105`) which needs its own
  spec.

## Proposal

### §1 Liveness is a state machine over observed traffic — `server/internal/handlers/agentws/handler.go`

Today liveness is a blocking round trip: the event loop calls `conn.Ping` and
blocks up to 12.5 s waiting for one specific answer
([handler.go:784-787, 817-828](server/internal/handlers/agentws/handler.go#L784)).
Replace it with a per-connection state machine whose inputs are **every
observation the read path can make** — the peer's frames, its pings and its
pongs alike — and whose only output is the decision to close.

`coder/websocket` v1.8.15 already exposes the hooks this needs
(`AcceptOptions.OnPingReceived` / `OnPongReceived`,
[accept.go:67-81](https://pkg.go.dev/github.com/coder/websocket@v1.8.15#AcceptOptions),
wired into the conn at `accept.go:176-177`), so nothing has to be inferred from
the absence of data frames:

| input | source | means |
|---|---|---|
| `frame` (claim/result) | the reader's `wsjson.Read` return | the peer is sending |
| `pong` | `OnPongReceived` (synchronous, from inside `Read`) | the peer answered our probe |
| `peerPing` | `OnPingReceived` (return `true`, keep the auto-reply) | the peer is probing us |
| `probeOK` / `probeFail` | a **pinger goroutine** calling `conn.Ping` on a ticker | our probe was answered / was not |
| `readError` / `peerClosed` | the reader / `readErr` channel | the socket died |
| `revoked` | `agentStillActive` | the agent was revoked |
| `shutdown` | `ctx.Done` | the server is going away |

States and transitions, all owned by the event loop — every input reaches it as
an event it selects on, and nothing is ever awaited inline:

```
                 frame | pong | peerPing | probeOK
        ┌────────────────────────────────────────────┐
        ▼                                            │
   live ──probeFail──▶ stale ──one more silent cycle──▶ dead
        ▲                                            ▲
        └────────── any observation resets ───────────┘   close: ping_timeout
```

- `live → stale` on the first `probeFail`; `stale → live` on **any**
  observation, including a `frame`. That is the whole point: an agent
  submitting results has proved itself alive whether or not its pong happened
  to be timely, so a delayed pong can never destroy a healthy connection.
- `stale → dead` only when a *second* consecutive probe cycle passes with no
  observation at all — then close with `AgentDisconnectReasonPingTimeout`,
  through the existing `conn.Close` + `recordConnectionEvent` path. The close
  is the machine's output, never a call whose own failure mode *is* the
  diagnosis.
- `readError` / `peerClosed` → `dead` with `error`; `revoked` → `revoked`;
  `shutdown` → `server_shutdown` (§4).
- On `live → stale`, log `WARN` with the agent UID, the region, the kind and
  age of the last observation, and how long the reader has been unable to
  return to `Read`. That one line distinguishes "peer silent" from "our read
  path starved" — what this report needed `psql` for.

The pinger goroutine replaces the loop's `ping.C` case and must live **outside**
the loop: `conn.Ping` must never occupy the goroutine that has to drain frames,
and a silent-but-healthy agent (nothing claimed, nothing to submit) still needs
a probe to be observable at all.

### §2 The read path is the machine's sensor — it must never block

Every observation in the table is made **inside `Read`**: the library dispatches
pongs and auto-replies to the peer's pings only while a goroutine is in `Read`
(`read.go`, `case opPing` / `case opPong`). A reader parked on channel delivery
observes nothing *and* stops answering the peer's own keepalives — which is
exactly how today's bug kills connections, and would make the agent declare
`ping failed` on its side if the stall outlived its 12.5 s deadline.

- Buffer the frame handoff: `frames := make(chan agentcrypto.ClientFrame,
  framesBufferSize)` with `framesBufferSize = 64`, and the invariant in a
  comment: *the reader must return to `Read` even while the loop is busy,
  because `Read` is where liveness is observed and pings are answered*. 64 is
  deliberately larger than one loop iteration can fall behind (an agent has at
  most its runner count of concurrent in-flight requests; each frame is capped
  at `maxFrameBytes`), and a pathological agent that fills it only delays its
  own request handling — never the liveness sensor.
- Record the observation **before** the handoff (the callbacks fire inside
  `Read`, and `Read` returns before the send), so even a full buffer can never
  manufacture a `stale` state. Handling may lag; liveness never does.

### §3 Make it countable

- Counters in `internal/prommetrics` for the machine's transitions:
  `agent_ws_conn_stale_total` (`live → stale`) and
  `agent_ws_conn_closed_total{reason}` (the four `AgentDisconnectReason*`
  values), plus `agent_ws_reconnects_total`. One-line increments at existing
  call sites; a recurrence then shows up on `/metrics` before anyone opens the
  events page — 142 `reason="ping_timeout"` closes in a day is unmissable.

### §4 Shutdown writes its disconnect

- In `runAgentConnection`, the `server_shutdown` reason is recorded
  **synchronously** (still bounded by `connectionEventTimeout`): the
  connection is already ending and the process is already going away, so the
  "never block the connection path" rule that justifies the detached goroutine
  no longer applies on that one path.
- Track every pending agent event write in a package-level `sync.WaitGroup`
  (Add/Done around the existing goroutine) and expose
  `agentws.Handler.WaitForEvents(ctx)`, called from `app/server.go` next to
  `realtimeHub.Close()` (`:3581`) so *any* in-flight agent event lands before
  exit, not only the shutdown one.
- Result: a restart produces `agent.disconnected {reason: server_shutdown}` for
  every connected agent instead of three orphan `agent.connected` rows.

## Tests

`pingInterval` is a package constant today, which is why nothing tests any of
this. Make it a `Handler` field with a `SetPingInterval(d)` setter (mirroring
the agent's existing `WithPingInterval`,
[ws.go:152](server/internal/checkworker/backend/ws.go#L152)), defaulting to
25 s, so the suite can run probe cycles at ~50 ms.

1. **The regression test (must fail before §1/§2, pass after).** With fast
   cycles, the fake agent sends a claim frame timed to land right after the
   server's probe goes out and before any pong (drive the send from the test
   once the probe is observable — never blind sleeps). Assert the connection
   survives ≥ 3 probe cycles, `last_seen_at` keeps moving, and no
   `agent.disconnected` row exists.
2. **The any-frame rule (§1).** An agent that keeps submitting results but
   *never* answers pings stays connected indefinitely and writes no
   `agent.disconnected`. This is the new semantic — it must not silently
   regress into "pong or die".
3. **Detection is still bounded (§1).** An agent that stops reading and writing
   entirely is closed after exactly two consecutive `probeFail`s with
   `reason='ping_timeout'`, and the first one logged the `stale` WARN naming
   the agent and its last observation.
4. **Terminal reasons stay distinct (§1/§4).** A read error closes with
   `error`, a revocation with `revoked`, a canceled request context with
   `server_shutdown` — and the `server_shutdown` event exists after
   `runAgentConnection` returns, with `WaitForEvents` joining a detached write.
5. Existing `connection_events_test.go` and the private-location liveness
   suites keep passing unchanged (the liveness window is 5 minutes; nothing
   here moves it).

## Acceptance

- A one-hour window on a busy agent shows **no** `agent.disconnected` events
  other than real ones, and the server log shows no `ping timeout` closes.
- An agent whose frames keep arriving but whose pongs are late is still
  considered alive (`stale` at worst) and is never closed — the any-frame rule
  is what the tests pin down.
- `agent.disconnected {reason: server_shutdown}` appears for every connected
  agent across a deploy, replacing today's orphan `agent.connected` rows.
- The server log, on the rare genuine miss, names the agent, the region and how
  long its reader was blocked — enough to diagnose a recurrence without
  database access.
