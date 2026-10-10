---
model: opus
effort: high
---

# Discord webhook integrations accept a URL and then never deliver

*Reported by **Jonathan**, a self-hoster, on 2026-10-08: "I just tried using the
webhook but when I send a test notification I get this error: no default
channel configured for discord connection".*

## What is wrong

#409 (v0.30.0, commit e89710ddc) removed Discord webhook delivery.
`DiscordSender.Send` (`server/internal/notifications/discord.go`) now only
sends through the bot and returns `ErrDiscordNoDestination` when the
integration has no `guild_id` / `channel_id`.

Everything around it still offers the webhook:

- The integration form (`web/dash0/src/components/integrations/integration-form.tsx`)
  shows the **Webhook URL** field when the instance has no bot, or when a URL
  is already stored.
- `integrations.json` says "Alerts are delivered one-way through the webhook
  URL below" (`discordNotConnectedBody`, `discordBotUnavailable`) and describes
  the type as "Send to a Discord webhook URL".
- `web/docs/docs/configuration/notifications.md` has a Bot vs Webhook table,
  says webhook mode "is unchanged and still fully supported", and has a
  "Setting up a webhook instead" guide.
- `models.DiscordSettings` still has `webhook_url`, and its comments still say
  the sender falls back to the webhook.

So on any instance without the bot, which is every self-hosted instance that
has not created a Discord application, the only Discord option the UI offers
can never work. The save succeeds, and every test and real alert fails with an
error about a "default channel" the user was never asked for.

## Decision: bring webhook delivery back

Do not remove the field. Restore delivery:

- The bot needs a Discord application, a bot token, a public key, an
  interactions endpoint and (for comments) the gateway. That is a lot to ask
  of a self-hoster who just wants alerts in a channel.
- A webhook is one URL, created in two clicks in the channel settings.
- Slack already works this way: the app, plus "Slack (webhook)" for people who
  cannot install it. Discord should match.

## Proposal

1. `DiscordSender.Send`:
   - Bot destination resolved (current rule) → bot path, unchanged.
   - Otherwise, `webhook_url` set → POST to the webhook.
   - Otherwise → an error that says what to do: "This Discord integration has
     no destination. Install the bot or add a webhook URL."
2. Webhook payload: an embed with the same title, colour, fields and link as
   the bot message. No buttons, no threads, no mentions (webhooks cannot
   receive interactions). Keep `?wait=true` so a 4xx comes back as an error.
   Handle 429 with `retry_after` like the other HTTP senders.
3. Validate `webhook_url` on save: `https://discord.com/api/webhooks/…` or
   `https://discordapp.com/api/webhooks/…` (and `canary.`/`ptb.` variants).
   Reject anything else with a clear message.
4. Update the stale comments on `DiscordSettings` and `UsesBot()`.
5. Docs: keep the Bot vs Webhook table, make sure it matches the code, and add
   one line to the webhook guide saying it is the right choice when the
   instance has no Discord bot.

## Acceptance criteria

- [ ] Instance without `SP_DISCORD_BOT_TOKEN`: create a Discord integration
      with only a webhook URL. Test notification arrives in the channel.
- [ ] A real incident open and resolve arrive through the webhook.
- [ ] An integration with both bot fields and a webhook URL uses the bot.
- [ ] An integration with neither returns the new error text.
- [ ] A non-Discord URL is rejected on save.
- [ ] Unit tests for the three branches of `Send` (httptest server for the
      webhook) and for the URL validation.
- [ ] The e2e `integrations.spec.ts` covers creating a webhook-only Discord
      integration on a bot-less instance.
