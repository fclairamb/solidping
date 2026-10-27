---
model: opus
effort: medium
---

# Device-flow consent approves a code anyone can mint and send by link, with no warning

## Problem

Found by the security scan of 2026-10-27 (finding V1).

**Severity: medium.** Anyone can trigger it, with no SolidPing account. It needs one signed-in member of the target instance to follow a link and click Approve.

This is the RFC 8628 §5.4 "remote phishing" risk (device-code phishing). The current consent page has none of the mitigations the RFC asks for.

- `POST /api/v1/auth/device` and `POST /api/v1/auth/device/token` are unauthenticated (`server/internal/app/server.go:829-830`). Anyone can open a request, receive its `user_code` and `verification_uri_complete`, and poll for the token.
- The requester picks the client name freely (`device_handler.go:43`, `device_service.go:115-148`; the default is "Unknown application", `device_service.go:46`). The attacker can name it after the real CLI.
- The consent page reads the code from `?user_code=` and looks it up immediately (`web/dash0/src/routes/orgs/$org/account.device.tsx:92-101`). A signed-out visitor is sent through login and brought back with the code intact (`web/dash0/src/lib/login-destination.ts:67`, `:126`).
- The authorize card shows only the attacker-chosen name, an org picker, the expiry and Approve / Deny (`account.device.tsx:222-241`, copy `device.authorizeDescription` in `web/dash0/src/locales/*/account.json`). Nothing tells the user to approve only a sign-in they started themselves. Nothing shows where or when the request was opened. The server does not record the requester at all (`DeviceConsentInfo`, `device_service.go:102-107`; `models.DeviceAuthRequest`, `server/internal/db/models/device_auth.go:32-48`).
- On Approve, `RespondToDeviceConsent` mints a 90-day PAT for the approving user in the selected org (`device_service.go:210-262`). The attacker's next poll receives it.

**Attack (no payloads):**
1. The attacker opens a device request on the target instance and names the client after the real CLI.
2. The attacker sends a member a link to the real instance's verification page with the code filled in. The pretext can be "finish connecting your CLI" or "approve the integration". The link must be used within the 15-minute request lifetime (`device_service.go:28`).
3. The member, on the real domain and signed in, sees a normal "Authorize this device" card and clicks Approve.
4. The attacker's poll returns a PAT carrying the member's role in that org for 90 days. A password reset does not revoke PATs (see T8 in the scan reports).

**Impact:** API access as the victim in the selected org (admin if the victim is an admin) for 90 days. The token stays until someone notices it under Account → Tokens.

## Proposal

The legitimate flow (the CLI opens `verification_uri_complete` in the user's own browser) must stay a one-click approval. The fixes add context and friction against a code that arrived from someone else.

1. **Record the requester.** Store the requester's client IP (from the same trusted-proxy resolution the rate limiter uses) and the user agent on `device_auth_requests` when the request is opened (`device_service.go:115-148`, `device_handler.go:43`). This needs a new migration in both dialects, following `wiki/conventions/migrations.md`: a new `NNN` file, never an edit to a tagged one.
2. **Show it on the consent page.** Return `requestedAt`, `requesterIp` and a short user-agent summary in `DeviceConsentInfo` (`device_service.go:102-107`). Render them on the authorize card (`account.device.tsx:228-241`), next to the approving user's own IP if that is available cheaply (see To verify).
3. **Warn explicitly.** Add a destructive-styled alert above Approve, in every locale: "Only approve if you just ran a SolidPing sign-in yourself, on a device in front of you. Never approve a code someone sent you." Mark the client name as self-reported ("calls itself …").
4. **Notify after approval.** Send the user a "new device token created" email naming the client, the org, the requester IP and the revoke location. If an equivalent PAT-created notification already exists, reuse it (see To verify).
5. Leave the RFC 8628 wire format, the code TTL and the PAT expiry unchanged.

## Tests

- `server/internal/handlers/auth/device_service_test.go`:
  - opening a request with a given client IP and user agent, then calling `GetDeviceConsent`, returns `requestedAt`, `requesterIp` and the user-agent summary. This fails before the fix, because the fields don't exist.
  - positive control: approving still mints the PAT, and a single poll still receives it.
- `server/internal/handlers/auth/device_handler_test.go`: the requester IP is taken from the trusted resolution, not from a spoofed `X-Forwarded-For` sent by an untrusted peer.
- Approval sends exactly one notification email to the approving user. Denial and a lost race send none.
- `web/dash0/e2e/device-authorization.spec.ts`:
  - the authorize card shows the phishing warning and the requester details. This fails before the fix.
  - positive control: the existing approve path still completes, and the CLI poll gets its token.

## To verify

- Is there already an email or audit notification when a PAT is created, and can it carry the device context?
- The helper that gives the trusted client IP (the same one the auth rate limiter uses), so this spec does not add another M10-style spoofable source.
- Can the consent endpoint cheaply expose the approver's own IP, for a "this request came from a different network" hint? If not, show only the requester IP.

## Open questions

- Should a code that arrives pre-filled from the URL need a second confirmation, such as ticking "I started this sign-in myself" or retyping the code? Recommended: a checkbox only. Retyping defeats the point of `verification_uri_complete` for the legitimate CLI flow.
