# Magic-link login (passwordless email sign-in)

> **Backlog, not todos, on purpose.** Magic-link login was rejected twice on the
> record — `specs/done/2026/05/2026-05-05-04-auth-flows-paper-cut-audit.md`
> ("rejected per security review: long-lived invites can't be safely converted
> to session-grant magic links") and
> `specs/done/2026/05/2026-05-05-03-activation-time-to-first-signal.md` ("out
> per security pushback… kept for the record so future readers don't
> relitigate"). This spec exists so the *analysis* sits next to those
> rejections: the recorded objection was about converting **long-lived invite
> links** into session grants; a fresh, short-lived, single-use login link is a
> different shape and is buildable safely. It stays parked until there is a
> measured reason to build it (see "When to pick this up").

## Problem

Password-averse users have no email-only way in. Today they use
"forgot password" as a de-facto magic link — request reset, set a throwaway
password, log in — which is three screens and a password they'll forget again.
For a SaaS signup funnel that friction is a conversion cost, and it lands
disproportionately on the least-engaged users (exactly the ones a monitoring
product needs to reach their first check).

The security framing that matters: **email is already a full account-takeover
channel**. Anyone controlling an inbox can run the hardened reset flow
(`specs/done/2026/05/2026-05-03-44-password-reset-hardening.md`) and own the
account. A 15-minute single-use login link grants nothing that flow doesn't
already grant — with one exception, TOTP, which is the non-negotiable
constraint below.

Why it is *backlog* anyway: the auth menu is already unusually rich —
passwords, passkeys (the better passwordless story,
`specs/done/2026/05/2026-05-05-16-passkey-authentication.md`), eight
SSO/OIDC/SAML providers, LDAP, and the device grant. The marginal user served
only by a magic link is small, and that passkey spec's pending follow-up is
magic-link **recovery**, which the reset flow substantially covers.

## Proposal

Clone the password-reset skeleton — `RequestPasswordReset` / `ResetPassword`
in [`server/internal/handlers/auth/service.go`](server/internal/handlers/auth/service.go)
(around `:2589`) — **not** the HMAC pattern in
[`server/internal/incidentlinks/magiclink.go`](server/internal/incidentlinks/magiclink.go).
The incident-links scheme has no revocation and no single-use semantics; fine
for ack/unsubscribe, wrong for minting sessions.

- **`POST /auth/request-login-link`** — email plus optional org preference.
  Anti-enumeration (always the same success response), the reset flow's rate
  limits (per-IP 5/min, per-user 3/h via `state_entries` counters), email sent
  async through the existing `jobdef.JobTypeEmail` path, new `login-link.html`
  template inheriting `base.html`.
- **Token**: 32 bytes from `crypto/rand`, stored **hashed** as
  `login_link:<sha256>` in `state_entries` with `{userUid, orgPref}`, TTL
  ~15 minutes, deleted on consume — exactly the reset pattern (plaintext never
  on disk).
- **Consumption is a POST, never a GET.** Outlook SafeLinks and other mail
  scanners prefetch GET links and would burn the single-use token before the
  human clicks. Land on `/dash0/login-link/$token`, show a "Continue to
  SolidPing" button (or auto-POST), consume via `POST /auth/consume-login-link`.
- **On consume**: if `totp_enabled`, return `{requires2FA, tempToken}` exactly
  like password login — a magic link that mints a session directly would
  silently bypass 2FA, and the reset flow never had this problem because it
  lands back on the login page. Otherwise mint the session through the
  existing path (`resolveOrgPreference`, refresh token, audit event with a new
  auth-method constant in `handlers/auth/audit.go`). Set `email_verified_at`
  if unset — the link proves inbox control. Unlike reset, revoke nothing.
- **Frontend**: an "Email me a login link" option on
  [`web/dash0/src/routes/orgs/$org/login.tsx`](web/dash0/src/routes/orgs/$org/login.tsx),
  the landing route, a label in
  [`web/dash0/src/lib/auth-method-label.ts`](web/dash0/src/lib/auth-method-label.ts),
  and keys in all four `locales/*/auth.json` files (a missing locale key has
  bitten this repo before).
- **Gate**: `auth.magic_link_enabled`, default **off**, surfaced via
  `GET /auth/providers` like `registrationEnabled`, so self-hosters without
  SMTP never show a dead button.

## When to pick this up

When the SaaS funnel shows measured password friction at signup or login
(activation drop-off at the password step, reset-as-login volume). This is a
conversion feature, not a security feature — without that signal it stays
parked.

## Open questions

- Should super-admin accounts be excluded from magic-link login as a hardening
  measure, or is the TOTP gate sufficient?
- Does the login email double as a verification path for pre-existing
  unverified accounts, or should those be forced through
  `confirm-registration` first?

## Not in scope

- Magic-link **invite acceptance** — the shape the security review actually
  rejected; invites keep their password-or-SSO acceptance flow.
- Magic-link **account recovery** for passkey-only users — anticipated as its
  own spec by the passkey spec (D4); the reset flow covers it meanwhile.
