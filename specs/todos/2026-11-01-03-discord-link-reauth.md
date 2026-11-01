---
model: opus
effort: medium
pr: batch
automerge: false
---

# Linking a Discord account needs no re-authentication and sends no notice, so a hijacked session can keep a way in

## Problem

Found by the security scan of 2026-11-01 (finding AB3).

**Severity: low.** It needs a session that is already hijacked (a stolen access or refresh token, an unattended signed-in browser) on an instance where Discord sign-in is configured. No such hijack path is known today. This is the gap AA2 (`specs/done/2026/10/2026-10-31-02-sign-in-factor-enrollment-reauth.md`, fixed in #728) left: AA2 covered TOTP, passkeys and a first password, not external identities.

- `POST /api/v1/orgs/:org/users/me/discord/link-start` (`server/internal/app/server.go:1722`) calls `CreateDiscordLink` (`server/internal/handlers/usernotifications/discord.go:236-268`). It mints a 15-minute single-use link token for the session's user. It refuses impersonation (`discordlink/link.go:55-61`) and PATs (`handlers/auth/pat_guard.go:65`), but a plain session passes with no re-authentication.
- The public callback `handleLinkCallback` (`server/internal/handlers/auth/discord_link.go:62-85`) binds whichever Discord account completes the round trip to that user. `LinkDiscordProvider` (`discord_link.go:141-163`) writes the `user_providers` row and sends no email.
- Discord sign-in resolves the user by that provider ID first (`server/internal/handlers/auth/discord_service.go:410-430` `findOrCreateUser`) and mints a session through `CompleteOrgLogin`.
- The code already treats a link as a sign-in factor: "a bound Discord identity signs in" (`pat_guard.go`, `discordlink/link.go:57-60`).

Impact: the holder of a hijacked session links their own Discord account to the victim. From then on they can sign in as the victim with Discord. The link survives a password change, a password reset, and the revocation of every session and PAT. A victim with TOTP still gets a code prompt on that sign-in (`join_policy.go` `twoFAChallenge`), which limits it to accounts without TOTP.

## Proposal

Apply the AA2 rule (`server/internal/handlers/auth/factor_reauth.go`) to the Discord link:

1. `CreateDiscordLink` takes `currentPassword` in its request body and calls the same re-authentication as `Setup2FA`: the current password when the account has one (through the shared change-password counter), otherwise a primary sign-in within `reauthSignInWindow`. Expose `reauthenticateForFactor` to `usernotifications` through the existing auth service interface rather than duplicating it. Map the errors with `writeReauthError`'s codes (`INVALID_CURRENT_PASSWORD`, `REAUTHENTICATION_REQUIRED`, `RATE_LIMITED`).
2. On a successful link in `LinkDiscordProvider`, email the account owner a "Discord account linked" notice, like `notifyPasskeyAdded` / `notifyTOTPEnabled`: the time, and a link to where the identity can be removed. Add the template and its four locales (en, fr, de, es).
3. dash0: the "Link Discord" action opens the existing re-authentication dialog (`web/dash0/src/components/security/reauth.tsx`) before calling `link-start`.
4. Update the OpenAPI spec, the generated client and `wiki/api-specification/` for the new request field.

## Tests

- `server/internal/handlers/usernotifications` (or the handler test of the route): `link-start` without `currentPassword` on an account with a password answers 401 `INVALID_CURRENT_PASSWORD` and mints no link token (fails before the fix). Same with a wrong password.
- A passwordless account whose session's sign-in is older than the window gets 403 `REAUTHENTICATION_REQUIRED`.
- Positive control: with the correct current password (or a recent sign-in for a passwordless account), `link-start` returns the authorize URL, and the callback links the identity as today.
- `discord_link_test.go`: a successful link enqueues the notice email to the owner. A refused link (claimed by another user) enqueues nothing.
- The impersonation and PAT refusals still hold.
- dash0 Playwright (`web/dash0/e2e/`): linking Discord asks for the password first.

## To verify

- Whether other "link an external identity from a session" flows exist besides Discord. The scan found none: Slack and the other providers link only during their own sign-in or install.
- Where the account page lists linked identities, and whether the user can remove a Discord link there, so the notice email can point to it.
