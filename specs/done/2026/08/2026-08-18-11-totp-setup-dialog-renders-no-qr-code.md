---
model: sonnet
effort: medium
---

# TOTP setup dialog shows no QR code — nothing renders the otpauth URI as a QR image

## Problem

On `/dash0/orgs/$org/account/security`, the "Set up two-factor authentication" dialog opens
with only its title/subtitle, Cancel, and a permanently-disabled Confirm — no QR code, no
manual secret, no code field. 2FA enrolment is impossible from the UI.

The backend side is correct. `POST /api/v1/auth/2fa/setup` returns exactly what it should:

```json
{
  "uri": "otpauth://totp/SolidPing:admin@solidping.com?algorithm=SHA1&digits=6&issuer=SolidPing&period=30&secret=...",
  "secret": "Z5ORW5AY..."
}
```

What's missing is the QR-code *representation* of that URI. The frontend never had a real
renderer:

1. It reads a field the API doesn't have — `resp.qrCodeUrl`
   ([twofa.ts:8](web/dash0/src/api/twofa.ts:8),
   [TOTPSetupDialog.tsx:46](web/dash0/src/components/security/TOTPSetupDialog.tsx:46)) instead
   of `uri`, so the state stays `""` and the `{!loading && qrCodeUrl && …}` gate at
   [TOTPSetupDialog.tsx:113](web/dash0/src/components/security/TOTPSetupDialog.tsx:113) hides
   the entire dialog body — including the manual secret and the code input, which don't depend
   on the QR at all. Silent, because the request itself is a 200.
2. Even with the right field, there is no QR generator anywhere in the stack: the `<img>` at
   [TOTPSetupDialog.tsx:122](web/dash0/src/components/security/TOTPSetupDialog.tsx:122) points
   at `https://api.qrserver.com/v1/create-qr-code/?data=<otpauth URI>` — a third-party service
   handed the TOTP seed in a URL, and unusable in egress-filtered/self-hosted deployments.

## Proposal

Generate the QR code ourselves. Frontend or backend both work; **frontend is the recommended
choice** — it keeps the API as-is (the `{uri, secret}` response is clean and client-agnostic)
and needs no new endpoint:

1. Add the `qrcode` npm package to dash0 and render `uri` locally (data-URL into the existing
   `<img data-testid="2fa-qr-code">`, or its `<canvas>` API). Delete the `api.qrserver.com`
   URL. No network request may leave the page during setup.
   - *Backend alternative, if preferred during implementation:* generate a PNG/SVG server-side
     (e.g. `github.com/skip2/go-qrcode`) and return it as a data URI alongside `uri`/`secret`.
     Pick one; don't do both.
2. Fix the field read: `qrCodeUrl` → `uri` in `twofa.ts` and `TOTPSetupDialog.tsx` (name the
   state `otpauthUri` — it's a URI, not an image URL).
3. Un-blank the failure mode: gate only the QR image on the URI; show the manual secret and
   code input whenever `secret` is present, and a real error alert if setup resolves with
   neither.
4. Add `web/dash0/e2e/account-security-2fa.spec.ts`: open the dialog, assert QR image,
   non-empty manual secret, and code input are visible (fails on today's code). Cover the full
   confirm → recovery-codes path if a TOTP code can be computed in-test from the secret.

Out of scope: documenting `/api/v1/auth/2fa/*` in the OpenAPI spec (currently absent) and the
fact that every dialog open rotates the stored secret — both worth their own pass, neither
blocks this fix.
