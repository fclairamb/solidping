---
model: opus
effort: high
---

# Embedded devices can't reach the heartbeat ingest — add a minimal TCP/UDP push transport

## Problem

Heartbeat checks are SolidPing's passive "device is alive" primitive
(`server/internal/checkers/checkheartbeat/checker.go`), but their only ingest
transport is HTTPS: `POST/GET /heartbeat/:org/:identifier` with a token
(`server/internal/app/server.go:1123-1124`,
`server/internal/handlers/heartbeat/`). That requires a full TCP + TLS + HTTP
stack on the sender, which shuts out exactly the fleet heartbeat semantics fit
best — microcontrollers (ESP8266/STM32-class), cellular modems driven by AT
commands, battery sensors, legacy PLCs:

- TLS costs tens of KB of RAM, a CA store, and a correct clock for cert
  validation — often unavailable or not worth it on constrained firmware.
- HTTP framing is pure overhead for a message whose meaning is one bit
  ("I'm alive").
- Battery devices want one fire-and-forget datagram, not a connection
  handshake.

## Proposal

Treat this as a **new transport for the existing heartbeat check type**, not a
new check type. No new check semantics — two small listeners that resolve a
token and feed the same service path as the HTTP ingest.

### Wire protocol

One line per beat, still `netcat`-debuggable. Two message forms, both
implemented from the start:

**SP1 — plaintext token:**

```
SP1 <org>/<check-identifier> <token> [annotation]\n
```

**SP2 — HMAC-signed (no secret on the wire):**

```
SP2 <org>/<check-identifier> <ts> <ctr> [annotation] <mac>\n
```

- `ts` — unix seconds from the device clock, or `0` when the device has no
  clock.
- `ctr` — a strictly-increasing counter. Device guidance (goes in the docs):
  persist a boot counter (one flash write per boot) and send
  `ctr = boot_count << 32 | seconds_since_boot` — monotonic across reboots,
  zero flash writes in steady state, no RNG needed (which is why this is a
  counter and not a nonce).
- `mac` — HMAC-SHA256 keyed with the check's token, computed over the exact
  line up to but excluding the final space and the MAC itself (i.e.
  `SP2 <org>/<check-identifier> <ts> <ctr>` plus the annotation when
  present), truncated to 16 bytes, hex-encoded. The MAC is always the
  **last** space-separated token — parse the line from both ends — so the
  annotation is covered by the signature and can never be tampered with on
  an authenticated beat. A bare datagram stays around 80 bytes.

The canonical-string-then-HMAC style deliberately mirrors the existing
service-signature scheme (`internal/servicesig`, HMAC-SHA256 over
`<timestamp>.<METHOD>.<path>.<sha256 body>`) — one signing idiom in the
project, not two.

SP2 verification, in order:

1. Look up the check by `<org>/<check-identifier>`, recompute the MAC with the
   check's token, compare in constant time.
2. If `ts != 0`, require it within a configurable window of server time
   (default ±5 min). The timestamp's only job is bounding damage if the
   server's counter state is ever lost (e.g. a restored backup).
3. Require `ctr` strictly greater than the last accepted counter for the
   check, then persist the new value. Strict monotonicity is the replay
   protection: an old datagram fails, and even the latest datagram cannot be
   replayed.

### Beat annotations (optional status word + key=value fields)

Both message forms accept an optional annotation, logfmt-style:

```
[status-word] [key1=value1 key2=value2 ...]
```

- **`status-word`** — one bare token without `=` (e.g. `started`, `alive`).
  V1 treats it as an opaque annotation stored on the result; the docs
  reserve `started` / `ok` / `fail` as "annotation-only today, may gain
  semantics later" so firmware can adopt the vocabulary now without wire
  changes. No status-changing keywords in V1 — a pushed `fail` raising
  incidents drags in recovery-lifecycle design, and under SP1 would let a
  token holder raise false alarms; deferred deliberately. The immediate
  value is reboot visibility: a device beating `started` every 10 minutes
  is crash-looping, which a bare heartbeat cannot distinguish from healthy.
- **`key=value` pairs** — split on the first `=` (values may contain `=`);
  keys `[a-z0-9_]{1,32}`, values ≤64 bytes, no quoting so no spaces in
  values, at most 10 pairs per beat, whole annotation ≤128 bytes (inside
  the line cap). NaN/Inf rejected; numbers parse as float64.
- **An annotation never invalidates a beat.** Parsing is best-effort: if
  the remainder doesn't match the grammar, store it as raw text on the
  result and count the beat anyway — aliveness first; a firmware typo in a
  key name must never make a healthy device look dead. (Under SP2 the MAC
  covers the raw bytes either way.)
- **Storage**: the status word and non-numeric pairs go to the result's
  `output` jsonb; **numeric values go to the result's `metrics` jsonb**,
  making them first-class time series — charted on the check page and
  rolled up by the existing aggregation suffix conventions
  (`server/internal/jobs/jobtypes/job_aggregation.go`). No schema change.
  This is the "send data" half of the original idea: battery volts, RSSI,
  temperature in one UDP datagram.
- **Untrusted input**: strip control characters, HTML-escape everywhere it
  renders in dash0, never log raw. The per-beat caps above bound metric-key
  cardinality; rate limiting bounds beat volume.
- **HTTP parity**: the existing HTTPS ingest accepts the same annotation
  (query param or small body) so the field means one thing across all three
  transports.

### Per-check HMAC requirement

The heartbeat check config gains `require_hmac` (bool, default `false` —
snake_case like other checker config keys, e.g. `sleep_ms`):

- `require_hmac: false` — the check accepts SP1, SP2, and the existing HTTP
  ingest. Convenience default; matches today's security posture.
- `require_hmac: true` — SP1 beats are rejected (silently on UDP); only SP2
  and HTTPS are accepted.

Enforcement must be per check and strict: a check that ever accepts SP1 leaks
its token to a passive sniffer, and the token is also the SP2 HMAC key — so
flipping a check to `require_hmac: true` should be paired with a token
rotation, and both the dashboard toggle and the docs must say so (the
rotate-token endpoint already exists).

### V1 scope

1. **UDP listener** — fire-and-forget, one datagram carries exactly one
   line. On success optionally reply `OK` (never more bytes than were
   received — no amplification vector); on any failure reply nothing and
   drop silently (no validity oracle for token, MAC, or org/check
   existence).
2. **TCP listener with connection reuse** — the stream is newline-delimited;
   each line is an independent SP1/SP2 beat, each accepted line answered
   `OK\n`. A device may send one beat and close, or hold the connection open
   and keep beating on it (one handshake total, NAT pinhole stays warm —
   matters on cellular). An invalid line closes the connection without a
   response. Guards: idle timeout (default 10 min), per-connection beat rate
   cap, bounded line length. **An open connection is not a heartbeat** —
   only accepted lines mark the check alive, so a hung device holding a
   socket never reads as up; docs tell devices to reconnect with jittered
   backoff after any disconnect. SP2's counter rule applies per line as
   normal.
3. **Config-gated, off by default.** New config keys following the koanf
   conventions (`heartbeat.tcp_listen` / `heartbeat.udp_listen`, env
   `SP_HEARTBEAT_TCP_LISTEN` / `SP_HEARTBEAT_UDP_LISTEN`). Default port when
   enabled: **4001, same number on TCP and UDP** — one number to document,
   one firewall rule, adjacent to the app's own 4000; its only notable prior
   art (etcd's v2 client port) is long retired, and it's configurable for
   anyone it collides with. Never encode protocol version in the port — the
   `SP1`/`SP2` prefix does the versioning, so one port serves every future
   form. Enabling is a deployment decision (new ports on the k8s Service /
   LB); document that, don't automate it here.
4. **Reuse `heartbeat.Service`** (`server/internal/handlers/heartbeat/service.go`)
   for token verification, result recording, incident/recovery semantics —
   identical behavior to the HTTP ingest, including the configured-check-timeout
   handling fixed by spec 2026-09-01-02.
5. **Counter state**: persist the last accepted `ctr` per check server-side
   (small per-check state write on each accepted SP2 beat; exact home —
   dedicated column vs. job-state jsonb — is an implementation choice, but it
   must survive restarts and be safe under concurrent beats).
6. **Abuse resistance**: per-source-IP rate limiting on both listeners
   (reuse/mirror `internal/middleware/ratelimit.go` budget logic where
   practical), bounded read sizes, constant-time token/MAC comparison, and
   per-listener Prometheus counters (accepted / rejected / malformed).
7. **Dashboard**: the heartbeat check detail page gains the `require_hmac`
   toggle (with the rotate-token nudge) and copy-paste examples — netcat
   one-liner for SP1, a minimal Arduino/ESP snippet for SP2 — next to the
   existing HTTP URL, shown only when the server reports a listener enabled.
8. **Docs**: a `web/docs/` page for embedded/push monitoring covering both
   message forms, both transports, the counter guidance, and the security
   trade-offs below.

### Why this design

- **Why a new trivial protocol rather than an existing one**: CoAP is
  UDP-native and standardized but drags in library weight on the device side,
  defeating the purpose; MQTT needs broker semantics we don't want to run;
  DTLS is the same handshake/cert/RAM burden that ruled out TLS, just over
  UDP. statsd is the precedent that a plaintext one-line UDP protocol is a
  perfectly good interface for this class of sender. HMAC-SHA256 on the
  device is a few KB of code (present in every embedded crypto library, or
  ~200 lines self-contained) — nothing like a TLS stack.
- **Security posture, stated explicitly** (in code comments and docs):
  SP1 sends the token in cleartext and a captured beat can be replayed to
  **mask a real outage** — that's what `require_hmac` exists to close. SP2
  authenticates the *message*, not the transport: an attacker can still drop
  datagrams (UDP guarantees nothing anyway); HMAC protects against forged
  aliveness, not censorship. UDP source addresses are spoofable — never treat
  the source IP as identity.

### Considered and deferred (do not implement in V1)

- **`.\n` repeat-beat on an established TCP connection** (MQTT
  `PINGREQ`-style). Deferred deliberately: it converts message
  authentication into session authentication — an on-path attacker could
  hijack the TCP session and keep dotting to mask an outage without holding
  the key — and the byte savings are marginal (TCP/IP headers dominate;
  byte-starved devices should use UDP). If ever revisited, the safe
  semantics are: dot = repeat the connection's most recently accepted full
  line, at least one full line before any dot, a re-auth interval (~15 min)
  after which dots are rejected and a full line is required again, and SP2
  counters advance only on real signed lines.

### Open questions (settle during implementation, prefer the listed default)

- **SaaS exposure**: raw TCP/UDP ports on solidping.io need LB support and
  possibly per-region entry points. V1 targets self-hosted + the k8xp dev
  deployment first.
