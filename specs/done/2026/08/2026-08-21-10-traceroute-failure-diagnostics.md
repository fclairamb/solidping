---
model: opus
effort: high
---

# Network-level failures capture no path diagnostics — attach an MTR-style traceroute when a check goes down

## Problem

Screenshots shipped end to end for browser checks (bounded capture LRU,
deported-agent upload-request frames —
`specs/done/2026/08/2026-08-21-05-agent-screenshot-upload-request-frame.md` —
and generic attachments with incident display,
`2026-08-21-01-generic-attachments-and-incident-screenshots.md`). Network
failures got nothing: a TCP timeout, connection refused, ICMP loss, or TLS
handshake failure produces only an error string, with no evidence of *where*
on the path it broke. Traceroute-on-failure is BetterStack's other
diagnostics differentiator, and nothing in the codebase touches traceroute or
MTR today. The attachment pipeline the screenshots work built is exactly the
transport this needs — the capture side is the only missing piece.

## Proposal

### Trigger policy

- Capture on the **transition to down** (the probe that opens the incident),
  not on every failing probe — and only for network-reachability failure
  classes: connect timeout, connection refused, ICMP timeout/unreachable,
  TLS handshake timeout. Application-level failures (HTTP 500, keyword
  mismatch, cert expiry) do not trigger a trace.
- Per-check toggle with an org-level default (on), plus an org rate limit
  (e.g. max N traces per minute) so a mass outage doesn't fork hundreds of
  traceroutes.
- **Best-effort and asynchronous**: the failing result is reported first,
  the trace runs afterwards with a hard time budget (~15s) and attaches when
  it completes. A trace failure never affects the result or the incident.

### Capture implementation

- Pure-Go MTR-style prober in the worker: a few rounds (default 3) of
  TTL-stepped probes recording per-hop RTT and loss, target resolved to the
  same address the failing check used (respect the check's IP-family pinning;
  workers have real IPv6 via hostNetwork).
- Privilege handling is the tricky part: ICMP echo needs root/`CAP_NET_RAW`.
  Detect capability at startup and pick the best available mode — privileged
  ICMP, unprivileged UDP (high ports), or TCP-SYN to the check's target port
  (which best matches what the check itself experienced through firewalls).
  Record the mode in the capture so the UI can label it; degrade silently,
  never crash the worker over a socket permission.
- Reverse-DNS each hop with a short per-hop timeout inside the overall
  budget.

### Transport & storage

- The trace is a JSON attachment (`type: traceroute`: mode, rounds, hops
  with ip/ptr/loss/avg-min-max RTT) on the failing result / opened incident,
  through the generic-attachments pipeline the screenshots use.
- Deported agents reuse the capture upload-request frame flow (bounded LRU,
  out-of-band upload) — same rules as screenshots, nothing agent-specific to
  invent beyond registering the new capture kind.

### Surfacing

- Incident and result detail render a hops table (hop #, host/IP, loss %,
  avg RTT) with the capture mode and probe region labelled — design-reference
  table components, mobile-usable.
- Docs page under diagnostics explaining modes and the privilege
  requirements for self-hosters.

### Testing

- Unit-test the round/aggregation logic and JSON shape with a fake prober.
- Integration test gated on capability detection (skip cleanly where raw
  sockets are unavailable, e.g. CI containers) — flaky-by-privilege is not
  acceptable; the gate must be deterministic.

### Out of scope (note in roadmap as follow-ups)

- A comparison trace on recovery (before/after path diff).
- DNS-failure diagnostics (resolution trace) — different capture, own spec.

---

## Implementation Plan

### Decisions settled before coding

**1. The trace is dispatched by the INCIDENT PIPELINE, not by the checker.**
The spec requires "capture on the transition to down only" and "the failing
result is reported first, the trace runs afterwards". Those two are the same
requirement: `createIncident` / `reopenIncident` *are* the transition, and they
run after the result row is written. Capturing in the checker would mean tracing
on every failing probe (a 15 s trace per 30 s probe) and discarding all but the
handful that opened an incident — the opposite of the storage argument the
screenshot rail was built on. So the checker's only job is to say *what kind of
failure this was*; the pipeline decides whether to trace.

**2. The checker records the failure class and the exact address it dialed.**
New `checkerdef.Diagnostics.NetworkFailure` — `{class, host, address, port}` —
set ONLY at transport-error sites in `checktcp`, `checkhttp`, `checkicmp`,
`checkssl`. Being set at those sites (and nowhere else) is what makes
"application-level failures do not trigger a trace" structural rather than a
string match: an HTTP 500, a keyword mismatch or an expired certificate never
passes through a dial error, so it never carries the marker. `address` is the
resolved IP the probe actually used, which is how the trace honours the check's
`ipVersion` pinning without re-deriving it.

**3. Vantage point: local for in-process workers, an agent frame for deported
agents.** A trace run on the master for a check that failed in `us1` would be a
lie. `tracedispatch.Dispatcher` therefore routes: agent connection registered
for `result.WorkerUID` → send a `trace-request` frame; else this process's own
worker uid → run locally; else drop. No fallback that would trace from the
wrong host.

**4. Privilege ladder — three modes, each labelled honestly.**
`icmp` (raw ICMP echo, needs root/`CAP_NET_RAW`) → `icmp-udp` (unprivileged
ICMP datagram socket: default on macOS, on Linux needs
`net.ipv4.ping_group_range`) → `tcp` (TTL-stepped connects to the check's own
port). The spec's "unprivileged UDP (high ports)" is deliberately NOT
implemented: classic UDP traceroute still needs a raw socket to *read* the ICMP
replies, so it is not actually an unprivileged mode — the ICMP datagram socket
is. The `tcp` mode cannot see intermediate hop addresses without `IP_RECVERR`
(Linux-only), so it records a per-TTL reachability ladder and says so; the UI
and the capture's `modeNote` must never present it as a hop list.
Detection is a one-shot socket open at first use, cached, and failure is
silent — a worker never dies over a socket permission.

### Steps

1. **`checkerdef` network-failure marker** — `netfailure.go`: `NetworkFailure`,
   the class constants, `ClassifyNetworkFailure(err, timedOut)`. Hung on
   `Diagnostics` (`json:"networkFailure,omitempty"` — small, so unlike
   `Screenshot.PNG` it rides the agent frame). Wired into `checktcp`,
   `checkhttp`, `checkicmp`, `checkssl`.

2. **`internal/nettrace` — the pure prober-independent core.** `Capture` /
   `Hop` JSON shapes, `Options`, the `Prober` interface, and `Trace()`: N rounds
   of TTL-stepped probes, per-hop send/receive/loss and min/avg/max RTT,
   stop-at-target, hard overall budget, reverse DNS with a per-hop timeout
   inside that budget. Unit-tested entirely against a fake prober.

3. **Real probers + capability detection** — `prober_icmp.go` (raw and datagram,
   v4 and v6), `prober_tcp.go`, `detect.go` (`Detect()`, cached, silent),
   `Run()` (detect → build → trace). Integration test gated on `Detect()` and
   skipped deterministically where no mode is available.

4. **Policy: per-check toggle, org default, rate limit.**
   - `checks.traceroute_on_failure BOOLEAN NULL` (NULL = inherit) — appended as
     `SECTION: traceroute-diagnostics` to the UNRELEASED `015_v0_18_0`, both
     dialects, both directions. Model, `CheckUpdate`, check create/update/response,
     `openapi.yaml` + `go generate ./pkg/client/...`.
   - Org default: org parameter `diagnostics.traceroute.enabled`, surfaced on the
     existing `GET/PATCH /orgs/:org/settings` as `tracerouteOnFailure`. Absent =
     on, per the spec.
   - `config.Checkers.Traceroute` — `enabled`, `rounds`, `hops`, `budget`,
     `limit` (traces/minute/org). Single-word koanf segments so the env loader
     reaches them without a manual reader.
   - Per-org fixed-window limiter (the `audit.FailedLoginFolder` pattern).

5. **Attachment rail: the `traceroute` kind.** `KindTraceroute`,
   `IncidentTracerouteTopic`, a kind-aware mime sniff (`application/json`
   accepted only under the traceroute kind, and only when the body decodes into
   a `nettrace.Capture`), `PutIncidentTraceroute`, and
   `ListIncidentAttachments` switched from one exact topic to the incident's
   topic PREFIX so screenshot and traceroute both surface.

6. **Incident trigger + dispatcher.** `incidents.TraceRequester` (nil-safe,
   returns nothing, same posture as `AttachmentStore`), called from
   `createIncident` / `reopenIncident` only. `internal/handlers/tracedispatch`
   holds the rate limit, the local-vs-agent routing, the 15 s budget, the
   goroutine, and the attachment write.

7. **Deported agents.** `MsgTypeTraceRequest` + trace fields on `ServerFrame`;
   `agentws.Handler.SendTraceRequest` on the existing `connRegistry`; agent-side
   `ws_trace.go` runs the trace and POSTs the JSON to the existing
   `/api/v1/agent/attachments` with its normal signed headers.

8. **dash0.** A `TracerouteCard` (design-reference `Card` + `Table`, mobile
   scroll container) rendering hop #, host/IP, loss %, avg RTT, with the mode
   and probe region labelled. Rendered on the incident detail page; the capture
   JSON is fetched from the attachment's own signed relative URL rather than
   inlined into the incident payload. i18n in all four locales.

9. **Docs** — `web/docs/docs/features/traceroute-diagnostics.md`: what triggers a
   trace, what each mode can and cannot see, and exactly what a self-hoster must
   grant (`CAP_NET_RAW` or `net.ipv4.ping_group_range`) to get real hops.

10. **Tests** — the spec's list, plus: the marker is absent for an HTTP 500 and
    present for a refused connection (negative with positive control); a
    flapping sequence produces exactly one trace request; the rate limit refuses
    the N+1th trace in a minute and admits it in the next; a trace failure
    leaves the incident and the result untouched.
