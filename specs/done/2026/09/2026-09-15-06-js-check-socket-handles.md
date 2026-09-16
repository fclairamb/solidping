---
model: opus
effort: high
---

# A JavaScript check can probe a port but cannot hold a conversation: no TCP, UDP or WebSocket connection handle

## Problem

The `js` check already runs the one-shot forms of the three transports. The
typed wrappers registered in
[`checker.go:389`](../../server/internal/checkers/checkjs/checker.go#L389)
make `solidping.tcp({...})`, `solidping.udp({...})` and
`solidping.websocket({...})` run the corresponding checker whole and hand back
its verdict. That covers "is the port open" and "does one payload get one
expected reply". It does not cover a *conversation*: connect once, write,
read, decide in JS what to send next, write again, close.

That is exactly the class of target the JavaScript check exists for, and the
one the fixed-shape checkers cannot express:

- Redis `AUTH` then `PING`, where the second message depends on the first
  succeeding.
- A challenge-response handshake, where the reply to the greeting has to be
  computed from the greeting.
- A length-prefixed binary protocol, where how many bytes to read next is
  in the bytes already read.
- A WebSocket feed: send a `subscribe` frame, then wait for the one event
  that proves the backend behind the socket is alive, ignoring the frames
  before it.

A script author who hits one of these today has two dead ends. Wrapping the
target in an HTTP endpoint is not monitoring the target. Adding `steps` to
the tcp check config would give multi-step exchanges without JS, but it
cannot branch on a reply, parse a prefix or compute a response, which is the
whole reason a script is involved.

The runtime already has the shape this needs, twice. `http.session()`
([`checker.go:525`](../../server/internal/checkers/checkjs/checker.go#L525),
[`httpsession.go`](../../server/internal/checkers/checkjs/httpsession.go))
is a stateful handle whose calls block and return values. `browser.open()`
([`browser.go:90`](../../server/internal/checkers/checkjs/browser.go#L90))
is a stateful handle with a lifecycle owned by `Execute`, per-call timeouts
clamped to the check's remaining time, its own action budget, and a settled
rule for what is returned versus thrown (spec 2026-09-12-06). The transport
pieces exist too: the accumulate-until-match read loop in
[`exchange.go:101`](../../server/internal/checkers/checkerdef/exchange.go#L101)
shared by the tcp and udp checkers, `coder/websocket` in
[`checkwebsocket/checker.go:147`](../../server/internal/checkers/checkwebsocket/checker.go#L147),
the tunnel dialer seam in
[`tunnel.go:48`](../../server/internal/checkers/checkerdef/tunnel.go#L48),
and dial-error classification in
[`netfailure.go:94`](../../server/internal/checkers/checkerdef/netfailure.go#L94).
Nothing new has to be invented; the work is composing what is there behind
three globals.

Two facts about the runtime shape the design and are not negotiable:

- **It is synchronous.** goja with no event loop. `http.*`, `sleep` and every
  `page.*` method block the script and return a value. The socket API must
  do the same: blocking calls with per-call timeouts, no events, callbacks
  or promises. This keeps the interrupt model in `Execute` intact.
- **goja strings cannot carry raw bytes.** The `base64` global is documented
  as text-only for that reason
  ([`checker.go:361`](../../server/internal/checkers/checkjs/checker.go#L361)).
  Bytes therefore always cross the JS boundary encoded, using the same
  encoding names the tcp check's `send_encoding` already accepts.

## Proposal

### 1. The API

Three new globals, registered next to `http` and `browser`. Every call blocks
and returns an object; nothing is asynchronous.

```js
// TCP, with optional TLS. Address is host:port, exactly like the tcp check.
var c = tcp.connect("redis.acme.com:6379", { tls: true, timeout: "3s" });
if (!c.ok) return { status: "down", output: { error: c.error, class: c.class } };
c.write("AUTH " + secrets.REDIS_PASSWORD + "\r\n");
var r = c.read({ until: "\r\n", timeout: "2s" });   // { ok, data, bytes, duration, timedOut, eof }
c.write("PING\r\n");
r = c.read({ until: "\r\n" });
c.close();
return { status: r.data === "+PONG\r\n" ? "up" : "down", output: { reply: r.data } };

// UDP: a connected socket, one datagram per call. Bytes go in and out as hex.
var u = udp.open("game.acme.com:27015");
u.send("ffffffff54536f7572636520456e67696e6520517565727900", { encoding: "hex" });
var d = u.receive({ timeout: "2s", encoding: "hex" });   // { ok, data, bytes, duration, timedOut }

// WebSocket: the handshake result sits on the handle, then frames.
var ws = websocket.connect("wss://feed.acme.com/v1", {
  headers: { Authorization: "Bearer " + secrets.TOKEN },
});
if (!ws.ok) return { status: "down", output: { error: ws.error, statusCode: ws.statusCode } };
ws.send(JSON.stringify({ op: "subscribe", channel: "heartbeat" }));
var f = ws.receive({ timeout: "5s" });   // { ok, type: "text"|"binary", data, bytes, duration, timedOut }
ws.close({ code: 1000 });
```

**`tcp.connect(address, options)`** returns a handle.

| Option | Type | Default | Meaning |
|---|---|---|---|
| `timeout` | duration string or ms | check's remaining time | Connect (and TLS handshake) budget, clamped like every other per-call timeout |
| `tls` | boolean | `false` | Wrap the connection in TLS after connecting |
| `tlsVerify` | boolean | `true` | `false` skips certificate verification, the `tls_verify: false` of the tcp check |
| `tlsServerName` | string | host part of `address` | SNI / verification name |
| `ipVersion` | `"auto"`, `"ipv4"`, `"ipv6"` | `IPVersionFrom(execCtx)` | Address family, resolved and selected with `checkerdef.SelectIPAddr` exactly as the tcp checker does |

Handle fields: `ok`, `error`, `class` (the `ClassifyDialError` class when
`ok` is false, may be empty), `remoteAddr` (the `ip:port` actually dialed),
`ipVersion`, `connectDuration` (ms), `tls` (`{ version, cipherSuite }`, the
same strings the tcp check puts in `tls_version` / `tls_cipher_suite`, only
when TLS is on), `tunneled` (only when true, see §5).

Handle methods:

- `write(data, { encoding })` → `{ ok, bytes, duration }`. `encoding` is one
  of the names `checkerdef.DecodePayload` accepts
  ([`payload.go:54`](../../server/internal/checkers/checkerdef/payload.go#L54)):
  `text` (default), `escaped`, `hex`. One write, bounded by the execution
  deadline. Capped at 64 KiB per call (the script size cap; nothing a script
  legitimately sends is bigger than the script).
- `read(options)` → `{ ok, data, bytes, duration, timedOut, eof }`. Exactly
  one of `bytes` (read exactly n), `until` (accumulate until the delimiter is
  present), `pattern` (accumulate until the RE2 matches, the `expect_pattern`
  semantics), or none (return the next chunk the kernel hands over, whatever
  its size). `encoding` selects how `data` comes back: `text` (default) or
  `hex`. `maxBytes` bounds the accumulation for this call, default 64 KiB,
  hard ceiling `checkerdef.MaxPayloadBytes`. `timeout` as everywhere.
- `close()` → `{ ok: true }`. Idempotent, uncounted, like `page.close()`.

**`udp.open(address, options)`** returns a handle over a *connected* UDP
socket (`net.Dialer.DialContext("udp", ...)`, the same call
[`checkudp/checker.go:169`](../../server/internal/checkers/checkudp/checker.go#L169)
makes). Options: `timeout` (resolution only; UDP has no handshake),
`ipVersion`. Handle fields: `ok`, `error`, `remoteAddr`, `ipVersion`.

- `send(data, { encoding })` → `{ ok, bytes, duration }`. One datagram.
- `receive({ timeout, encoding, maxBytes })` → `{ ok, data, bytes, duration,
  timedOut }`. One datagram per call; a datagram longer than `maxBytes`
  (default 64 KiB, which is also the protocol maximum) is truncated and
  `bytes` reports what was kept. There is no `until`: UDP is message-based
  and a script that wants several datagrams loops.
- `close()`.

A connected UDP socket surfaces an ICMP port-unreachable on the *next* call,
as Go does. The doc says so, and `receive` reports it as `{ ok: false, error,
class: "refused" }` rather than a timeout.

**`websocket.connect(url, options)`** returns a handle. Options: `headers`,
`timeout` (handshake budget), `tlsVerify` (default `true`; `false` is the
checker's `tls_skip_verify`). Handle fields: `ok`, `error`, `statusCode`
(the handshake's HTTP status when the server answered at all: `101` on
success, the rejecting status otherwise, absent when the dial failed before
any response), `tunneled`.

- `send(data, { type, encoding })` → `{ ok, bytes, duration }`. `type` is
  `text` (default) or `binary`; `encoding` decodes `data` first, so a binary
  frame is `send("cafe", { type: "binary", encoding: "hex" })`.
- `receive({ timeout, encoding, maxBytes })` → `{ ok, type, data, bytes,
  duration, timedOut }`. One frame per call. `coder/websocket` answers pings
  inside `Read`, so a script never sees control frames. `maxBytes` is
  applied with `conn.SetReadLimit` per call; a frame over the limit is
  `{ ok: false, error }` and the connection is closed by the library, which
  the handle reflects on the next call.
- `close({ code, reason })` → `{ ok: true }`. Default code `1000`. Idempotent.

The names `tcp`, `udp` and `websocket` collide with the sub-check wrappers
`solidping.tcp` / `solidping.udp` / `solidping.websocket` only in
vocabulary, not in scope, and the `browser` global already set that
precedent against `solidping.browser` (spec 2026-09-12-06 §1): the
sub-check runs a whole check, the global drives a connection.

### 2. What is returned and what is thrown

The browser global settled the rule and this spec copies it verbatim
([`browser.go:353`](../../server/internal/checkers/checkjs/browser.go#L353)):
a failure that is the *target's* verdict comes back as a value, a failure
that is the *script's* or the *operator's* problem throws.

Returned as `{ ok: false, error, ... }`:

- Connect failures of every kind: refused, reset, unresolvable, connect
  timeout, TLS handshake failure, WebSocket handshake rejected. The handle
  itself carries `ok: false`; its methods then return
  `{ ok: false, error: "not connected" }` so a script that forgot to check
  `c.ok` fails loudly at the next line instead of dereferencing `undefined`.
- Read/write failures after connect: peer closed (`eof: true`), reset, the
  per-call timeout (`timedOut: true`, with the partial `data` accumulated so
  far), the per-call byte cap hit without a match (`error` names the cap,
  the same sentence `Exchange.Run` uses).
- An invalid option value: a bad `timeout` string, an unknown `encoding`, a
  `pattern` that does not compile, `bytes` and `until` given together. This
  matches `http.*`, which returns `{ error }` for a bad option
  ([`checker.go:711`](../../server/internal/checkers/checkjs/checker.go#L711)).
- A budget refusal (§4), exactly as the sub-check and browser-action budgets
  already do.

Thrown (the run reports `error`):

- The type gate (§5), like `browser.open()` under a disabled `browser` type
  (`errBrowserTypeDisabled`).
- The execution deadline expiring while a call is blocked. The binding
  panics with `execCtx.Err()` exactly as `sleep` does
  ([`checker.go:334`](../../server/internal/checkers/checkjs/checker.go#L334)),
  which is what lets `resultFor` report `timeout` rather than a script error.
  This is distinct from the per-call `timeout` option, which is a value.

### 3. Implementation

Two new files in `checkjs`, no new package:

- `socket.go`: the `tcp` and `udp` globals over one `socketHandle` struct
  wrapping a `net.Conn`, with a `kind` that decides which methods are bound
  (`write`/`read` for TCP, `send`/`receive` for UDP) and whether `until` /
  `pattern` / `bytes` are accepted.
- `websocket.go`: the `websocket` global over a `*websocket.Conn`.

Both register through `registerGlobals`
([`checker.go:256`](../../server/internal/checkers/checkjs/checker.go#L256)).

**Dialing** reuses the tcp checker's direct path piece by piece rather than
importing `checktcp`: `checkerdef.LookupIPAddr`, `checkerdef.SelectIPAddr`
with the version above, a plain `net.Dialer`, then `tls.Client` when asked.
`ClassifyDialError` fills `class`. The WebSocket handle mirrors
`WebSocketChecker.dial`: `DialOptions.HTTPHeader`, a private
`http.Transport` only when `tlsVerify: false` or a tunnel dialer is present,
default client otherwise.

**The read loop is extracted, not copied.** `Exchange.Run`
([`exchange.go:101`](../../server/internal/checkers/checkerdef/exchange.go#L101))
already contains accumulate-until-match with a byte cap and timeout
classification. Pull the loop into a `checkerdef.ReadUntil(conn, deadline,
match func([]byte) bool, limit int)` that `Exchange.Run` calls with its
existing `MaxExchangeReadSize` and the handle calls with the per-call
`maxBytes`. One implementation of "read until", one cap message. The tcp
and udp checkers' tests must pass unchanged after the extraction; that is
the proof it was a refactor.

**Deadlines.** Every blocking call derives its context with the rule
`pageCallContext` implements
([`browser.go:399`](../../server/internal/checkers/checkjs/browser.go#L399)):
`timeout` parsed by `optionTimeout`, clamped to the check's remaining time,
never widening it. Rename that helper to `callContext` and share it. For
`net.Conn` the context's deadline becomes `SetReadDeadline` /
`SetWriteDeadline`; a goroutine is not needed. After the call returns, the
handle asks which deadline fired: the per-call one gives `timedOut: true`,
the execution one panics (§2). For `coder/websocket`, `Read(ctx)` and
`Write(ctx, ...)` take the derived context directly.

**Lifecycle.** Every handle is appended to `jsRuntime.sockets`. `Execute`
gains `defer runtime.closeSockets()` beside the existing
`defer runtime.closeBrowser()`
([`checker.go:137`](../../server/internal/checkers/checkjs/checker.go#L137)),
so a script that returns early, throws or is interrupted never leaks a
socket. `close()` is idempotent on both paths.

**Metrics.** Nothing is recorded automatically. `connectDuration` and the
per-call `duration` fields give the script what it needs to put into
`metrics` itself, the same contract as `http.*`'s `duration`.

### 4. Budgets and caps

| Limit | Value | Counted where |
|---|---|---|
| Connections opened per execution (`tcp` + `udp` + `websocket`) | 5 | at `connect` / `open`, whether or not earlier ones were closed |
| Each `connect` / `open` | 1 unit of the existing 20-call budget (`maxSubChecks`, [`checker.go:65`](../../server/internal/checkers/checkjs/checker.go#L65)) | same counter `http.*` and `solidping.*` use |
| Socket actions (`write`/`read`/`send`/`receive`) | 200 per execution, `maxSocketActions` | a separate counter, like `maxBrowserActions` ([`browser.go:22`](../../server/internal/checkers/checkjs/browser.go#L22)); `close()` is uncounted |
| Bytes read across all handles | `checkerdef.MaxPayloadBytes` (1 MiB) | shared with the HTTP body cap, so there is still ONE number |
| Bytes per `read` / `receive` | 64 KiB default via `maxBytes`, ceiling 1 MiB | per call |
| Bytes per `write` / `send` | 64 KiB | per call |

Counting happens *before* the action runs, so a refused call still spends
its unit; that is the browser rule and the reason a script cannot spin the
refusal path for free.

The connects share the 20-call budget rather than getting a pool of their
own because `http.*` set that rule and one number is easier to document. A
script that spends 18 calls on HTTP has two connections left; that is the
intended trade.

### 5. Type gate, tunnels, address family

**Type gate.** `tcp.connect` consults `TypeEnabled(CheckTypeTCP)`,
`udp.open` the `udp` type and `websocket.connect` the `websocket` type, with
the same message the sub-check gate emits ("check type X is disabled on this
server"). An operator who turned a transport off does not get it back
through a script. The gate is server-level and not org-aware, the known
limitation documented on `jsRuntime.check`; this spec does not widen it.

**Tunnels.** `tcp.connect` and `websocket.connect` honor
`checkerdef.TunnelDialerFrom(r.execCtx)`: when a dialer is present they hand
it the raw `host:port`, skip local resolution, set `tunneled: true`, and
leave `remoteAddr` / `ipVersion` absent, exactly as the tcp and websocket
checkers do. `udp.open` under a tunnel returns `{ ok: false, error: "UDP
cannot be tunneled" }`: SSH direct-tcpip forwards TCP only. An `ipVersion`
option combined with a tunnel is rejected the way the API rejects the pair
on a check config.

Today no dialer can reach a `js` execution: the type does not declare
`SupportsTunnel`
([`types.go:359`](../../server/internal/checkers/checkerdef/types.go#L359)),
so the API refuses `tunnelCheckUid` on it and the worker never establishes
one. Spec 2026-09-15-07 changes that. This spec still writes the tunnel
branch and tests it with a stub dialer on the context, so that when the
flag flips the sockets are already correct.

**Address family.** `ipVersion` is a per-call option because the `js` type
does not declare `SupportsIPVersion` either. Its default is
`IPVersionFrom(execCtx)`, which is `auto` today and becomes the check's pin
the day the type declares support, with no change here.

### 6. Documentation and samples

- `web/docs/docs/features/javascript-checks.md`: three new API-reference
  sections after `browser`, anchored `{#tcp}`, `{#udp}`, `{#websocket}`,
  each with the option and result tables above. The Limits table
  ([line 369](../../web/docs/docs/features/javascript-checks.md#L369)) gains
  the rows of §4. The `base64` section gets one sentence pointing binary
  traffic at `encoding: "hex"`.
- Three new tested full examples, each preceded by the
  `<!-- test: name -->` comment the harness in
  [`docs_examples_test.go:43`](../../server/internal/checkers/checkjs/docs_examples_test.go#L43)
  recognizes: `tcp-redis-ping` (AUTH then PING against a minimal RESP
  fixture), `udp-query` (send a datagram, assert the reply), and
  `websocket-subscribe` (subscribe, skip unrelated frames, assert the one
  event). The fixtures are in-process `net.Listen` / `net.ListenPacket` /
  `httptest` servers so the examples run in `make test`.
- Promote `tcp-redis-ping` to the sample picker in
  [`samples.go`](../../server/internal/checkers/checkjs/samples.go) as
  `js-tcp-redis-ping`, asserted character for character against the doc
  fence, the way `js-browser-login` is.
- `server/internal/app/docsres/` is a build copy (`make copy-docs`); it is
  not edited by hand.
- Changelog entry under Features: "JavaScript checks can open TCP, UDP and
  WebSocket connections and drive them step by step."

### 7. Tests

Table-driven, in `checkjs/socket_test.go`, `checkjs/websocket_test.go` and
additions to `docs_examples_test.go`. Every negative has a positive control
next to it, so a test cannot pass by the feature being absent.

- **Conversations.** TCP `until`, `bytes`, `pattern` and bare reads against
  a loopback fixture; TLS via `tls.Listen` with a self-signed certificate,
  `tlsVerify: false` succeeds and reports `tls.version`, `tlsVerify: true`
  fails with an `error` naming the certificate. UDP echo. WebSocket text
  and binary frames, `statusCode: 101`, a server that rejects with `401`
  gives `ok: false, statusCode: 401`.
- **Verdict values.** Refused gives `class: "refused"`; an unresolvable
  name gives `ok: false` with a resolve error; a peer that closes gives
  `eof: true`; a per-call timeout gives `timedOut: true` with the partial
  data.
- **Execution deadline.** A script blocked in `read` when the check's
  deadline expires ends with status `timeout`, and the fixture observes the
  connection closed. Positive control: the same script with a short per-call
  `timeout` keeps the connection alive until `close()`.
- **Budgets.** The sixth `connect` is refused and the fixture's accept count
  stays at five; the 201st action is refused; 19 `http.get` calls then two
  `connect`s refuse the second; a `read({ until })` against a fixture that
  never sends the delimiter stops at `maxBytes` with the cap message and the
  shared 1 MiB counter is what a later `http.get` sees.
- **Gate.** With a stub `TypeEnabled` refusing `tcp`, `tcp.connect` throws
  and the fixture's accept count is zero; positive control with the gate
  open. Same for `udp` and `websocket`.
- **Tunnel.** A `checkerdef.DialerFunc` on the execution context that
  records the address it was given and returns a `net.Pipe` end: the handle
  connects to `private.invalid:6379` (a name that cannot resolve locally,
  the pattern of
  [`checkhttp/tunnel_test.go`](../../server/internal/checkers/checkhttp/tunnel_test.go)),
  the recorded address is verbatim, `tunneled` is true and `remoteAddr` is
  absent. `udp.open` under the same context returns the cannot-be-tunneled
  error and never dials.
- **Lifecycle.** `close()` twice is fine; a method after `close()` returns
  not-connected; a script that returns without closing still leaves the
  fixture seeing EOF once `Execute` returns.
- **Refactor proof.** `checkerdef`, `checktcp` and `checkudp` test suites
  pass unchanged after `ReadUntil` is extracted.
- **Docs.** `TestDocExamplesCompile` picks the new fences up on its own; one
  `TestDocExample*` per new tag runs the fence against its fixture, plus a
  wrong-password variant for `tcp-redis-ping` that must come back `down`.

### Decisions

1. Synchronous, value-returning API; no events, callbacks or promises.
2. Connect failure is a returned handle with `ok: false`, never a throw.
3. Bytes cross the boundary encoded, with the `DecodePayload` vocabulary
   (`text`, `escaped`, `hex`). `base64` was considered and dropped: `hex`
   already covers binary and the `base64` global is text-only by contract.
4. Connects share the 20-call budget; socket actions get their own budget
   of 200; bytes share the 1 MiB payload cap.
5. Five connections per execution, counted at open, closed ones included.
6. The read loop is extracted from `Exchange.Run` and shared, not copied.
7. Type gate applies per transport with the sub-check gate's message; gate
   refusal throws, like `browser.open()`.
8. Tunnel and `ipVersion` behavior mirror the tcp / websocket checkers,
   tested with a stub dialer even though the `js` type cannot be tunneled
   until spec 2026-09-15-07 lands.

### Open questions

None blocking. Resolved during design:

- *Own budget for connects?* No; shared, see §4.
- *Per-read default cap?* 64 KiB, see §4.
- *`readLine` sugar?* Not in v1; `read({ until: "\n" })` is one option away.

### Out of scope

- An event or callback API, and anything requiring an event loop.
- Listening sockets, TLS client certificates, WebSocket subprotocols and
  compression, HTTP or SOCKS proxies.
- Making the `js` type tunnel-capable (spec 2026-09-15-07) or
  `ipVersion`-capable at the check level.
- An org-aware type gate (same follow-up as the sub-check and browser gates).
- Dashboard work: the editor is plain CodeMirror JavaScript mode with no
  API-specific completions, so there is nothing to update.

## Implementation Plan

1. Extract `checkerdef.ReadUntil` from `Exchange.Run`; run the `checkerdef`,
   `checktcp` and `checkudp` suites to prove the refactor.
2. Rename `pageCallContext` to `callContext`; add the socket counters, the
   handle slice and `closeSockets()` to `jsRuntime` and `Execute`.
3. `socket.go`: `tcp.connect`, `udp.open`, the handle and its methods,
   the gate, the tunnel branch, `ClassifyDialError` on failures.
4. `websocket.go`: `websocket.connect` and its methods.
5. Tests of §7, negatives with positive controls.
6. Docs: API sections, Limits rows, three tested examples; promote the TCP
   one to `samples.go` with the character-for-character assertion.
7. Changelog entry. `make lint`, `make test`.
