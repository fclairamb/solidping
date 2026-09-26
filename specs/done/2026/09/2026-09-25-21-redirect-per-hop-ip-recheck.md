---
model: sonnet
effort: medium
---

# HTTP check redirects pivot external targets to internal hosts with no per-hop re-check

## Problem

`checkhttp` follows redirects unconditionally up to 10 hops with no
host-continuity or IP revalidation:

```go
// checkhttp/checker.go:242-255 — CheckRedirect returns nil until the hop cap
```

A public `http://` target can 302 to `http://169.254.169.254/` or
`http://127.0.0.1:4000/api/v1/...`; the worker follows it, and the failure
capture stores the **final hop's** response (body/headers) into check
results (`checkhttp/checker.go:362-389`) — an external-to-internal pivot
with readback, bypassing any URL-level check on the configured target. Same
policy shape in `checkjs` (`checker.go:832-841`).

## Proposal

This is largely covered by spec `2026-09-25-19` if, and only if, the egress
guard is enforced at the **dial layer**: every hop of a redirect opens a new
connection, so a dial-level IP check re-validates each hop automatically.
This spec pins that requirement down and adds what the dial guard cannot do:

1. Rely on `internal/egress` dial-level enforcement for the per-hop IP check
   (this spec adds the tests proving redirect hops are covered — see below).
2. Add a `redirect_host_policy` option to `checkhttp` / `checkjs` config
   (default `any`, options `same-host`): when `same-host`, the
   `CheckRedirect` function rejects any hop whose URL host differs from the
   previous hop's, recording `redirect to different host refused` in
   diagnostics. Useful for operators monitoring a specific endpoint that
   must not bounce.
3. Keep the existing hop cap (10) and `follow_redirects: false` opt-out as
   they are.
4. Diagnostics: record each redirect hop's URL (already partly done in
   `checkjs`) so an operator can see where a chain went.

## Tests

- Integration: public-facing test server (bound to a non-loopback address or
  a hostname resolving publicly) that 302s to `http://127.0.0.1:<port>/` —
  with `allow_private=false`, the check fails with the egress error and the
  loopback server records zero requests (proves the dial guard fires on hop
  2, not just on the configured target).
- Same setup with `redirect_host_policy: same-host` and
  `allow_private=true`: the redirect is refused by policy with the
  diagnostics message; the loopback server still records nothing.
- `follow_redirects: false` unaffected; hop-cap tests keep passing.
- `checkjs` redirect chain: same assertions via the JS-side client.