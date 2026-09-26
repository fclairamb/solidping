---
model: sonnet
effort: low
---

# Server-side fetches of user-supplied base URLs: Betterstack importer and Freebox pairing

## Problem

Two integration/import surfaces fetch a URL the caller chooses, with
results or error text flowing back:

1. **Betterstack importer** (org-admin gated, `app/server.go:1090-1092`):
   `handlers/checks/importers/betterstack.go:82` accepts `baseUrl`, honors
   the override at `:155-158`, and GETs `<baseUrl>/api/v2/monitors` with the
   caller's Betterstack token in `Authorization` (`:280`) — a GET-anywhere
   primitive with a credential attached, plus response parsing fed back to
   the caller.
2. **Freebox pairing** (user-role gated): `handlers/integrations/service.go:923-931`
   takes `baseUrl` from the request, persists it (`:964-970`), and the
   pairing handshake POSTs `/api/v4/login/authorize/` to it
   (`integrations/freebox/client.go:127,223`), with parsed results/errors
   surfaced via the pairing-status endpoint — a POST/scan primitive by an
   ordinary member against internal JSON APIs.

Both also inherit the general no-egress-guard problem (spec
`2026-09-25-19` covers the dial layer; this spec adds the URL-contract
validation those features actually need).

## Proposal

1. **Betterstack**: the override exists only to support alternate API
   instances. Replace free-form `baseUrl` with a fixed host allowlist:
   accept only `https://` and a host equal to `api.betterstack.com` (or the
   documented EU alternate if one exists — check the vendor docs the
   importer cites); anything else → `VALIDATION_ERROR`. If no alternate
   instance is actually documented, **drop the `baseUrl` field entirely**
   (preferred: one less footgun; the fixed default at `:20` remains).
2. **Freebox**: the point of the feature is talking to the member's own box,
   so private addresses are legitimate — but the URL contract must still be
   enforced:
   - Require `https://` (or `http://` **only** for private-range IPs /
     `mafreebox.freebox.fr`, since local Freebox APIs are commonly plain
     HTTP — verify against `integrations/freebox/client.go` which scheme the
     pairing flow really needs and match it).
   - Reject userinfo, ports outside 80/443/8443 and non-IP non-`*.freebox.fr`
     hosts when the target is not the default.
   - In SaaS mode, disable the `baseUrl` override entirely (pairing is meant
     to run from the member's network or an agent, never the shared worker —
     same reasoning as spec `2026-09-25-22`).
3. Both surfaces dial through the `internal/egress` guard once spec
   `2026-09-25-19` lands (importer: deny-private on shared workers; Freebox:
   allow-private since that is its purpose).
4. Docs + changelog for the narrowed `baseUrl` handling.

## Tests

- Betterstack: `baseUrl=https://evil.example` → 400 at import time, no
  outbound request (fake transport assertion); allowlisted host → mock
  import proceeds.
- Freebox: `baseUrl` with userinfo or a weird port → 400; default
  `mafreebox.freebox.fr` and `http://192.168.1.254` accepted
  (self-hosted); the SaaS-mode server rejects any override.
- Pairing-status endpoint shows a clear validation error, not a raw fetch
  error, when the URL was rejected.