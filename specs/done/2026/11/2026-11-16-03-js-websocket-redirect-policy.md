---
model: opus
effort: medium
---

# The JS `websocket.connect` handshake follows redirects with Go's default header policy

## Problem

Found by the security scan of 2026-11-16 (finding AO3). Severity: Low. This is a gap left by the AN2 fix (`specs/done/2026/11/2026-11-15-02-redirect-policy-prometheus-websocket-rabbitmq.md`, #880). Nobody has to act: it needs a JS check that sends a secret in a WebSocket handshake header, and a handshake target that redirects to another origin. The leaked secret is the check's own org's.

- #880 gave the `websocket` check type the shared `checkerdef.CredentialRedirectPolicy` (`server/internal/checkers/checkwebsocket/checker.go:59`, `:136-140`). The JS `http` binding already strips secret-bearing headers on cross-origin hops (`server/internal/checkers/checkjs/checker.go:1181`, `redirectPolicy` `:1258-1330`, `redirectSecrets` `:1357`, `stripRedirectHeaders` `:1377`).
- The JS `websocket` binding does neither. `wsHandle.dialOptions` (`server/internal/checkers/checkjs/websocket.go:108-169`):
  - copies the script's `headers` onto the handshake verbatim (`:111-121`);
  - builds an `http.Client` with a transport only, and no `CheckRedirect` (`:160-166`). Without a tunnel, a guard or `tlsVerify: false`, it leaves `HTTPClient` nil.
- So, as `checkwebsocket/checker.go:136-139` itself documents, coder/websocket follows handshake redirects with net/http's policy and no hop limit of ours:
  - custom headers (`X-API-Key: <check.secrets.…>`) and `Referer` go to any host;
  - `Authorization`/`Cookie` survive subdomain hops and same-host `wss` → `ws` downgrades.
- The egress guard still dials every hop in SaaS mode, so this is a credential leak, not an SSRF.

## Proposal

1. In `dialOptions`, always build the `http.Client` (keep the transport and allowlist logic as is), and give it a `CheckRedirect` that applies the JS http binding's rule:
   - `checkerdef.ApplyRedirectHop` against `via[0]`;
   - on a cross-origin hop, drop `Authorization`, `Proxy-Authorization`, `Cookie` and every header whose value contains a value of `r.redirectSecrets()`;
   - refuse a scheme downgrade;
   - cap the chain at `checkerdef.DefaultMaxRedirects`.

   Reuse `CredentialRedirectPolicy`, fed with the secret-bearing header names, if that fits better than a second policy.
2. Surface the dropped headers the way the JS `http` result does (`redirect_dropped_secret_headers`, `server/internal/checkers/checkjs/checker.go:277`), so a broken check explains itself.
3. Update `web/docs/docs/features/javascript-checks.md`, which describes the redirect behaviour of the bindings.

## Tests

- `server/internal/checkers/checkjs` test with two httptest servers. A script connects to origin A with a header carrying `check.secrets.token`. A answers 302 to origin B, and B records the handshake headers. Assert that B never sees the secret header, `Authorization` or `Referer`. This fails before the fix. Positive control: a same-origin redirect keeps the header and the handshake succeeds.
- A handshake that redirects more than `DefaultMaxRedirects` times fails with a redirect-limit error instead of looping until the timeout.
- A `wss` → `ws` same-host redirect is refused.
