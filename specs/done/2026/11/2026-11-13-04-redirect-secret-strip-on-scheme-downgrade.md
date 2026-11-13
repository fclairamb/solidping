---
model: opus
effort: medium
---

# HTTP and JS checks resend secret headers in cleartext on a same-host https → http redirect

## Problem

Found by the security scan of 2026-11-13 (finding AL2). Severity: low. Who can trigger it:
nobody has to act. It needs two conditions:
- a monitored `https://` URL redirects to `http://` on the same host (a misconfiguration);
- someone can observe the network between the worker or agent and that host.

This is a residual of the AK6 fix (#851, spec `2026-11-12-06`). On a redirect, secret headers
are stripped only when the hop leaves the original request's origin, and `sameOrigin` ignores
the scheme:
- `server/internal/checkers/checkhttp/checker.go:1198-1208`, used at `:417`;
- the copy in `server/internal/checkers/checkjs/checker.go:1262-1273`, used at `:1233`.

When neither URL has an explicit port, it returns `true` whatever the scheme (`:1203-1205`).
With explicit ports, it compares only the effective port. So a hop from `https://a/` to
`http://a/` (port-less, the common case), or from `https://a:8443/` to `http://a:8443/`, counts
as same-origin. The secret headers then go out in plaintext.

net/http also keeps `Authorization` on a same-host hop, so the basic auth that checkhttp sets
(`checker.go:368`) leaks the same way.

The comment at `:1195-1197` meant to allow only an upgrade (http → https). The code allows a
downgrade too.

## Proposal

1. In both copies of `sameOrigin`, compare the scheme. Count a hop as same-origin only if both of these hold:
   - the hostname is equal (case-insensitive);
   - either the scheme is unchanged with an equal effective port, or the hop is an `http` → `https` upgrade.

   Any `https` → `http` hop is cross-origin. Better: move the helper to one shared place
   (`checkers/checkerdef`) so the two checkers cannot drift again.
2. checkhttp: on a hop that is not same-origin, also delete `Authorization` from `req.Header`.
   It must not depend on net/http's host-only rule. That way basic auth never rides a
   downgraded hop. Record it in `redirect_dropped_secret_headers` like the others.
3. Update the comment above `sameOrigin` to describe the rule actually implemented.

## Tests

- `checkers/checkhttp/checker_test.go`, next to `TestHTTPChecker_Execute_SecretHeadersCrossHostRedirect` (`:2498`), using an `httptest.NewTLSServer` that redirects to an `httptest.NewServer` on the same hostname:
  - port-less and explicit-port https → http hops: the plaintext hop receives neither the secret header nor `Authorization`, and the output lists the dropped names (regression: fails before the fix);
  - same-scheme, same-host hop: the headers are kept (positive control);
  - http → https upgrade on the same host: the headers are kept (positive control).
- A table test of the shared `sameOrigin` with the cases above, plus a cross-host hop and a hop to another port.
- `checkers/checkjs`: the same downgrade case through `redirectPolicy`, so the JS copy is covered too.
