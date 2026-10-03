---
model: opus
effort: high
---

# HTTP checks can't verify that a server speaks HTTP/2 or HTTP/3

## Problem
Every HTTP check runs on HTTP/1.1. Since spec 2026-09-28-04, the shared transport and every private transport set `TLSNextProto: noHTTP2()` (`server/internal/checkers/checkerdef/httptransport.go:78`, `:108`, `:127`). That was deliberate: a timed-out HTTP/2 probe left a stuck stream on a pooled connection and the next 16 probes failed on it.

As a result, a user can't monitor that their server still serves HTTP/2 or HTTP/3. A CDN, ingress or proxy change that silently drops h2 (ALPN misconfiguration) or h3 goes unnoticed. The negotiated protocol is only visible in the status line captured on failure (`server/internal/checkers/checkhttp/capture.go:187`). The idea is listed in `specs/ideas/2025-12-16-things-to-test.md:12`.

## Proposal
1. **Config.** Add `httpVersion` to the HTTP check config (`server/internal/checkers/checkhttp/config/config.go`, parsed like `followRedirects` at `:382`). Values: `"1.1"` (default, omitted when default), `"2"`, `"3"`. Reject any other value in validation (`server/internal/checkers/checkhttp/config/validate.go:18`). Reject `"3"` on a tunneled check: the SSH tunnel only carries TCP. Use a `VALIDATION_ERROR` with a clear message.
2. **Transport selection.** Extend `HTTPTransportFor` / `buildHTTPTransport` (`httptransport.go:50`, `:112`) with the requested version. `"1.1"` keeps today's behaviour exactly, including the shared pooled `checkTransport`.
3. **HTTP/2.** Build a fresh `http.Transport` per probe with `Protocols` set to HTTP/2 only (`SetHTTP2(true)`, plus `SetUnencryptedHTTP2(true)` for `http://` URLs, i.e. h2c with prior knowledge). Set `DisableKeepAlives: true` and close idle connections after the probe. The transport never outlives the probe, so the stranded-stream bug from 2026-09-28-04 can't return. Reuse the same `DialContext` (tunnel dialer, `guardedFamilyDialContext`) and `TLSClientConfig` (`verifySsl: false`) logic as the HTTP/1.1 branch. Factor that logic out instead of duplicating it. Update the doc comment on `checkTransport` (`httptransport.go:54-77`) to say HTTP/2 is opt-in and unpooled.
4. **HTTP/3.** Add `github.com/quic-go/quic-go` and use `http3.Transport`, built per probe and closed after it. Its `Dial` hook must apply the same rules as the TCP path:
   - honour the pinned IP family;
   - under an enforcing egress guard, resolve once, refuse non-public addresses, and dial the pinned IP (never the name again).

   Reuse the resolve/select/check logic behind `guardedFamilyDialContext` (`httptransport.go:157`) and `egress.Guard` instead of reimplementing it. Pass `InsecureSkipVerify` through `TLSClientConfig` when `verifySsl: false`. Do not fall back to TCP: an unreachable UDP path is a failure.
5. **Protocol assertion.** In `server/internal/checkers/checkhttp/checker.go`, after `client.Do` (around `:314`), compare `resp.ProtoMajor` with the requested version. On a mismatch the check goes down with `expected HTTP/2, got HTTP/1.1` (or similar). With HTTP/2 forced via `Protocols` the client may instead fail at the handshake, so map that error to the same clear message.
6. **Output.** Record the negotiated protocol (`resp.Proto`) on every result, up and down, not only in the failure capture. Also record the `Alt-Svc` response header when present, so users can see whether h3 is advertised.
7. **Dash0.** Add a "HTTP version" select (1.1 / 2 / 3) to `web/dash0/src/components/checks/form/types/http.tsx`. Disable or hide `3` when the check is tunneled. Add labels to `web/dash0/src/locales/{en,fr,de,es}/checks.json`.
8. **Docs and API.** Document `httpVersion` and the tunnel restriction in the HTTP check docs page and in `server/internal/app/openapi/openapi.yaml`. Add a `CHANGELOG.md` entry.

## Tests
- `server/internal/checkers/checkhttp/config/config_test.go`: `httpVersion` parses `"1.1"`, `"2"`, `"3"`, defaults to `"1.1"`, and is omitted on serialization at the default. Invalid values (`"4"`, `"http2"`) and `"3"` on a tunneled check are rejected.
- `server/internal/checkers/checkerdef/httptransport_test.go`: `"1.1"` still returns the shared `checkTransport` in the common case. `"2"` returns a fresh, non-pooled transport each call. The egress guard still refuses a private address for `"2"` and `"3"`.
- `server/internal/checkers/checkhttp/checker_test.go`:
  - `"2"` against an h2 TLS `httptest` server is up and reports `HTTP/2.0`;
  - `"2"` against an h2c `http://` server is up;
  - `"2"` against an HTTP/1.1-only server is down with the mismatch error;
  - `"3"` against a local `http3.Server` is up and reports `HTTP/3.0`;
  - `"3"` against a target with no UDP listener is down (no silent fallback);
  - every result (up and down) carries the negotiated protocol, and `Alt-Svc` when the server sends it.
- Regression: rerun the test from 2026-09-28-04 (`TestStrandedConnectionDoesNotSurviveTheProbeThatHitIt`) with `httpVersion: "2"`. A timed-out probe must not make the next probe fail.
- `web/dash0/src/components/checks/form/types/http.test.ts`: the field round-trips, and `3` is unavailable on a tunneled check.

## To verify
- How the HTTP check config knows it is tunneled at validation time (field name or context), so the `"3"` rejection can live in `validate.go`.
- The docs page that documents HTTP check options (likely under `web/docs/docs/`). `followRedirects` only shows up in `features/javascript-checks.md` and the changelog, so the HTTP options page may need creating.
- Whether `egress.Guard` exposes its resolve-and-check step separately from `DialContextWith`, so the QUIC `Dial` hook can reuse it without a TCP dial.
- Whether HTTP checks run on private-location workers built from a separate binary that would also need the quic-go dependency.
