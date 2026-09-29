---
effort: medium
---

# Check probes must not run on HTTP/2: one blip, one failed probe — not sixteen

## Problem

One ~1-minute IPv6 blackhole on a worker node turns into **16 minutes of false
failures**, because HTTP/2 hands the stranded connection back to every later
probe.

Measured on the production `lauterbourg` worker (2026-09-28, 10:46–11:03 UTC),
`ss -tanpi6` on the worker's own socket:

```
ESTAB 0 683→757→1127  [2a02:c207…]:36742 → [2a09:8280:1::184:3cba:0]:443  solidping fd=13
  rto:120000 backoff:12→15   lastack:592s→938s   unacked:1 retrans:1/18   notsent:644→1088
```

and the rolling packet capture for the same window (1 file/minute):

```
before: 247–294 packets/min   during: 71 → 2 → 1 → 1 → 1 → 1 (same 39-byte
segment, backoff ~4 min apart, no SYN, no inbound)   after: 45 (fresh handshake)
```

while a **fresh** connection from the same node, to the same address, answered
in 54–86 ms the whole time.

The check results follow exactly:

```
10:46:42  timeout  6006 ms   ← the blip; budget still at the 5 s floor
10:47:49  timeout 14597 ms   ← EWMA pushed the budget to the 15 s ceiling
10:48:51 … 11:01:51  timeout 16001 ms  ×13
11:02:36  down      1841 ms  ← kernel finally reports ETTIMEDOUT (tcp_retries2=15)
11:03:35  up          67 ms  ← fresh socket, immediate success
```

Mechanism: the probe's context expires, net/http cancels the request, and over
HTTP/2 that cancels a **stream**, not the connection — and specifically, when no
frame has been read since the request was sent, `clientStream.cleanupWriteRequest`
keeps the stream *"consuming a concurrency slot until we can confirm the server
is still responding"* (`net/http/internal/http2`). The connection returns to the
pool carrying a stream that can never complete; `CloseIdleConnections` refuses
to touch it (`closeIfIdle`: `len(cc.streams) > 0`). Every later execution of
that check obtains the connection (`GotConn` fires — hence `ip_version: ipv6` on
every failed row), its writer blocks waiting for flow-control credit that can
never arrive, and it burns the whole budget **without putting a packet on the
wire**. Only the kernel's `tcp_retries2` give-up ends it.

Two independent defects are visible in the evidence:

1. **The strand lasts 16 minutes** (above) — 86 % of that check's failures over
   a day (255 failing minutes, of which only 37 were the blip itself) and
   16 `region-heartbeat-lauterbourg is down/degraded` incidents in 14 h, while
   every other region's heartbeat was 100 % clean.
2. **The probe cannot fall back to IPv4 during the blip**: production runs with
   `allow_private_targets=false`, so checks dial through
   `egress.Guard.DialContextWith`, which resolves once and dials the answers
   **sequentially** with `defaultDialTimeout = 30 s` — longer than every check
   budget. On eu3, `LookupIPAddr` returns IPv6 first, deterministically (measured
   with a `CGO_ENABLED=0` binary, the same resolver the distroless image uses),
   so a blackholed v6 answer consumes the whole budget and the v4 answer is never
   tried. That contradicts `IPVersionAuto`'s documented *"Happy Eyeballs for
   HTTP"*.

## Decisions

- **Checks speak HTTP/1.1 on their pooled transports.** HTTP/1.1 closes the
  connection of any request that did not complete normally, so a connection can
  never outlive the probe that stranded it — while pooling across executions and
  across a redirect chain inside one execution is kept (no latency regression).
- Do **not** try to delete the corpse after the fact. The first attempt at this
  fix was `CloseIdleConnections()` after a failed execution; it was rejected
  because HTTP/2's stuck stream makes `closeIfIdle` refuse the very connection we
  need gone, and a test showed it working only ~1 run in 4. A fix that depends on
  Go's per-protocol cancellation internals is not a fix.
- Both shared transports are covered: the guard's (`HTTPTransport()`, every SaaS
  worker) and a new `checkerdef.checkTransport` replacing the `nil` that used to
  hand checks `http.DefaultTransport`. `http.DefaultTransport` itself is left
  alone — it is process-wide, and checks no longer reach it.
- Private transports (tunnel, `ipVersion` pin, `verifySsl: false`) already were
  HTTP/1.1: net/http "conservatively disables HTTP/2" once a `DialContext` or
  `TLSClientConfig` is set. This spec makes the two shared ones match them.
- Out of scope (own spec): `ipVersion: auto` losing its Happy-Eyeballs fallback
  under the enforcing guard (defect 2 above). That one decides how many probes a
  blip costs; this one decides whether a single stranded connection keeps failing.

## Proposal

### 1. `checkerdef.checkTransport`

New package-level transport in
`server/internal/checkers/checkerdef/httptransport.go`, returned by
`buildHTTPTransport` where it used to return `nil`:

- `TLSNextProto: {}` + `ForceAttemptHTTP2: false` — HTTP/2 off (an explicit empty
  `TLSNextProto` is the documented way to do it, `net/http` `protocols()`);
- dial settings mirroring `http.DefaultTransport` (30 s connect timeout, 30 s
  keep-alive, `ProxyFromEnvironment`, `IdleConnTimeout` 90 s …) so the numbers a
  check reports do not shift;
- shared across executions, so connection reuse survives.

### 2. `egress.Guard.HTTPTransport()`

Set `ForceAttemptHTTP2 = false` on the cloned transport (the clone carries
`http.DefaultTransport`'s `true` over) and say why in the doc comment.

### 3. Call-site comments

`checkhttp` and `checkjs` document that an untunneled check runs on
`http.DefaultTransport`; both now name the shared check transport.

### 4. Tests — `checkerdef/stranded_connection_test.go`

- `TestCheckTransportsAreHTTP1`: both entry points return an HTTP/1.1 transport
  (never `http.DefaultTransport`), under enforcing and non-enforcing guards.
- `TestStrandedConnectionDoesNotSurviveTheProbeThatHitIt`: an HTTP/2-capable TLS
  server that black-holes its first connection — probe 1 times out, **probe 2
  answers, on a different connection**, over HTTP/1.1. Under the old transports
  probe 2 timed out too; that is the regression this pins.

## Verification

- `go test ./internal/checkers/... ./internal/egress/...` (8/8 repeats of the new
  tests), `make test`, `make lint`.
- Production after deploy: `region-heartbeat-*` failures collapse to the length of
  the blip (~1 min) instead of ~16 min; the `down → up` pair at the end of each
  block disappears; check durations keep their shape (pooling retained).
