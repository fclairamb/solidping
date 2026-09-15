---
model: sonnet
effort: high
---

# A JavaScript check cannot run through an SSH tunnel, even though every sub-check it runs already could

## Problem

An `http`, `tcp` or `websocket` check can carry a `tunnelCheckUid` and be
dialed through an SSH check's bastion, which is how a private API behind a
bastion gets monitored. A `js` check cannot, for three linked reasons:

1. The type's metadata does not declare `SupportsTunnel`
   ([`types.go:359`](../../server/internal/checkers/checkerdef/types.go#L359)).
   The API therefore rejects `tunnelCheckUid` on a `js` check with
   "check type \"js\" cannot run through an SSH tunnel"
   ([`handlers/checks/tunnel.go:66`](../../server/internal/handlers/checks/tunnel.go#L66)),
   the dashboard hides the tunnel selector (it is gated on the served flag,
   [`check-form.tsx:425`](../../web/dash0/src/components/shared/check-form.tsx#L425)),
   and the worker never establishes a tunnel for it
   ([`worker.go:1075`](../../server/internal/checkworker/worker.go#L1075)).
2. Even with the flag flipped, the JS `http.*` helper would bypass the
   tunnel: it builds a bare `http.Client` with no transport
   ([`checker.go:735`](../../server/internal/checkers/checkjs/checker.go#L735))
   and never reads `checkerdef.TunnelDialerFrom(ctx)`. The `http` checker
   and `checkprometheus` both go through `checkerdef.BuildHTTPTransport`
   ([`httptransport.go:32`](../../server/internal/checkers/checkerdef/httptransport.go#L32))
   for exactly this; the JS helper predates it and was never converted.
3. Sub-checks are the opposite: `solidping.http(...)`, `solidping.tcp(...)`
   and the rest execute on `r.execCtx`
   ([`checker.go:504`](../../server/internal/checkers/checkjs/checker.go#L504)),
   so the moment a dialer is on that context they *would* be tunneled, with
   no code change. Which also means a sub-check of a type that cannot be
   tunneled (`udp`, `icmp`, `dns`) would silently probe from the worker's
   own network while the check claims to be behind a bastion.

Spec 2026-07-18-04 listed `checkjs` under "own network stack / out of scope
for now, own spec later if asked for". This is that spec. The trigger is
spec 2026-09-15-06, which gives scripts raw TCP and WebSocket handles that
honor the tunnel dialer; without this spec that branch is unreachable.

## Proposal

### 1. Declare the capability

Set `SupportsTunnel: true` on the `CheckTypeJS` row of the type metadata.
That single flag lights up every existing seam with no further code:

- API validation accepts `tunnelCheckUid` on `js`, with the region rules of
  spec 2026-07-18-07 applying unchanged.
- The dashboard shows the tunnel selector on the `js` form.
- The worker runs `setupTunnel`, attaches the dialer with
  `checkerdef.WithTunnelDialer`, and on tunnel failure saves the
  "tunnel failed" result without running the script.
- Config-as-code documents accept the key.

`SupportsIPVersion` stays off. A tunneled check rejects `ipVersion` anyway,
and the untunneled `js` case has no address to pin at the check level; the
socket handles of spec 2026-09-15-06 take it per call.

### 2. `http.*` and `http.session()` honor the dialer

In `httpRequest`, set `client.Transport =
checkerdef.BuildHTTPTransport(checkerdef.TunnelDialerFrom(r.execCtx), false,
checkerdef.IPVersionFrom(r.execCtx))`. The builder returns `nil` when
nothing applies, so the untunneled path keeps `http.DefaultTransport` and
its connection pool byte for byte. When a dialer is present, the transport
hands the raw `host:port` to it, the bastion resolves the name, and the
response object gains `tunneled: true`. The field is present only when
true, so an untunneled response's shape does not change.

The `Host` header, redirects, the cookie jar and every option keep their
current behavior. A redirect to another host is dialed through the same
tunnel, as the `http` checker does.

### 3. Sub-checks of non-tunnel types are refused, not silently local

In `jsRuntime.check`, when a dialer is on the execution context and the
requested type's metadata lacks `SupportsTunnel`, return the error result
"check type X cannot run through an SSH tunnel", the API's own sentence.
The refusal sits after the budget counter and the activation gate, in the
same position, so it spends a budget unit like every other refusal. A
tunneled script therefore cannot accidentally run a `udp`, `icmp` or `dns`
probe from the worker while believing it went through the bastion.

Types that do declare support need nothing: they already read the dialer
off the context.

### 4. `browser.open()` is refused under a tunnel

Chrome has its own network stack; `OpenBrowser` cannot be routed through a
`ContextDialer`. `openPage` throws "browser cannot run through an SSH
tunnel" when a dialer is on the context, before touching the browser
session. It is a throw, like the type gate in the same function, because it
is a configuration conflict rather than a target verdict. The one-shot
`solidping.browser(...)` is covered by §3 for the same reason, since
`browser` does not declare `SupportsTunnel`.

### 5. Socket handles

Spec 2026-09-15-06 already specifies the tunnel branch for `tcp.connect`
and `websocket.connect` and the refusal for `udp.open`. Nothing to add
here; this spec is what makes that branch reachable in production.

### 6. Documentation

- `web/docs/docs/features/ssh-tunnels.md`, "Supported check types": add
  `js`, with the three rules a script author needs: `http.*` and the
  socket handles go through the bastion; sub-checks of types the table
  does not list are refused inside a tunneled script; `browser.open()` is
  refused.
- `web/docs/docs/features/javascript-checks.md`: a short "Running through
  an SSH tunnel" subsection under Configuration, and a `tunnelCheckUid`
  line in the config-as-code example. The `http.<method>` response table
  gains the `tunneled` row.
- Changelog entry under Features: "JavaScript checks can run through an
  SSH tunnel; `http.*` and sub-checks are dialed through the bastion."

### 7. Tests

- `checkjs/tunnel_test.go`, following
  [`checkhttp/tunnel_test.go`](../../server/internal/checkers/checkhttp/tunnel_test.go):
  start `sshtunneltest`, forward `private.invalid:80` to an `httptest`
  backend, run a script doing `http.get("http://private.invalid/health")`
  with the dialer on the context. Assert `200`, `tunneled: true`, and
  `srv.Requested()` saw `private.invalid:80` verbatim. Negative control:
  the same script with no dialer fails with a resolve error. Same for
  `http.session()`, and for a redirect whose target is also behind the
  bastion.
- Sub-check inheritance: `solidping.http({ url: "http://private.invalid/" })`
  succeeds through the tunnel. Refusal: with a stub `udp` checker installed
  the way `subcheck_gate_test.go` does, `solidping.udp(...)` under a tunnel
  returns the cannot-be-tunneled error and the stub's call count stays at
  zero; positive control without the dialer reaches the stub.
- `browser.open()` under a tunnel throws and the `OpenBrowser` seam
  ([`browser.go:75`](../../server/internal/checkers/checkjs/browser.go#L75))
  is never invoked; positive control without the dialer reaches it.
- `handlers/checks/tunnel_test.go`: a `js` check with a valid `ssh`
  reference is now accepted. The existing rejection test uses `ssh` as its
  non-tunnel example, which keeps lacking the flag, so it stays as is.
- The "proves `SupportsTunnel` is real" convention
  ([`checkprometheus/checker_test.go:806`](../../server/internal/checkers/checkprometheus/checker_test.go#L806)):
  the `tunnel_test.go` above is that proof for `js`.
- Dashboard: if
  [`check-ssh-tunnel.spec.ts`](../../web/dash0/e2e/check-ssh-tunnel.spec.ts)
  enumerates the types that show the selector, add `js` to it; otherwise
  nothing, since the form is gated on the served flag.

### Decisions

1. One flag on the type metadata, no hand-maintained list anywhere.
2. `http.*` goes through `BuildHTTPTransport`, the same code path as the
   `http` and `prometheus` checkers, with `nil` preserving the pooled
   default transport when untunneled.
3. Inside a tunneled script, sub-checks of types without `SupportsTunnel`
   and `browser.open()` are refused rather than silently run from the
   worker's network. Silent local probing behind a "tunneled" label is the
   one outcome this spec exists to prevent.
4. `SupportsIPVersion` is not declared for `js`.

### Out of scope

- Tunneling the browser. Chrome would need a proxy, which is a different
  feature.
- Chaining tunnels, or any change to the region rules.
- An org-aware activation gate.

## Implementation Plan

1. Flip `SupportsTunnel` on `CheckTypeJS`; add the handler acceptance test.
2. `httpRequest`: transport via `BuildHTTPTransport`, `tunneled: true` on
   the response.
3. `jsRuntime.check`: refuse non-tunnel types under a dialer. `openPage`:
   refuse under a dialer.
4. Tests of §7.
5. Docs and changelog. `make lint`, `make test`.
