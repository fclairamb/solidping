---
sidebar_position: 24
title: JavaScript checks
---

# JavaScript checks

A `js` check runs a script you write against a real, sandboxed JavaScript
engine ([goja](https://github.com/dop251/goja)) once per execution. It exists
for everything the fixed check types cannot express: log in through a real
form and confirm you get a session, chain a bearer-token login into an
authenticated call, aggregate several other checks into one result, or skip a
probe outside business hours.

This page documents the runtime's actual API surface — every global a script
can call, what it returns, and where its limits are — with full, tested
examples for the workflows above. Every fenced example on this page is parsed
out and compiled by a Go test
([`docs_examples_test.go`](https://github.com/fclairamb/solidping/blob/main/server/internal/checkers/checkjs/docs_examples_test.go))
that fails the build on a syntax error, and the ones tagged `test:` in the
source are additionally **executed** against a real HTTP fixture and asserted
on their result — so an example on this page is never a screenshot of code
that used to work.

## How a script runs

- The engine is [goja](https://github.com/dop251/goja), a pure-Go ECMAScript
  implementation — **not** a browser and **not** Node.js.
- Your script is wrapped in a function, so a top-level `return` works and is
  how you report the result: `(function() { <your script> })()`.
- Execution is **synchronous**. There is no `fetch`, no `Promise`/`async`, no
  `setTimeout`, no `require`, no DOM. `sleep(ms)` (below) is the only way to
  wait, and the time it sleeps counts against the check's timeout.
- One execution per region per period — the same schedule as any other check
  type.
- The check's `timeout` (default and maximum `30s`) interrupts the running
  script; a script that has not returned by then never gets to report its own
  status — see [Result contract](#result-contract).

## Configuration

| Config key | Type | Set from | Notes |
|---|---|---|---|
| `script` | string, ≤ 64 KB | Dashboard form, API, `sp`, config-as-code | Code editor |
| `env` | map of string → string, ≤ 50 entries | Dashboard form, API, `sp`, config-as-code | Public, plaintext — see below |
| `secrets` | map of string → string, ≤ 50 entries | Dashboard form, API, `sp`, config-as-code | Encrypted, write-only in the form — see below |
| `timeout` | duration string, ≤ `30s` | API, `sp`, config-as-code | Defaults to `30s`. The one field with **no** dashboard control |
| `tunnelCheckUid` | uid of an `ssh` check | Dashboard form (Advanced), API, `sp`, config-as-code | Runs the script's `http.*`, `http.session()`, and `tcp`/`udp`/`websocket` handles through that bastion — see below |

**The dashboard form edits `script`, `env`, and `secrets`.** Only `timeout` has
no field in the `js` check form — it is set through the REST API, the `sp`
CLI, or a [config-as-code](./config-as-code.md) document. An existing
`timeout` value is preserved untouched when you save the form for something
else (the same `ownedKeys` mechanism every check-type form uses), so scripting
a check's timeout via the API and then tweaking its name in the dashboard does
not silently reset it.

The `secrets` editor is **write-only**: like every other credential field in
the dashboard, once a value is saved it is never shown again — the section
instead shows "•••• (encrypted — enter new values to replace)" in place of the
stored value. Leaving the section untouched preserves the stored credential;
editing or removing a row is what changes it (adding a new, still-blank row
does not — that lets you add a second credential without retyping the first).
`env` has no such restriction — it is plaintext, so the form shows and edits
it like any other field.

### Script parameters: `env` and `secrets`

A script reads its parameters from two maps, and the difference between them
is where the value is stored:

| Config key | Storage | Engine global | For |
|---|---|---|---|
| `env` | public `config`, plaintext | `env.BASE_URL` | non-secret parameters — a base URL, a username, a threshold |
| `secrets` | encrypted, never returned on a read | `secrets.PASSWORD` | credentials |

`env` values come back on `GET` and appear in a config-as-code export, so they
are diffable and reviewable. `secrets` values do not: they are stripped from
every export, never echoed to the dashboard, and **preserved when a write
omits the key** — which is what lets you edit anything else on the check
without re-entering the credential.

**A password goes in `secrets`, never in `env`.** `env` is public config: it
comes back on every `GET`, is present in every export, and is exactly as
visible as the check's name. If a script needs a credential, it belongs in
`secrets`.

Both maps accept [secret references](./config-as-code.md) — `${param:my-key}`
and `${env:MY_VAR}` — so a tracked manifest can carry
`secrets: { PASSWORD: "${param:sso-password}" }` and nothing sensitive in git.

```json
{
  "env": { "BASE_URL": "https://acme.com", "USERNAME": "probe" },
  "secrets": { "PASSWORD": "${param:sso-password}" }
}
```

### Running through an SSH tunnel

A `js` check can carry a `tunnelCheckUid` [like any other tunnel-capable
type](./ssh-tunnels.md). Once set, three rules apply:

- `http.*`, `http.session()`, and the `tcp`/`udp`/`websocket` handles are
  dialed through the bastion — the hostname is resolved on the far side, not
  by the worker, and a tunneled `http.<method>()` response gains
  `tunneled: true` (`udp.open()` is still refused: UDP cannot be tunneled).
- A sub-check of a type that itself does **not** support tunneling
  (`solidping.udp(...)`, `solidping.icmp(...)`, `solidping.dns(...)`, …)
  is refused with an error result rather than silently run from the
  worker's own network.
- `browser.open()` throws: Chrome has its own network stack and cannot be
  routed through the tunnel.

## Result contract {#result-contract}

The script's `return` value is an object with up to three fields:

| Field | Type | Meaning |
|---|---|---|
| `status` | string | `"up"`, `"down"`, or `"error"` — see below. Any other **unrecognized** value is treated as `"error"`. |
| `metrics` | object of numbers | Shown as the check's metrics, same as any other check type |
| `output` | object | Shown on the result's detail page; merged with `console.*` output under `output.console` |

**A script must return `"up"`, `"down"`, or `"error"` — never `"timeout"`.**
`timeout` is reserved for the runtime: it is what the check reports when the
engine cuts the script off after its own `timeout` elapses, not something a
script decides to return. If a script could return `"timeout"` for, say, a
slow upstream it merely measured, the status would mean two different things
in the same check's history — "the runtime gave up on this script" and "the
script measured something slow" — and the one thing `timeout` is genuinely
useful for (telling those two apart) would be lost. Report a slow-but-answered
upstream as `"down"` and let its `metrics` carry the latency.

This is a contract on **what a script should do**, not (yet) a rule the engine
enforces: today, a script that explicitly `return`s `{ status: "timeout" }` is
still accepted and reported as the timeout status, the same as if the runtime
had cut it off — the engine does not currently tell the two apart. Treat this
as reserved rather than relying on it; a future release may tighten the engine
to reject a script-returned `"timeout"` as `"error"` instead.

A missing or unrecognized `status`, a script that throws, or a script that
never calls `return` an object at all are every one of them reported as
`"error"` — not `"down"`. That distinction matters for alerting: `"down"`
means the script checked something and it failed; `"error"` means the script
itself is broken (a bug, a typo, an unhandled exception) and needs a fix, not
an incident on the target.

```js
// The minimal contract: nothing else is required.
return { status: "up" };
```

```js
// A more complete return.
return {
  status: "down",
  metrics: { latencyMs: 340 },
  output: { reason: "unexpected status 503" },
};
```

## API reference

### `console`

`console.log/warn/error/info(...)` behave like their browser namesakes.
Every call is appended to a buffer (capped at 16 KB) that appears as
`output.console` in the result — the one place to look when a script's own
`return` does not explain what happened.

```js
console.log("about to call", env.BASE_URL);
```

### `sleep(ms)`

Blocks for `ms` milliseconds. It is the only way to wait inside a script — no
`setTimeout` exists — and the wait counts against the check's `timeout`.

```js
sleep(200); // wait 200ms, e.g. to let an async side effect settle
```

### `env` / `secrets`

Read-only objects built from the `env` and `secrets` config maps —
`env.BASE_URL`, `secrets.PASSWORD`. See
[Script parameters](#script-parameters-env-and-secrets) above. A name present
in `env` is `undefined` under `secrets` and vice versa — the two never merge.

### `base64.encode` / `base64.decode`

goja has no `btoa`/`atob` (those are browser APIs, not part of ECMAScript),
so a script that needs to build a `Basic` auth header or decode a base64
token has nothing built in for it. `base64.encode`/`base64.decode` fill that
one gap:

| Function | Signature | Notes |
|---|---|---|
| `base64.encode` | `(text: string) => string` | Standard, padded encoding (`StdEncoding`) — the encoding HTTP Basic auth requires |
| `base64.decode` | `(text: string) => string` | **Throws** on malformed input — it never returns an empty or partial string |

```js
base64.encode("user:pass") === "dXNlcjpwYXNz"; // true
```

`decode`'s throw-on-malformed-input behavior is deliberate: a silently empty
string on a typo'd or truncated value would turn a broken credential into a
check that probes with an empty password and reports `"down"` — with nothing
in the script itself pointing at why. A thrown error surfaces as `"error"`
with the message in `output.error`, which is the honest outcome for "this
input was not valid base64".

**Text only.** goja strings are UTF-16; `decode`'s output is only meaningful
for text that was originally text (a `user:pass` pair, a JSON token) —
arbitrary binary does not round-trip through a JS string. Every use on this
page is exactly that kind of text. For genuinely binary traffic — a DNS query,
an RCON packet, a length-prefixed frame — use the socket handles' `encoding:
"hex"` instead ([`tcp`](#tcp), [`udp`](#udp), [`websocket`](#websocket)), which
is the one representation that survives the boundary intact.

### `http.<method>(url, options)`

`http.get/post/put/patch/delete/head(url, options)` performs one request.
These functions are **stateless** — nothing is carried from one call to the
next. For a flow that needs cookies across calls, see
[`http.session()`](#httpsession) below.

**Options**

| Option | Type | Default | Meaning |
|---|---|---|---|
| `body` | string | — | Request body |
| `headers` | object | — | Request headers, `{name: value}` |
| `followRedirects` | boolean | `true` | `false` returns the 3xx itself, with `Location` intact, instead of following it |
| `maxRedirects` | number | `10` | Redirect hops to follow; capped at 10 |
| `timeout` | duration string or number of ms | the check's own timeout | Never longer than the check's own timeout, regardless of what is asked for |

**Response**

| Field | Meaning |
|---|---|
| `statusCode` | HTTP status of the final response |
| `body` | Response body, capped at 1 MB |
| `headers` | Response headers, **canonical keys** (`Content-Type`, `Set-Cookie`) — a single value is a string, a repeated header is an array |
| `url` | The final URL, after any redirects that were followed |
| `redirects` | The chain that was walked: `[{statusCode, location}]`, empty when nothing was followed |
| `duration` | Milliseconds |
| `tunneled` | `true` when the check has a `tunnelCheckUid` and the request was dialed through it — present only when true, so an untunneled response's shape is unchanged. See [Running through an SSH tunnel](#running-through-an-ssh-tunnel) |
| `error` | Present **instead of** every field above when the request could not even be made (DNS failure, connection refused, …) — always check `resp.error` before reading `resp.statusCode` |

```js
var resp = http.get(env.BASE_URL + "/health");
if (resp.error) {
  return { status: "error", output: { error: resp.error } };
}
```

Stopping at a redirect is what makes an OAuth-style flow assertable — the
`302` carrying `code=` **is** the success signal there, and following it
would just land you on the relying party's own (unauthenticated) error page:

```js
var r = http.get(authorizeUrl, { followRedirects: false });
return { status: r.statusCode === 302 && /code=/.test(r.headers.Location) ? "up" : "down" };
```

### `http.session()` {#httpsession}

`http.session()` returns an object with the same six verbs, plus
`cookies(url)`, all backed by **one cookie jar**. Cookies set by one call —
including ones set *during* a redirect chain, which a bare `http.*` call
cannot carry even within itself — are carried into the next:

```js
var s = http.session();
var page = s.get(env.BASE_URL + "/auth"); // Set-Cookie captured
var r = s.post(page.url, {
  body: "user=" + env.USERNAME + "&pass=" + encodeURIComponent(secrets.PASSWORD),
  headers: { "Content-Type": "application/x-www-form-urlencoded" },
  followRedirects: false,
});
return { status: r.statusCode === 302 ? "up" : "down" };
```

`s.cookies(url)` returns the cookies that would be sent to that URL, as
`[{name, value, domain, path}]`, for assertions. The jar is per-execution — it
is never persisted between runs — and bounded (100 cookies, 4 KiB each) so a
misbehaving target cannot grow a check's memory without limit.

Every `http.*` call — session or not — counts against the same 20-call budget
as `solidping.*` below.

### `browser` {#browser}

`browser.open()` gives a script a **real headless-Chrome page** to drive — the
same Chrome the [`browser` check type](./check-types.md#browser) uses, through
the same concurrency cap and the same isolated (incognito) context. It is what
lets a script log in through a JavaScript-driven form, wait for a client-side
render, and read what a user would actually see.

It is deliberately small. There are no tabs, frames, downloads, uploads,
request interception, header/UA overrides, viewport emulation, video or
tracing; `page.evaluate()` is the escape hatch for everything the table does
not have. This is not Playwright and does not try to become it.

| Call | Returns | Notes |
|---|---|---|
| `browser.open()` | `page` | Acquires a browser slot, opens a fresh isolated context and tab. **Throws** — see below. |
| `page.goto(url)` | `{ ok, url, title, duration, error? }` | Navigates and waits for `body`. `url` must be `http`/`https`. `url` in the result is the URL **after** redirects; there is deliberately **no status code** — read one with `page.evaluate` or `http.get`. |
| `page.waitFor(selector, { timeout? })` | `{ ok, duration, error? }` | Waits for the selector to become visible. `timeout` is a duration string (`"10s"`) or a number of ms, clamped to the script's remaining time. |
| `page.click(selector)` | `{ ok, error? }` | Clicks the first match, scrolling it into view. |
| `page.fill(selector, text)` | `{ ok, error? }` | Clears the field and **sends keys**, so a React/Vue controlled input sees real input events. |
| `page.press(selector, key)` | `{ ok, error? }` | Sends one key — `"Enter"`, `"Tab"`, `"Escape"`, an arrow, or a single character. |
| `page.text(selector)` | `{ ok, text, error? }` | Visible text of the first match, capped at 1 MB. |
| `page.evaluate(expression)` | `{ ok, value, error? }` | Runs the expression **inside the page** (real DOM, page origin) and returns its JSON-serialisable value; a returned Promise is awaited. A throw inside the page is `{ ok: false, error }`. Capped at 1 MB. |
| `page.url()` | `string` | Current top-frame URL — how you assert you landed on `/dashboard`. |
| `page.cookies()` | `[{ name, value, domain, path, secure, httpOnly, expires }]` | The page's cookies, so a browser-established session can be handed to `http.*` — see the example below. |
| `page.screenshot()` | `{ ok, error? }` | Attaches a capture of the page to the result — see below. |
| `page.close()` | `undefined` | Disposes the page and releases the slot. Called for you when the script ends; calling it twice is a no-op. |

**Target failures return, infrastructure failures throw.** A selector that
never appeared, a page that threw, a navigation that failed — all of them come
back as `{ ok: false, error }`, so the script keeps the right to decide whether
that means `down`. Only *our* infrastructure failing (no Chrome on this
worker, the CDP connection dying mid-script, the page already closed) throws,
which the runtime reports as `error` — see
[Result contract](#result-contract). This is the same split `http.*` makes
with its `{ error }` return.

**One page per execution.** A second `browser.open()` throws, including after
a `page.close()`: the page is a resource this script already spent. The slot is
held from `open()` to `close()` — a script holding a page *is* a browser check
in flight — so on a saturated worker `open()` waits inside the check's own
timeout and then reports `timed out waiting for a free browser slot`, exactly
as a browser check does.

**No Chrome, no page.** `browser.open()` throws when this worker has no
browser, and the check reports `error`, never `down` — your monitored site is
not implicated by our sidecar being down. Give every region that runs `js`
checks a headless-shell sidecar (the shipped `docker-compose.yml` does), or
pin such checks to regions whose capability list shows `browser`; a `js` check
is scheduled as a `js` check, so nothing routes it to a browser-capable region
for you. An operator who disabled the `browser` check type
(`checkers.enabled`/`checkers.disabled`) disables it here too, with the same
message.

**Screenshots.** `page.screenshot()` attaches a capture to the result's
diagnostics, through the same size and time caps a browser check's capture
uses. The **last** successful call wins, so an early "before" shot can be
overwritten by the one taken at the moment you decide the target is down. The
capture is **kept only when the final status is `down` or `timeout`** — the
verdicts a browser check keeps one for — and dropped otherwise, so a shot on
an `up` run costs a CDP round-trip and nothing else. As with a browser check,
the image is what the page looked like when *the script asked*, not a frame
from the instant of failure — and, as with a browser check, it is a **WebP**
full-page capture. The format is not selectable from the script.

**Period floor.** A script that calls `browser.open(` is held to the `browser`
check's **1m** minimum period instead of the `js` type's 30s, decided when the
check is saved. A headless run costs seconds and holds one of four slots;
without the floor one such check would starve every browser check beside it.

### `rdp` {#rdp-object}

`rdp.connect(options)` gives a script a **real interactive Windows desktop** to
drive — the same logon path the [RDP check type](./check-types.md#rdp-remote-desktop)
uses, through its own concurrency cap (`rdp.connect` is **always** an
authenticated logon; there is no pre-auth variant of the object). It is what
lets a script log in to a Windows desktop, wait for it to settle, click and
type, and assert on what a user would actually see.

Assertions v1 are **pixels only**: `pixel`, `regionHash`, `waitForStable`,
`waitForChange`. Template matching and OCR are out of scope (they need cgo or
a sidecar, which SolidPing forbids).

| Call | Returns | Notes |
|---|---|---|
| `rdp.connect({ host, username, password, domain?, width?, height? })` | `session` | Connects, runs CredSSP/NLA, finishes the connection sequence. Auth failures and slot timeouts **return** `{ ok: false, failureCode?, timedOut? }`; script bugs and infrastructure **throw**. `width`/`height` default to 1280x800. |
| `session.waitForStable({ quietMs?, timeout? })` | `{ ok, duration, error? }` | Blocks until the screen has had no update for `quietMs` (default 2000), bounded by the timeout. A hung logon times out. |
| `session.waitForChange(timeoutMs)` | `{ ok, error? }` | Blocks until a new update arrives after stability — how you assert a stuck login screen never repainted. Timeout expiry is `{ ok: false, error }`, a value the script decides on. |
| `session.click(x, y)` | `{ ok, error? }` | Moves the pointer and presses+releases the **left** button. |
| `session.rightClick(x, y)` / `session.doubleClick(x, y)` | `{ ok, error? }` | Right button; double-click. |
| `session.move(x, y)` | `{ ok, error? }` | Repositions the pointer without pressing. |
| `session.type(text)` | `{ ok, error? }` | Sends text as **Unicode input events** (`TS_UNICODE_KEYBOARD_EVENT`), so the target's keyboard layout does not matter. |
| `session.key("ctrl+alt+end")` / `"win+r"` / `"enter"` | `{ ok, error? }` | Named keys and combos via **scancodes** — modifiers held, the last key pressed+released, modifiers released in reverse. |
| `session.pixel(x, y)` | `{ ok, r, g, b, error? }` | One pixel as `{ r, g, b }`. Out-of-bounds coordinates are `{ ok: false, error }`. |
| `session.regionHash(x, y, w, h)` | `{ ok, hash, error? }` | Stable FNV-1a hash of a rectangle of the framebuffer — same visible pixels give the same hash across runs and workers. |
| `session.screenshot()` | `{ ok, error? }` | Attaches a PNG capture to the result's diagnostics — the browser rules: last call wins, kept only on `down`/`timeout`. |
| `session.logoff()` | `{ ok, error? }` | Asks the **server** to end the session (TS_SHUTDOWN_REQUEST). |
| `session.disconnect()` | `{ ok }` | Drops the transport only — the session survives until the server's idle policy ends it. |
| `session.close()` | `undefined` | Alias for `logoff()`, for parity with `browser`. |

**Every authenticated session is a real interactive Windows logon** — the same
[operational caveats](./check-types.md#warning-authenticated-runs-are-real-windows-logons)
as the check apply: profile load, logon scripts/GPOs, an RDS licence, a
possible kicked user, a Security event-log entry per run. Use a dedicated
monitoring account. Keep the interval long: **the period floor is 15 minutes**
for any script that calls `rdp.connect(`, decided when the check is saved —
stricter than the browser floor, and it wins when a script uses both.

**Ending the session.** `logoff()` and `disconnect()` are both explicit. A
script that ends without calling either gets a log off — the check's default —
and the slot is released when the script ends whatever it did, so an early
`return`, a throw, or an interrupt never leaks a session.

### `tcp` {#tcp}

`tcp.connect(address, options)` gives a script a **live TCP connection** to
drive: connect once, write, read, decide in JavaScript what to send next, write
again, close. That is the difference between "is the port open" — which
[`solidping.tcp()`](#solidpingtypeconfig--solidpingchecktype-config) already
answers — and a *conversation*: a Redis `AUTH` then `PING`, a challenge-response
handshake, a length-prefixed binary protocol.

Every call **blocks and returns a value**. There are no events, callbacks or
promises: the runtime has no event loop, and `sleep` and `http.*` behave the
same way.

`address` is `host:port`, exactly like the `tcp` check's config.

**`tcp.connect` options**

| Option | Type | Default | Meaning |
|---|---|---|---|
| `timeout` | duration string or number of ms | the check's remaining time | Connect *and* TLS-handshake budget, clamped to the check's own timeout and never widening it |
| `tls` | boolean | `false` | Wrap the connection in TLS once connected |
| `tlsVerify` | boolean | `true` | `false` skips certificate verification — the `tls_verify: false` of the `tcp` check |
| `tlsServerName` | string | host part of `address` | SNI / verification name |
| `ipVersion` | `"auto"`, `"ipv4"`, `"ipv6"` | `"auto"` | Address family, resolved and selected exactly as the `tcp` checker does |

**Handle fields**

| Field | Meaning |
|---|---|
| `ok` | Did the connection come up? **Always check this first** — every method on a failed handle returns `{ ok: false, error: "not connected" }` |
| `error` | Why it did not, when `ok` is false |
| `class` | Reachability class of a failed connect — `connection-refused`, `connect-timeout`, `network-unreachable`, `host-unreachable`. Absent when the failure has no class (a name that does not resolve has no address to trace to) |
| `remoteAddr` | The `ip:port` actually dialed |
| `ipVersion` | `ipv4` or `ipv6` |
| `connectDuration` | Milliseconds |
| `tls` | `{ version, cipherSuite }` — the same strings the `tcp` check reports — only when `tls: true` |
| `tunneled` | `true` when the connection went through an SSH tunnel; `remoteAddr` and `ipVersion` are then absent |

**Handle methods**

| Call | Returns | Notes |
|---|---|---|
| `write(data, { encoding?, timeout? })` | `{ ok, bytes, duration, error? }` | One write. `encoding` is `text` (default), `escaped` (`\r`, `\n`, `\xNN`) or `hex`. Capped at 64 KiB per call. |
| `read({ until?, bytes?, pattern?, maxBytes?, encoding?, timeout? })` | `{ ok, data, bytes, duration, timedOut, eof, error? }` | Accumulates until the criterion is met — see below. |
| `close()` | `{ ok: true }` | Idempotent and **uncounted**. Called for you when the script ends. |

`read` takes **at most one** criterion:

- `until: "\r\n"` — accumulate until the delimiter appears.
- `bytes: 4` — read exactly four bytes.
- `pattern: "^\\+OK"` — accumulate until the RE2 pattern matches, the
  `expect_pattern` semantics of the `tcp` check.
- none at all — return the next chunk the kernel hands over, whatever its size.

`maxBytes` bounds the accumulation for that one call (default 64 KiB, ceiling
1 MB). Hitting it without a match is `{ ok: false, error: "no match in the
first N bytes of the reply" }` with the accumulated `data` intact. `encoding`
selects how `data` comes back: `text` (default) or `hex`.

### `udp` {#udp}

`udp.open(address, options)` returns a handle over a **connected** UDP socket —
one datagram per call, because a datagram is the unit. Options: `timeout`
(resolution only; UDP has no handshake) and `ipVersion`. Handle fields: `ok`,
`error`, `class`, `remoteAddr`, `ipVersion`.

| Call | Returns | Notes |
|---|---|---|
| `send(data, { encoding?, timeout? })` | `{ ok, bytes, duration, error? }` | One datagram. Same `encoding` names as `tcp`'s `write`. |
| `receive({ timeout?, encoding?, maxBytes? })` | `{ ok, data, bytes, duration, timedOut, error? }` | One datagram. A datagram longer than `maxBytes` (default 64 KiB, which is also the protocol maximum) is truncated and `bytes` reports what was kept. |
| `close()` | `{ ok: true }` | Idempotent, uncounted. |

There is deliberately **no `until`**: UDP is message-based, and a script that
wants several datagrams loops. Passing `until`, `bytes` or `pattern` to
`receive` is an error rather than a silently ignored option.

A connected UDP socket surfaces an ICMP port-unreachable on the **next** call,
as the operating system does — `receive` reports it as
`{ ok: false, error, class: "connection-refused" }` rather than as a timeout,
which is the difference between "nothing is listening" and "the service is
slow".

**UDP cannot be tunneled.** An SSH `direct-tcpip` forward carries TCP only, so
`udp.open` under a tunnel returns `{ ok: false, error: "UDP cannot be
tunneled…" }` and never dials — it does not quietly fall back to probing from
the worker's own network.

### `websocket` {#websocket}

`websocket.connect(url, options)` performs the handshake and returns a handle
over the live connection. `url` is `ws://` or `wss://`. Options: `headers`
(`{name: value}`), `timeout` (handshake budget) and `tlsVerify` (default `true`;
`false` is the `websocket` check's `tls_skip_verify`).

Handle fields: `ok`, `error`, `class`, `connectDuration`, `tunneled`, and
`statusCode` — the handshake's HTTP status when the server answered at all
(`101` on success, the rejecting status otherwise), absent when the dial never
reached a server. A `401` on the upgrade and a connection that reached nothing
are different incidents, and this is what tells them apart.

| Call | Returns | Notes |
|---|---|---|
| `send(data, { type?, encoding?, timeout? })` | `{ ok, bytes, duration, error? }` | `type` is `text` (default) or `binary`; `encoding` decodes `data` first, so a binary frame is `send("cafe", { type: "binary", encoding: "hex" })`. |
| `receive({ timeout?, encoding?, maxBytes? })` | `{ ok, type, data, bytes, duration, timedOut, eof?, error? }` | One frame. Pings are answered inside the read, so a script never sees a control frame. |
| `close({ code?, reason? })` | `{ ok: true }` | Default code `1000`. Idempotent, uncounted. |

`maxBytes` is applied as the read limit for that call; a frame over it fails and
the connection is closed by the library, which the handle reflects on the next
call. **A `receive` that hits its per-call `timeout` also closes the
connection** — a half-read frame cannot be resumed — so unlike a `tcp` handle, a
WebSocket handle is finished once a `receive` times out. `timedOut: true` is
still a returned value, not a throw: the script decides what a silent feed means.

### Sockets: what is returned and what is thrown {#socket-errors}

The same split `browser` makes, for the same reason. A failure that is the
**target's** verdict comes back as a value; a failure that is the **operator's**
or the **runtime's** throws.

Returned as `{ ok: false, error, … }`:

- Connect failures of every kind — refused, reset, unresolvable, connect
  timeout, TLS handshake failure, a rejected WebSocket handshake.
- Read and write failures after connect — the peer closed (`eof: true`), a
  reset, the per-call `timeout` (`timedOut: true`, **with the partial `data`
  accumulated so far**), the byte cap hit without a match.
- An invalid option value: a bad `timeout` string, an unknown `encoding`, a
  `pattern` that does not compile, two read criteria at once.
- A budget refusal (see [Limits](#limits)).

Thrown, so the check reports `error` or `timeout`:

- A transport the operator **disabled** on this server
  (`checkers.enabled`/`checkers.disabled`), with the same message the sub-check
  gate emits. A script does not get a check type back that an operator turned
  off.
- The **check's own deadline** expiring while a call is blocked. That is a
  `timeout`, not a script error — and it is distinct from the per-call `timeout`
  option, which is a value. Whatever the script opened is disposed on the way
  out, so nothing leaks.

### `solidping.<type>(config)` / `solidping.check(type, config)`

Runs another check type's logic inline and returns its result, without
creating a separate check. `config` is the same key set as that check type's
config-as-code document (see [Check Types](./check-types.md)).

```js
var result = solidping.http({ url: "https://api.acme.com/health" });
// result: { status, duration, metrics, output }
```

Typed wrappers exist for every sub-checkable type:
`solidping.http/tcp/dns/ssl/icmp/smtp/udp/ssh/pop3/imap/websocket/postgresql/ftp/sftp/domain(config)`,
plus the generic `solidping.check(type, config)` that takes the type name as a
string.

- `js` and `heartbeat` cannot be used as a sub-check — calling either returns
  an error result naming the type, rather than recursing or hanging.
- A check type this server has disabled
  (`checkers.enabled`/`checkers.disabled`) also returns an error result rather
  than silently running — a script cannot use a type the operator turned off.
- Every `solidping.*` call counts against the same 20-call budget as
  `http.*`. The 21st call — of either kind, in any combination — returns an
  error result naming the limit instead of running.

## Limits

| Limit | Value |
|---|---|
| Script size | 64 KB |
| Sub-checks (`http.*` + `solidping.*` + each `connect`/`open` combined) | 20 per execution |
| Browser pages | 1 per execution |
| Browser actions (every `page.*` call except `url()`/`close()`) | 100 per execution, counted **separately** from the 20-call budget above |
| RDP sessions | 1 per execution |
| RDP actions (every `session.*` call except `logoff()`/`disconnect()`/`close()`) | 100 per execution, counted **separately** from the 20-call budget above |
| Connections (`tcp` + `udp` + `websocket` combined) | 5 per execution, counted at `connect`/`open` **whether or not earlier ones were closed** |
| Socket actions (`write`/`read`/`send`/`receive`; `close()` is free) | 200 per execution, counted **separately** from the 20-call budget |
| Console output | 16 KB |
| HTTP response body, `page.text()`, `page.evaluate()` | 1 MB (one shared cap) |
| Bytes read across all socket handles | 1 MB per execution — a socket read **spends** it, and the next HTTP response body is capped at whatever is left |
| Bytes per `read` / `receive` | 64 KiB by default via `maxBytes`, ceiling 1 MB |
| Bytes per `write` / `send` | 64 KiB |
| `env` / `secrets` entries | 50 each |
| Default / maximum timeout | 30s |
| Cookie jar (per session) | 100 cookies, 4 KiB each |

Every budget is spent **before** the call runs, so a refused call still costs
its unit and a script cannot spin the refusal path for free. Connections share
the 20-call budget rather than getting a pool of their own because `http.*` set
that rule and one number is easier to keep in your head: a script that spends 18
calls on HTTP has two connections left. That is the intended trade.

## Full examples

Every example below is copy-pasteable and ends in a `return`. The ones marked
"(tested)" are executed by
[`docs_examples_test.go`](https://github.com/fclairamb/solidping/blob/main/server/internal/checkers/checkjs/docs_examples_test.go)
against a real `httptest` fixture on every build — the comment right before
the fence is what tells the test which fixture and assertion to run.

### JSON API health (tested)

<!-- test: json-health -->
```js
var resp = http.get(env.BASE_URL + "/health");
if (resp.error) {
  return { status: "error", output: { error: resp.error } };
}
var data = JSON.parse(resp.body);
if (resp.statusCode !== 200 || data.status !== "ok") {
  return { status: "down", output: { statusCode: resp.statusCode, body: resp.body } };
}
return { status: "up", metrics: { latencyMs: data.latencyMs } };
```

### Bearer-token login, then an authenticated call (tested)

The chaining example: log in with a JSON `POST`, parse the token out of the
response, then send it on the next call. The password comes from `secrets`,
the base URL and username from `env` — never the other way around.

<!-- test: bearer-chain -->
```js
var login = http.post(env.BASE_URL + "/login", {
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({ username: env.USERNAME, password: secrets.PASSWORD }),
});
if (login.error || login.statusCode !== 200) {
  return { status: "down", output: { step: "login", statusCode: login.statusCode, error: login.error } };
}
var token = JSON.parse(login.body).token;
var me = http.get(env.BASE_URL + "/me", {
  headers: { "Authorization": "Bearer " + token },
});
return { status: me.statusCode === 200 ? "up" : "down", output: { step: "me", statusCode: me.statusCode } };
```

A test with the wrong password proves this example actually checks the
credential: it returns `"down"`, not `"up"` — the token step never sees a
valid token to chain with.

### Form login with a session (tested)

The cookie-jar example: get the login page (which sets a CSRF cookie), post
the form with `followRedirects: false` so the `302` itself is observable, and
assert on it directly.

<!-- test: form-session -->
```js
var s = http.session();
s.get(env.BASE_URL + "/form/login"); // captures the csrf cookie
var login = s.post(env.BASE_URL + "/form/login", {
  headers: { "Content-Type": "application/x-www-form-urlencoded" },
  body: "username=" + env.USERNAME + "&password=" + encodeURIComponent(secrets.PASSWORD),
  followRedirects: false,
});
return {
  status: login.statusCode === 302 && login.headers.Location === "/dashboard" ? "up" : "down",
  output: { statusCode: login.statusCode },
};
```

**The manual variant, without a jar** — read `Set-Cookie` off the first
response and send it back by hand on the second. It reaches the same result
here, but has a real caveat: a cookie set *during* a redirect chain (a hop the
manual version never sees the headers of) is lost this way. Prefer
`http.session()` whenever the login page itself redirects.

<!-- test: form-manual-cookie -->
```js
var page = http.get(env.BASE_URL + "/form/login");
var cookie = page.headers["Set-Cookie"];
var login = http.post(env.BASE_URL + "/form/login", {
  headers: {
    "Content-Type": "application/x-www-form-urlencoded",
    "Cookie": cookie.split(";")[0],
  },
  body: "username=" + env.USERNAME + "&password=" + encodeURIComponent(secrets.PASSWORD),
  followRedirects: false,
});
return { status: login.statusCode === 302 ? "up" : "down" };
```

### Basic auth via header (tested)

`base64.encode` builds the header by hand — useful when you need to add or
compute other headers alongside it. For the common case of "just send Basic
auth on this one call", `solidping.http({ url, basicAuth: "user:pass" })` is
simpler: the credential is stored encrypted the same way an `http` check's
own Basic Auth field is, and you do not have to think about encoding at all.

<!-- test: basic-auth -->
```js
var creds = base64.encode(env.USERNAME + ":" + secrets.PASSWORD);
var resp = http.get(env.BASE_URL + "/basic", {
  headers: { "Authorization": "Basic " + creds },
});
return { status: resp.statusCode === 200 ? "up" : "down" };
```

### Multi-step workflow with cleanup (tested)

Create a resource, read it back, and always attempt the delete — even if the
read failed — so a failing check does not leave litter behind on the target.

<!-- test: create-read-delete -->
```js
var steps = [];
var created = http.post(env.BASE_URL + "/items", {
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({ name: "probe" }),
});
steps.push("create");
if (created.statusCode !== 201) {
  return { status: "down", output: { step: "create", statusCode: created.statusCode, steps: steps } };
}
var id = JSON.parse(created.body).id;
var status = "up";
var error;
var readBack = http.get(env.BASE_URL + "/items/" + id);
steps.push("read");
if (readBack.statusCode !== 200) {
  status = "down";
  error = "unexpected read status " + readBack.statusCode;
}
// Cleanup is always attempted, whether or not the read above succeeded.
http.delete(env.BASE_URL + "/items/" + id);
steps.push("delete");
return { status: status, output: { steps: steps, error: error } };
```

### Aggregating sub-checks (tested)

Fold several `solidping.*` calls into one result: the worst status wins, and
each call's own duration is reported as a metric.

<!-- test: aggregate-subchecks -->
```js
var checks = [
  solidping.http({ url: env.BASE_URL + "/agg/ok" }),
  solidping.http({ url: env.BASE_URL + "/agg/ok" }),
  solidping.http({ url: env.BASE_URL + "/agg/bad" }),
];
var worst = "up";
var metrics = {};
checks.forEach(function (result, index) {
  metrics["check" + index + "Duration"] = result.duration;
  if (result.status !== "up") {
    worst = "down";
  }
});
return { status: worst, metrics: metrics };
```

### Form login through a real browser (tested)

The workflow a page-load check cannot express: fill the real form, wait for the
app to render, then hand the browser's session to a plain HTTP call. Needs a
worker with a browser — see [`browser`](#browser) — and runs at the 1m floor.

<!-- test: browser-login -->
```js
var page = browser.open();
var nav = page.goto(env.BASE_URL + "/login");
if (!nav.ok) return { status: "down", output: { step: "load", error: nav.error } };
page.fill("#email", env.USERNAME);
page.fill("#password", secrets.PASSWORD);
page.click("button[type=submit]");
var dash = page.waitFor("[data-testid=dashboard]", { timeout: "10s" });
if (!dash.ok) {
  page.screenshot();
  return { status: "down", output: { step: "login", url: page.url(), error: dash.error } };
}
// Hand the browser's session to a plain HTTP call: cookies go on the header,
// http.session()'s jar is filled by the target only.
var cookie = page.cookies().map(function (c) { return c.name + "=" + c.value; }).join("; ");
var me = http.get(env.BASE_URL + "/api/me", { headers: { Cookie: cookie } });
if (me.error || me.statusCode !== 200) {
  return { status: "down", output: { step: "api", statusCode: me.statusCode, error: me.error } };
}
return { status: "up", metrics: { loginMs: nav.duration + dash.duration } };
```

The screenshot on the failing branch is kept only because that branch returns
`down`; the same call on the success path would be dropped. This example is
also shipped as the **JS: Browser Form Login** sample in the dashboard's sample
picker, from the same source, so the two cannot drift.

### Redis: AUTH, then PING (tested)

The conversation a `tcp` check cannot hold: the second message only makes sense
if the first one succeeded, and the reply to the first has to be *read* to know
that. `env.REDIS_ADDR` is `host:port`; the password comes from `secrets`.

<!-- test: tcp-redis-ping -->
```js
var c = tcp.connect(env.REDIS_ADDR, { timeout: "3s" });
if (!c.ok) {
  return { status: "down", output: { step: "connect", error: c.error, class: c.class } };
}
c.write("AUTH " + secrets.REDIS_PASSWORD + "\r\n");
var auth = c.read({ until: "\r\n", timeout: "2s" });
if (auth.data.charAt(0) !== "+") {
  c.close();
  return { status: "down", output: { step: "auth", reply: auth.data } };
}
c.write("PING\r\n");
var pong = c.read({ until: "\r\n", timeout: "2s" });
c.close();
return {
  status: pong.data === "+PONG\r\n" ? "up" : "down",
  metrics: { connectMs: c.connectDuration, pingMs: pong.duration },
  output: { reply: pong.data },
};
```

A test with the wrong password proves this example actually checks the
credential: RESP answers `-ERR …`, the `+` test fails, and the check reports
`down` at the `auth` step instead of sailing on to `PING`. This example is also
shipped as the **JS: Redis AUTH + PING** sample in the dashboard's sample
picker, from the same source, so the two cannot drift.

### A binary UDP query (tested)

Bytes in and bytes out as hex, because a JS string cannot carry them. One
datagram per call — there is no `until` over UDP.

<!-- test: udp-query -->
```js
var u = udp.open(env.UDP_ADDR, { timeout: "2s" });
if (!u.ok) {
  return { status: "down", output: { step: "open", error: u.error } };
}
u.send("5350 0100 0001", { encoding: "hex" });
var reply = u.receive({ timeout: "2s", encoding: "hex" });
u.close();
if (!reply.ok) {
  return { status: "down", output: { step: "receive", error: reply.error, timedOut: reply.timedOut } };
}
return {
  status: reply.data.indexOf("5350") === 0 ? "up" : "down",
  metrics: { replyBytes: reply.bytes, replyMs: reply.duration },
  output: { reply: reply.data },
};
```

### A WebSocket feed: subscribe, then wait for the event that matters (tested)

The frames before the one you care about are the script's problem to skip, which
is exactly why a handle beats a one-shot check here: a `websocket` check
matching a pattern would fire on the first frame that happened to contain it.

<!-- test: websocket-subscribe -->
```js
var ws = websocket.connect(env.FEED_URL, { timeout: "5s" });
if (!ws.ok) {
  return { status: "down", output: { step: "handshake", error: ws.error, statusCode: ws.statusCode } };
}
ws.send(JSON.stringify({ op: "subscribe", channel: "heartbeat" }));
for (var i = 0; i < 5; i++) {
  var frame = ws.receive({ timeout: "5s" });
  if (!frame.ok) {
    ws.close();
    return { status: "down", output: { step: "receive", error: frame.error, skipped: i } };
  }
  var event = JSON.parse(frame.data);
  if (event.type === "heartbeat") {
    ws.close({ code: 1000 });
    return { status: "up", metrics: { seq: event.seq, waitMs: frame.duration }, output: { skipped: i } };
  }
}
ws.close();
return { status: "down", output: { step: "subscribe", error: "no heartbeat frame in 5 frames" } };
```

### Time-conditional check

Skip the probe outside business hours without calling anything — note that
the check still **runs** on its normal period; this only decides whether that
run does any work.

```js
var hour = new Date().getUTCHours();
if (hour < 6 || hour >= 22) {
  return { status: "up", output: { skipped: "outside business hours" } };
}
var resp = http.get(env.BASE_URL + "/health");
return { status: resp.statusCode === 200 ? "up" : "down" };
```

This example is compiled by the same test as every other on this page, but
is not executed against a fixture: its branch depends on wall-clock time, not
on anything an `httptest` server can control.

### Config-as-code

The bearer-token example above, as a tracked
[config-as-code](./config-as-code.md) document — the credential is a
`${param:}` reference, so nothing sensitive is in git:

```yaml
type: js
name: Login smoke test
period: 5m
config:
  tunnelCheckUid: 8f2c1a6e-3b4d-4e5f-9a1b-2c3d4e5f6a7b # the bastion's ssh check uid; omit if reachable directly
  script: |
    var login = http.post(env.BASE_URL + "/login", {
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ username: env.USERNAME, password: secrets.PASSWORD }),
    });
    if (login.error || login.statusCode !== 200) {
      return { status: "down", output: { step: "login" } };
    }
    var token = JSON.parse(login.body).token;
    var me = http.get(env.BASE_URL + "/me", {
      headers: { "Authorization": "Bearer " + token },
    });
    return { status: me.statusCode === 200 ? "up" : "down" };
  env:
    BASE_URL: https://api.acme.com
    USERNAME: probe
  secrets:
    PASSWORD: "${param:acme-probe-password}"
```

## Troubleshooting

**`script must return a result object`** — the script did not `return`
anything (or returned `null`/`undefined`). Every script must end with a
`return { status: ... }`, including every early-exit branch.

**`sub-check limit of 20 exceeded`** — the script called `http.*` and
`solidping.*` more than 20 times combined in one execution. The 21st call
returns this error instead of running; check for an unbounded loop calling
either.

**Status `timeout` with no obvious reason** — the script did not return
before the check's `timeout` elapsed. A `sleep(ms)` call that's too long, a
slow-to-answer `http.*` call with no `timeout` option of its own, or a script
stuck in a loop are the usual causes; note this is the engine reporting the
script, not the script reporting itself — see [Result contract](#result-contract).

**`env.X` (or `secrets.X`) is `undefined`** — the name is not on the check:
either it was never added (check the dashboard's `env`/`secrets` editors, or
the API/`sp`/config-as-code value, for a typo), or — for `secrets` — the
editor was left untouched on a save and nothing was ever entered for that key
in the first place. Remember that a saved `secrets` value never displays
again, so "is it actually set?" is answered by the row existing with the
"encrypted" placeholder, not by its value.

**`check type "browser" is disabled on this server`** — an operator turned the
`browser` check type off (`checkers.enabled` / `checkers.disabled`), and a
script does not get it back. The same message appears for
`solidping.check("browser", …)`.

**`Chrome/Chromium not found` / `cannot reach the remote Chrome (CDP)
endpoint`** — this worker has no browser. The check reports `error`, never
`down`: your target was never contacted. Give the region a headless-shell
sidecar (`SP_CHECKERS_BROWSER_CDP_URL`) or pin the check to a region whose
capability list shows `browser` — `js` checks are not routed to
browser-capable regions automatically.

**`a page is already open`** — a script may open **one** page per execution,
and closing it does not buy another. Drive the page you have, or split the
workflow across two checks.

**`browser action limit of 100 exceeded`** — the 101st `page.*` call returns
this instead of running. It is a separate budget from the 20-call
`http.*`/`solidping.*` one; a loop over a long list of selectors is the usual
cause.

**`period for js checks must be at least 60s: scripts that open a browser have
the browser check's 1m floor`** — the script contains `browser.open(`, so it
inherits the [`browser` floor](#browser). Raise the period to `1m`, or stop
opening a browser.

**`timed out waiting for a free browser slot`** — at most four browser
executions run at a time per worker, and a script holds its slot from `open()`
to `close()`. Space browser-using checks out, or add workers.

**Where did my `console.log` go?** — every `console.log/warn/error/info` call
lands in `output.console` on the result, whether or not the script's own
`return` includes an `output` field of its own.
