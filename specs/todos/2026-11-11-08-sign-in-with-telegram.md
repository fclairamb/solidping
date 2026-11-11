---
model: opus
effort: high
---

# Sign in with Telegram

GitHub Issue: https://github.com/fclairamb/solidping-business/issues/67

## Source

In-app report (v0.46.0, Android Chrome), filed from the demo org's login page: "could we add
Telegram as an authentication provider?". A feature request, not a bug. The login page today
offers Slack, Google, GitHub, Microsoft, GitLab and Discord, plus email/password and the demo.

## Problem

- Sign-in providers are each a handler/service pair in `server/internal/handlers/auth/`
  (`discord.go` + `discord_service.go` is the closest template), advertised by
  `ListProviders` (`server/internal/handlers/auth/providers_available.go`) and rendered by
  dash0's login page.
- Telegram is already a notification medium: one instance-wide bot
  (`config.TelegramConfig`, `server/internal/config/config.go:1876-1915`, `SP_TELEGRAM_*`,
  separate bots for dev and prod), a webhook (`server/internal/handlers/telegramcb/`) and
  user contacts of type `telegram` linked through a `t.me/<bot>?start=` deep link.
- Telegram is not OAuth2/OIDC. Its web login ("Login Widget", or the redirect form
  `https://oauth.telegram.org/auth?bot_id=…&origin=…&return_to=…`) returns the Telegram user's
  `id`, names, `username`, `photo_url`, `auth_date` and a `hash`, verified as
  HMAC-SHA256 of the sorted `key=value` lines keyed by SHA-256 of the bot token. It returns no
  email address, and the bot must have the site's domain set with @BotFather `/setdomain`
  (one domain per bot).

## Why a spec

- New endpoints (start + callback) and a new provider in `ListProviders` / the OpenAPI file.
- Correctness hinges on authentication: signature and freshness checks, account matching
  without an email.
- A product decision the reporter did not settle: who may sign in with a Telegram identity.

## Proposal

1. **Flow**: the redirect form, not the embedded widget (no third-party script, so no CSP
   change; works the same on mobile). `GET /api/v1/auth/telegram/login` redirects to
   `oauth.telegram.org/auth` with the bot id, the origin and a `return_to` on
   `/api/v1/auth/telegram/callback`, carrying a signed state (org, `returnTo`) like the other
   providers. The callback reads the result, verifies the hash with the bot token, refuses an
   `auth_date` older than 5 minutes (replay), then resolves the user.
2. **Account matching** (see open question): a Telegram identity signs in only to the
   SolidPing user who already has a verified `telegram` contact with the same Telegram user id.
   No auto-registration (no email to create the account with).
3. **Enablement**: shown in `ListProviders` only when Telegram is enabled
   (`TelegramConfig.IsEnabled`) and a new `SP_TELEGRAM_LOGIN_ENABLED` is true (default false:
   the bot's domain must be set with @BotFather first, which the operator does by hand).
4. dash0: a "Telegram" button on the login page (`data-testid="login-telegram"`), same layout
   as the others.
5. Audit: the login audit records the method as `telegram`.
6. Tests: hash verification (valid, tampered field, wrong bot token, stale `auth_date`), the
   callback for a linked user (session issued), an unlinked Telegram id (refused with a message
   telling the user to link Telegram in their account first), and `ListProviders` gating.
7. Docs: `web/docs` authentication page (setup: `/setdomain`, the env var), OpenAPI for the two
   endpoints.

## Open questions

- **Who may sign in with Telegram?** Recommended: only users who already linked the same
  Telegram account as a verified notification contact (the link proves ownership; no
  email-less accounts appear). Alternative: allow sign-up from Telegram with an email asked
  afterwards, which needs a new "pending email" account state.
- **Is the feature worth it now?** Recommended: yes but behind the default-off switch; the bot
  and the user-contact link already exist, so the cost is the two endpoints and the hash check.

## Closing the issue

This spec closes fclairamb/solidping-business#67. The implementing PR body **must** carry a
`Closes fclairamb/solidping-business#67` line. When the spec is archived to `specs/done/`,
verify the issue is closed and, if not, close it by hand:

    gh issue close 67 --repo fclairamb/solidping-business --comment "Implemented by <PR or commit>; spec: <archived spec path>"
