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
page is exactly that kind of text.

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
| Sub-checks (`http.*` + `solidping.*` combined) | 20 per execution |
| Console output | 16 KB |
| HTTP response body | 1 MB |
| `env` / `secrets` entries | 50 each |
| Default / maximum timeout | 30s |
| Cookie jar (per session) | 100 cookies, 4 KiB each |

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

**Where did my `console.log` go?** — every `console.log/warn/error/info` call
lands in `output.console` on the result, whether or not the script's own
`return` includes an `output` field of its own.
