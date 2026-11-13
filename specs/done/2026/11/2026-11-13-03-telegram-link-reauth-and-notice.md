---
model: opus
effort: medium
---

# Linking a Telegram account mints a sign-in method without re-authentication or notice

## Problem

Found by the security scan of 2026-11-13 (finding AL1). Severity: low. Who can trigger it: the
holder of a hijacked interactive session (stolen access token, unlocked browser) of a user, on
an instance with Sign in with Telegram on (`SP_TELEGRAM_LOGIN_ENABLED`, off by default).

Since #848, a verified `telegram` user contact also works as a sign-in method:
`TelegramLoginService.HandleCallback` signs in the user who owns the verified contact whose
value is the Telegram user id (`server/internal/handlers/auth/telegram_service.go:280-345`,
`resolveContact` at `:312`). That contact is created when whoever holds a connect token presses
Start in the bot chat (`server/internal/handlers/telegramcb/handler.go:380-411`, `VerifiedAt`
set at `:390`).

The connect token is minted by `POST /api/v1/orgs/:org/users/me/telegram/link`
(`server/internal/app/server.go:1748`) → `Service.CreateTelegramLink`
(`server/internal/handlers/usernotifications/telegram.go:86-118`). That call refuses
impersonation (`:89`) and PATs refuse it too (`server/internal/handlers/auth/pat_guard.go:68`).
But it never re-authenticates the user and no email goes out when the link completes.

The Discord link is the same kind of sign-in method, and it got both protections in the AB3 fix:
- `CreateDiscordLink` calls `ReauthenticateForFactor` (`handlers/usernotifications/discord.go:277-283`);
- `notifyDiscordLinked` emails the user (`handlers/auth/discord_link_notice.go:27`).

Impact: the session holder links their own Telegram account to the victim. From then on they
can sign in as the victim with Telegram. That access survives a password reset and the
revocation of every session and PAT, and the victim is never told. A victim with TOTP is still
asked for the code (`CompleteOrgLogin`, `handlers/auth/join_policy.go:376-377`).

There is a second, smaller effect. Turning `SP_TELEGRAM_LOGIN_ENABLED` on silently made every
Telegram contact linked before #848 (linked as a paging channel only) a sign-in method too.

## Proposal

1. `CreateTelegramLink` takes `currentPassword *string`. After `refuseImpersonation`, it calls
   `s.reauth.ReauthenticateForFactor` the same way `CreateDiscordLink` does. It fails closed
   with `ErrReauthUnavailable` when no re-authenticator is wired, and returns the same
   `auth.WriteReauthError` answers. Apply this only when Telegram sign-in is active
   (`cfg.Telegram.LoginActive()`). With sign-in off, the link is only a paging channel, and the
   current flow stays as it is.
2. `handlers/usernotifications/handler.go:137` (`CreateTelegramLink`) decodes the optional
   `currentPassword` body field, like the Discord link-start handler. Update the OpenAPI spec
   (`server/internal/app/openapi/openapi.yaml`) and the dash0 hook
   (`web/dash0/src/api/hooks.ts:7247-7270`). The connect button in
   `routes/orgs/$org/account.notifications.tsx` must prompt for the password, or show the "sign
   in again" answer, exactly like the Discord connect flow. Reuse its component.
3. When `linkContact` (`telegramcb/handler.go:380`) creates a contact, or revives a deleted one,
   and Telegram sign-in is active, email the user at their current address: "a Telegram account
   was linked, and it can now sign you in; remove it in Notifications if it wasn't you". Model it
   on `notifyDiscordLinked`. Send nothing when the link only refreshes an already-verified row.
4. In `web/docs/docs/configuration/telegram.md`, add one line: enabling sign-in makes the
   Telegram contacts users already linked a sign-in method too.

## Tests

- `handlers/usernotifications/telegram_test.go`, all with Telegram sign-in active:
  - `CreateTelegramLink` with a wrong or missing password fails and stores no state entry (regression: fails before the fix).
  - The correct password returns a URL (positive control).
  - A passwordless user with a sign-in older than 10 minutes is refused.
  - Impersonation is still refused before the password is checked.
  - No re-authenticator wired: it fails closed.
- With Telegram sign-in off, `CreateTelegramLink` without a password still succeeds (positive
  control for the scope of step 1).
- `handlers/telegramcb/handler_test.go`:
  - The first link with sign-in active sends exactly one notice to the user's email (regression).
  - A re-link of an already-verified contact sends none.
  - With sign-in off, no notice is sent.
- The dash0 Playwright notifications spec: connecting Telegram asks for the password when sign-in is on.
