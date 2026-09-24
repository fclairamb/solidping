---
model: opus
effort: xhigh
---

# An organization cannot require its members to sign in through a given SSO provider

**Status:** backlog. Split out of
`specs/todos/2026-09-23-02-password-reset-for-sso-users.md`, which makes password
reset always available to SSO-only users. Once that lands, any member can give
themselves a password, so an org that wants "SSO only" has no way to get it.

## Problem

Sign-in methods are global. A user has at most one password (`users.password_hash`)
and any number of linked providers (`user_providers`), and whichever one they use
opens every org they belong to. An org cannot say "our members sign in with our
Google Workspace / our SAML IdP, nothing else".

That matters for offboarding: when the IdP disables someone, the org expects them
to lose access. Today they keep it through a password they set, or through another
provider (GitHub, for example) linked to the same user.

## Proposal (to refine)

- Org setting: an allowed-providers list (e.g. `["google"]`, `["saml"]`), empty
  meaning "anything goes" (today's behavior). Probably an org parameter, admin or
  owner-writable, shown on the org settings page next to the auto-join settings.
- Enforced when a session enters the org, not when the user authenticates: login
  is already org-scoped (`POST /auth/login` carries `org`), and org switching must
  re-check. A session obtained with a method the org does not allow is refused for
  that org with a dedicated error code (e.g. `SSO_REQUIRED`) naming the required
  provider, so dash0 can show a "Continue with Google" button instead of a generic
  403.
- The session needs to record *which* method authenticated it (password, which
  provider) so the check is possible. Today the JWT does not carry it (`Claims`,
  `server/internal/handlers/auth/service.go:176`).
- Password reset stays available (spec 2026-09-23-02), but the reset email for a
  member of an enforced org should say that org requires the provider, so the
  password isn't mistaken for a way in.

## Open questions

- Non-interactive credentials: PATs, MCP OAuth tokens, device auth (CLI). Do
  existing ones keep working? Probably yes for PATs (explicitly issued), but a PAT
  created after authenticating with a now-forbidden method is a loophole.
- Break-glass: an owner locked out when the IdP is down. Exempt owners, or require
  at least one owner who can still sign in before the setting can be saved?
- Can an admin enable it while they themselves are signed in with a forbidden
  method (self-lockout)?
- Provider identity, not just provider type: "Google" should probably mean "Google
  with a `hd` claim of acme.com", not any Google account. Same for Microsoft
  tenants. That needs the domain / tenant stored on `user_providers` or checked at
  login.
- SAML / OIDC are single global instances today (`config/saml_auth.go`). Per-org
  IdPs are the natural next step and change what "force a provider" means.
- Existing sessions when the setting is turned on: revoke them, or let them expire?
