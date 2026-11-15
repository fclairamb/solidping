---
model: opus
effort: medium
---

# Prometheus, WebSocket and RabbitMQ checks follow redirects with Go's default header policy

## Problem

Found by the security scan of 2026-11-15 (finding AN2). Severity: Low. Nobody has to act: it needs a check carrying credentials, and a target that redirects (a third-party endpoint, a compromised host, or a MITM on plain http).

The HTTP and JS checks route every redirect hop through `checkerdef.SameRedirectOrigin` / `ApplyRedirectHop` (`server/internal/checkers/checkerdef/redirectorigin.go`). Three other checkers send credentials and follow redirects with Go's default policy:

- **Prometheus.** `newClient` sets no `CheckRedirect` (`server/internal/checkers/checkprometheus/checker.go:128-134`). `doGet` sets every configured header (`:150-152`), and the config documents those headers as the place for bearer/basic auth (`checkprometheus/config/config.go:153-156`). URL and headers can hold `${param:}` values.
- **WebSocket.** The handshake sets every configured header (`server/internal/checkers/checkwebsocket/checker.go:118-124`). coder/websocket wraps `CheckRedirect` only to rewrite `ws`/`wss` and otherwise follows every redirect (`coder/websocket dial.go`). With no previous `CheckRedirect`, there is not even Go's 10-hop limit, only the check timeout.
- **RabbitMQ.** The management API calls use `SetBasicAuth` on `checkerdef.GuardedHTTPClient`, which has no `CheckRedirect` (`server/internal/checkers/checkrabbitmq/checker.go:405-409`, `:676-680`, `checkerdef/outbound.go:119-125`).

Go's default policy (`net/http/client.go`, `makeHeadersCopier` and `shouldCopyHeaderOnRedirect`):

- keeps `Authorization` and `Cookie` when the new host is the same hostname or a subdomain, whatever the scheme. So an `https` → `http` hop on the same host resends Basic auth or a bearer token in cleartext (the AL2 case);
- forwards every other header (`X-Api-Key`, `X-Scope-OrgID`, `PRIVATE-TOKEN`…) to any host;
- sets `Referer` to the previous URL, query string included (the AM2 case);
- replays a 307/308 body (not used by these three checkers today).

The egress guard still applies on every hop, so this is a credential leak, not an SSRF.

## Proposal

1. Add one shared policy in `checkerdef`, for example `checkerdef.CredentialRedirectPolicy(onDrop func(names []string)) func(*http.Request, []*http.Request) error`. It:
   - caps the chain (reuse the HTTP checker's `maxVia`);
   - calls `ApplyRedirectHop(via[0].URL, req)`;
   - on a cross-origin hop, deletes `Authorization`, `Proxy-Authorization`, `Cookie` and every header the check configured (the caller passes their names), and reports the dropped names;
   - stops with `http.ErrUseLastResponse` when `WithholdBody` is set.
2. Use it in `checkprometheus.newClient`, in `checkwebsocket` (always pass an `HTTPClient` whose `CheckRedirect` is this policy, so coder/websocket calls it after its scheme rewrite), and in both RabbitMQ management calls. For RabbitMQ, consider not following redirects at all (`ErrUseLastResponse`): the URL is built from the host, so a redirect is never expected.
3. Report the dropped names in the output as `redirect_dropped_secret_headers`, as the HTTP check does.
4. Mention the rule in the Prometheus, WebSocket and RabbitMQ sections of `web/docs/docs/features/check-types.md`.

## Tests

- `checkprometheus/checker_test.go`: target A answers 302 to a second server B. The check has `headers: {X-Api-Key: k, Authorization: Bearer t}`. Assert B gets neither header and no `Referer`. A second case: a same-hostname `https` → `http` hop does not carry `Authorization`. These fail before the fix. Positive control: a same-origin redirect still carries both headers, and the check reads the metric.
- `checkwebsocket/checker_test.go`: a handshake redirected cross-origin does not carry the configured headers. Positive control: a same-origin redirect does. The chain is capped: a redirect loop ends with an error before the timeout.
- `checkrabbitmq`: a management endpoint redirecting to another origin (or to `http` on the same host) never receives the Basic auth header.
- `checkerdef/redirectorigin_test.go`: unit tests of the shared policy (cross-origin drops the named headers, same-origin keeps them, body withheld, chain cap).
