---
model: sonnet
effort: medium
---

# Notification senders POST to fully user-controlled URLs with no validation

## Problem

Notification integration URLs are taken straight from settings with no
parse, scheme or host check anywhere:

- webhook: `notifications/webhook.go:151-153` (`Settings["url"]`)
- gotify: `notifications/gotify.go:74` (`server_url`)
- ntfy: `notifications/ntfy.go:39` (`serverUrl`, any URL accepted)
- matrix: `notifications/matrix.go:84` (`homeserverUrl`, GET at `:165`)
- googlechat / mattermost / discord / slack incoming: `webhook_url`
  (`googlechat.go:74`, `mattermost.go:92`)

Integration CRUD validates only Twilio and MS-Teams bot settings
(`handlers/integrations/service.go:403,494-501,804-813`) — nothing validates
these URLs.

**Why this matters (the explanation)**: the server POSTs incident payloads to
whatever URL an org member configures. Without validation, a member points a
webhook at an internal address — `http://169.254.169.254/...` (cloud
metadata), `http://localhost:4000/api/v1/...` (our own API) — and triggers a
delivery on demand via `POST /orgs/:org/integrations/:uid/test`
(`app/server.go:1731`, user-role gated). Crucially this is **not blind SSRF**:
every delivery persists `DeliveryDetails` — status code plus up to 16 KB of
the target's **response body** (`notifications/webhook.go:228-256`,
`db/models/delivery_details.go`) — readable back via
`GET /orgs/:org/incidents/:uid/notifications`. That is a complete read-SSRF
loop. Attacker-set custom headers (`webhook.go:210-219`) additionally let the
server send chosen `Authorization` headers at internal services.

## Proposal

1. Shared validator in `internal/notifications` (used by integration CRUD at
   create AND update, and defensively by each sender before use):
   - Require `http://` or `https://`, reject userinfo in the URL, require a
     host, reject hosts resolving to loopback/link-local/private ranges
     **unless** `egress.allow_private_targets` is true (reuse the
     `internal/egress` guard from spec `2026-09-25-19` — senders should dial
     through the same guarded dialer so rebinding is covered).
   - Webhook custom headers keep working; the guard is transport-level, so
     no header changes needed.
2. Apply per-sender to the six sender families above; Slack/Discord bot /
   Telegram / PagerDuty / Pushover / Twilio already use fixed vendor hosts —
   leave them alone.
3. Error surfaces as a normal `VALIDATION_ERROR` on integration save
   (`"url must be a public http(s) endpoint"`) and as a failed delivery with
   a clear `DeliveryDetails` error when the policy changes under an existing
   integration.
4. Docs: note in the integrations docs + changelog entry.

Existing protections that stay: URL redaction in logs, signing-header
override protection, secrets in encrypted `settings_private` (spec docs in
`notifications/webhook.go:289-321`).

## Tests

- Table-driven validator tests: http/https accepted, `file://`, `gopher://`,
  userinfo, no-host, metadata IP, `localhost`, RFC1918 hostnames rejected;
  accepted when `allow_private=true`.
- CRUD tests (both DBs): creating/updating a webhook with
  `url=http://169.254.169.254/` → 400 `VALIDATION_ERROR`; with a public URL →
  200.
- Sender test: a webhook pointing at a loopback test server fails delivery
  with the egress error recorded in `DeliveryDetails`, no request issued
  (assert the test server received nothing).
- `POST /integrations/:uid/test` with an internal URL → validation error
  before any delivery is attempted.