---
model: opus
effort: medium
---

# JS checks forward headers holding an encoded secret to the host a redirect points to

## Problem

Found by the security scan of 2026-11-13 (finding AL3). Severity: low. Who can trigger it:
nobody has to act. It happens when a JS check sends a header built from an encoded
`secrets.*` value (base64, hex, a percent-encoded value, or a prefix scheme other than
`Authorization`) to a target that redirects to another host: a CDN, an object store, SSO, or
a stale DNS name.

This is a residual of the AK6 fix (#851, spec `2026-11-12-06`). On a cross-host hop, the JS
redirect policy strips a header only when its value contains a raw secret value as a substring:
- `stripSecretValuedHeaders`, `server/internal/checkers/checkjs/checker.go:1289-1299`;
- `containsSecret`, `:1301-1309`;
- called from `redirectPolicy` at `:1233`.

A header carrying an encoded secret contains no raw substring, so it survives and reaches the
other host. checkhttp is not affected: it strips its configured `SecretHeaders` by name.
net/http strips only `Authorization`, `Cookie` and `WWW-Authenticate` cross-host.

The secrets are browser-sealed and bound to the check's `script` and `env`. They were meant
for that target only, which is the same impact as AK6.

## Proposal

1. In `redirectPolicy`, on a hop that is not same-origin (`:1233`) and when the check has
   secrets (`len(secrets) > 0`), strip every header the script set itself. Keep only a short
   list of headers that never carry credentials: `Accept`, `Accept-Encoding`,
   `Accept-Language`, `Content-Type`, `User-Agent`, `Cache-Control`, plus the ones net/http
   adds itself. Don't try to recognise encodings: a list of headers to keep cannot be
   bypassed, and a value match can.
2. Keep the current value-substring check as well. It also covers a header in the keep list
   that would carry a raw secret.
3. Report the dropped header names (never their values) in the output, like checkhttp's
   `redirect_dropped_secret_headers`, so a check that breaks because of this can be diagnosed.
4. Document it in `web/docs/docs/features/javascript-checks.md`, next to the redirect options:
   on a cross-host redirect, a check with secrets sends only these generic headers.

## Tests

- `checkers/checkjs` (new `redirect_secrets_test.go`), with two `httptest` servers on different hosts and a script that sends one header holding a base64-encoded secret and one holding a hex-encoded secret:
  - the second host receives neither header, and the output lists both names (regression: fails before the fix);
  - same-host redirect: both headers are kept (positive control);
  - check without secrets: the custom headers follow the cross-host hop unchanged (positive control for the `len(secrets) > 0` scope);
  - `Accept` and `User-Agent` survive the cross-host hop.
