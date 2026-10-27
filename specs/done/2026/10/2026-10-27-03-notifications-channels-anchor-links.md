---
model: haiku
effort: low
pr: batch
automerge: false
---

# Link each channel in the "Supported Channels" table to its section

## Problem
In `web/docs/docs/configuration/notifications.md:12-33` (https://solidping.k8xp.com/docs/configuration/notifications#supported-channels) the first column ("Channel") is plain text. Each channel that has a section further down the page should link to its anchor.

## Proposal
Wrap the first-column name in an anchor link, in `web/docs/docs/configuration/notifications.md:14-33`. Docusaurus slugs (github-slugger) of the headings:

| Row | Anchor | Heading (line) |
|---|---|---|
| Slack | `#slack` | `## Slack` (132) |
| Slack (webhook) | `#slack-webhook` | `## Slack (webhook)` (267) |
| Teams (bot) | `#teams-bot--the-slack-grade-option` | `### Teams (bot) — …` (307) |
| Teams (webhook) | `#teams-webhook--the-zero-infra-option` | `### Teams (webhook) — …` (297) |
| Discord | `#discord` | 465 |
| Email | `#email-smtp` | `## Email (SMTP)` (35) |
| Webhooks | `#webhooks` | 931 |
| Google Chat | `#google-chat` | 688 |
| Mattermost | `#mattermost` | 699 |
| ntfy | `#ntfy` | 711 |
| Gotify | `#gotify` | 734 |
| Matrix | `#matrix` | 756 |
| Zulip | `#zulip` | 795 |
| PagerDuty | `#pagerduty` | 839 |
| Pushover | `#pushover` | 868 |
| Web Push | `#web-push` | 897 |

Rows with no section on this page: AI agent (A2A), SMS / Voice, WhatsApp, Telegram. Link their first column to the page already linked in the third column (`../features/ai-agent-a2a.md`, `./sms.md`, `./whatsapp.md`, `./telegram.md`) and leave the third column as is.

The two Teams slugs contain a double dash (em dash removed): confirm them against the built page, or add explicit `{#teams-bot}` / `{#teams-webhook}` ids to those two headings and use those instead.

## Tests
- `cd web/docs && bun run build` (or the repo's docs build): no broken-anchor warning (Docusaurus `onBrokenAnchors`).
- Manually check in the built page that each of the 20 first-column links lands on its section (negative case: no link 404s or stays on the table).

## To verify
- Exact slugs of the two Teams headings (see above).
