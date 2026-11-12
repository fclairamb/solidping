---
model: sonnet
effort: medium
---

# HTTP checks resend their secret headers to the host a redirect points to

## Problem

Found by the security scan of 2026-11-12 (finding AK6).

**Severity: Low.** Nobody has to act: it happens whenever a monitored URL answers with a redirect to another host. The leak goes to that host: a CDN, an object store, an SSO page, or a host that took over a stale DNS name. The defaults make it the common case:
- `followRedirects` defaults to on;
- `redirectHostPolicy` defaults to `any` (`server/internal/checkers/checkhttp/config/config.go:686-690`).

How it happens:
- The HTTP checker sets every `secretHeaders` entry on the request with `req.Header.Set` (`server/internal/checkers/checkhttp/checker.go:368-371`).
- On a redirect, Go's `net/http` client copies the original request's headers to the next hop. For a different host it only strips `Authorization`, `WWW-Authenticate` and `Cookie`. Any other header goes along, so `X-Api-Key`, `X-Auth-Token` and the like are resent to the redirect's host.
- `CheckRedirect` (`checker.go:385-419`) only enforces the egress guard and the optional same-host policy. It never strips the secret headers.

The values are exactly what the user marked as secret: encrypted at rest, redacted from exports, and for private agents browser-sealed. Basic auth (`SetBasicAuth`, `checker.go:356`) is an `Authorization` header and is already stripped.

## Proposal

1. In `CheckRedirect` (`checker.go:385`), when the next hop's host differs from the **original** request's host (compare `Hostname()` and the effective port), delete every key of `cfg.SecretHeaders` from `req.Header`, case-insensitively.
   - Same-host hops keep them, including a scheme upgrade from http to https on the same host.
2. Record the dropped secret header names, but never their values, in the redirect chain output, so a check that needs the key on the final host fails visibly instead of silently.
3. Check whether the JS check's `http` binding has the same behaviour with headers built from `secrets.*`. If it does, apply the same rule there (To verify: `server/internal/checkers/checkjs`, the redirect handling of its HTTP client).
4. Docs: one line in the HTTP check page saying that secret headers are only sent to the URL's host.

## Tests

- `server/internal/checkers/checkhttp/checker_test.go` (or `redirect_test.go`), with two `httptest` servers A and B, where A redirects to B:
  - **Regression:** a check on A with `secretHeaders: {"X-Api-Key": "k"}` reaches B without `X-Api-Key` (fails on main today, where B receives it);
  - a public header `X-Trace: 1` still reaches B (positive control: only secrets are dropped);
  - a redirect from A to A (another path) keeps `X-Api-Key` (positive control: same-host hops are unchanged).
