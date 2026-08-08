---
model: sonnet
effort: medium
---

# HTTP checks cannot skip TLS verification or stop following redirects

## Problem

The HTTP checker always verifies TLS certificates and always follows redirects
(up to 10). Both behaviors are hardcoded:

- The client is built with no `Transport`/`TLSClientConfig`, so certificate
  verification is always on
  ([checker.go:251](server/internal/checkers/checkhttp/checker.go:251)).
- `CheckRedirect` follows up to `maxRedirects = 10` unconditionally
  ([checker.go:61](server/internal/checkers/checkhttp/checker.go:61),
  [checker.go:252](server/internal/checkers/checkhttp/checker.go:252)).

This blocks two legitimate use cases:

1. **Monitoring endpoints with self-signed or internal-CA certificates**
   (staging environments, appliances, internal services) — the check can never
   go green.
2. **Asserting on the redirect itself** — e.g. verifying that `http://example.com`
   returns a `301` to HTTPS. Today the client follows the hop, so the user can
   only assert on the final destination.

The gap is already visible to users: both third-party importers parse these
exact options and emit "not supported" warnings —
[betterstack.go:364-371](server/internal/handlers/checks/importers/betterstack.go:364)
(`verify_ssl`, `follow_redirects`) and
[gatus.go:216](server/internal/handlers/checks/importers/gatus.go:216)
(`client.insecure`).

## Proposal

Add two optional booleans to `HTTPConfig`
([config.go:61](server/internal/checkers/checkhttp/config.go:61)), both
defaulting to today's behavior so existing checks are untouched:

- **`verifySsl`** (default `true`) — when `false`, the request is executed with
  `InsecureSkipVerify: true` in the transport's `TLSClientConfig`.
- **`followRedirects`** (default `true`) — when `false`, `CheckRedirect`
  returns `http.ErrUseLastResponse` immediately, so the check receives the
  first response as-is and status/body/header assertions run against it
  (e.g. `expectedStatus: 301` + `headersPattern.Location`).

### Backend

- Canonical keys are camelCase (`verifySsl`, `followRedirects`) per the
  existing convention; accept `verify_ssl` / `follow_redirects` as read
  fallbacks via `resolveKey`
  ([config.go:110](server/internal/checkers/checkhttp/config.go:110)) — that
  also matches what importer payloads and hand-written manifests use.
- Because both default to `true`, parse them as presence-aware (`*bool` or
  explicit key check) — a plain `bool` zero value would flip the default.
  `GetConfig` should emit the key only when set to the non-default `false`,
  matching the omit-empty style of the other fields.
- The transport construction must compose with the tunnel dialer path
  ([checker.go:262-271](server/internal/checkers/checkhttp/checker.go:262)):
  build a single `http.Transport` when either a tunnel dialer or
  `verifySsl: false` is in play, setting `DialContext` and/or
  `TLSClientConfig` on it. Keep `client.Transport` nil when neither applies.
- When TLS verification is skipped, include a marker in the result output
  (e.g. `tls_verify_skipped: true`) so it's visible in result details.

### Importers

- `betterstack.go`: map `verify_ssl` / `follow_redirects` into the config
  instead of warning; drop the two warning branches.
- `gatus.go`: map `client.insecure: true` → `verifySsl: false` instead of
  warning; update `gatus_test.go:157` accordingly.
- Check the uptime-kuma importer for equivalent flags while there
  (`ignoreTls` / `maxredirects`) and map them too if present.

### Frontend (dash0)

- Add the two toggles to the HTTP check form, in the advanced/options section,
  following the design reference
  ([design-reference.tsx](web/dash0/src/routes/orgs/$org/design-reference.tsx)).
  Default-on switches; `verifySsl` off should carry a short warning hint
  ("certificate errors will be ignored").

### Tests & docs

- Backend table tests: `httptest.NewTLSServer` (self-signed) fails with the
  default and succeeds with `verifySsl: false` (positive + negative control);
  a redirecting test server asserts `301` is surfaced with
  `followRedirects: false` and followed by default.
- Importer tests updated for the new mappings.
- Update the HTTP check samples ([samples.go](server/internal/checkers/checkhttp/samples.go))
  and the docs/OpenAPI description of the HTTP check config.

### Out of scope

Other TLS-capable checkers (websocket, grpc, smtp/imap/pop3) have their own
verification behavior — leave them unchanged; a follow-up spec can generalize
if needed.
