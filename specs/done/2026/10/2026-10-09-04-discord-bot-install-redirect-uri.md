---
model: sonnet
effort: medium
---

# "Install Discord bot" fails with "Invalid OAuth2 redirect_uri" on self-hosted instances

*Reported by **Jonathan**, a self-hoster, on 2026-10-08: "I figured out that I
needed to setup the Discord authentication, configured that and then I could go
to the Discord integration and I had a button for Install Discord bot but when I
click that button I get an Invalid Oauth2 redirect_uri error from Discord."*

This is the second report of the same failure. The first one is in
`specs/done/2026/09/2026-09-19-01-discord-bot-half-configured.md`, where the
redirect mismatch was "the leading suspect".

## What is wrong

Discord login and bot install use two different redirect URIs on the same
Discord application:

| Flow | Redirect URI | Built in |
|---|---|---|
| Login, account linking | `{SP_BASE_URL}/api/v1/auth/discord/callback` | `handlers/auth/discord_service.go` |
| Bot install | `{SP_BASE_URL}/api/v1/integrations/discord/oauth` | `integrations/discord/service.go` `installRedirectURI()` |

The public docs only list the first one
(`web/docs/docs/configuration/authentication.md`, "Add redirect URL"). The bot
operator section of `notifications.md` lists the client ID, secret, bot token,
public key and gateway, but never the install redirect or the Interactions
Endpoint URL. Both only exist in the internal runbook
`wiki/runbooks/discord-bot-setup.md`.

A self-hoster following the public docs registers one URI, login works, and
the install fails at Discord's authorize step.

Two smaller issues:
- `SP_DISCORD_REDIRECT_URL` / `auth.discord.redirect_url` is read into
  `cfg.Discord.RedirectURL` and never used. Setting it does nothing.
- `SP_DISCORD_ENABLED` is required for the login routes but is not in the
  public docs.

## Proposal

1. **Docs.** Add a "Discord application setup" section to
   `web/docs/docs/configuration/notifications.md` (bot) and link it from
   `authentication.md` (login). It lists every value to put in the Discord
   developer portal, in the order the portal asks for them:
   - OAuth2 → Redirects: both URIs above.
   - Installation → scopes `bot`, `applications.commands`, `identify` and the
     permissions `botPermissions` composes (named, not as an integer).
   - General Information → Interactions Endpoint URL:
     `{SP_BASE_URL}/api/v1/integrations/discord/interactions`, saved **after**
     `SP_DISCORD_PUBLIC_KEY` is set.
   - Bot → Message Content intent if the gateway is enabled.
   Add `SP_DISCORD_ENABLED` to the env var table.
2. **Show the URIs in the product.** On the Discord settings page
   (`server.auth.tsx` Discord provider, and wherever the bot is configured),
   show the exact redirect URIs and the interactions URL computed from the
   current base URL, each with a copy button. A self-hoster should not need the
   docs to get these right.
3. **Warn on a localhost base URL.** When `SP_BASE_URL` is still the default
   `http://localhost:4000` and the request came from another host, show a
   warning next to those URIs: Discord will redirect to localhost.
4. **Remove `SP_DISCORD_REDIRECT_URL`.** Drop the config field and the system
   parameter. Log a startup warning if the env var is set, saying it is
   ignored and both URIs derive from `SP_BASE_URL`. Do not wire it: one
   override for two different paths would be more confusing than none.

## Acceptance criteria

- [ ] Following only the public docs, a fresh Discord application lets a user
      log in **and** install the bot on a self-hosted instance.
- [ ] The Discord settings page shows both redirect URIs and the interactions
      URL, matching what the server actually sends.
- [ ] A test asserts the displayed install URI equals `installRedirectURI()`.
- [ ] The localhost warning shows when the base URL is the default and the page
      is opened through another host.
- [ ] `SP_DISCORD_REDIRECT_URL` is gone from config and systemconfig, with the
      startup warning.
- [ ] Translations exist in en, fr, de, es.

## Reproduce

Create a new Discord application. Register only
`{SP_BASE_URL}/api/v1/auth/discord/callback`. Set `SP_DISCORD_ENABLED`,
`SP_DISCORD_CLIENT_ID`, `SP_DISCORD_CLIENT_SECRET`, `SP_DISCORD_BOT_TOKEN`,
`SP_DISCORD_PUBLIC_KEY`. Log in with Discord (works), then click **Install
Discord bot** on a Discord integration: Discord shows "Invalid OAuth2
redirect_uri".
