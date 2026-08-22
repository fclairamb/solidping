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
