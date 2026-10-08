# Kener — Analysis (vs SolidPing)

*Researched 2026-10-06 from primary sources only (GitHub repository and the docs
shipped in it, `src/routes/(docs)/docs/content/v4/`). Promoted from the
`indie-watch.md` new-entrants block.*

## Overview

Kener is an open-source, self-hosted **status page** system with built-in
monitoring, built by Raj Nandan Sharma. Its own description: "a sleek and
lightweight status page system built with SvelteKit and NodeJS. It's not here to
replace heavyweights like Datadog or Atlassian." The status page is the product;
monitors feed it.

SolidPing's overlap is the status page + monitor + incident core. Kener is ahead
on the public status page; SolidPing is ahead on monitoring and alerting.

- **Website / docs**: https://kener.ing
- **Repository**: https://github.com/rajnandan1/kener
- **License**: MIT

## At a glance

| Attribute | Value (2026-10-06) |
|---|---|
| **GitHub** | 5,189★, 302 forks, created 2023-12 |
| **Latest release** | v4.1.6 (2026-10-02); v4.1.3 → v4.1.6 in a month |
| **Stack** | SvelteKit + Node.js (>= 24.14), Express, BullMQ |
| **Database** | SQLite (default), PostgreSQL or MySQL |
| **Other services** | **Redis is required** ("Kener will not start without `REDIS_URL`") |
| **Monitor types** | 12: API (HTTP with custom eval), Ping, TCP, DNS, SSL, SQL, Heartbeat, GameDig, gRPC, Docker, Prometheus (PromQL), Group |
| **Scheduling** | Per-monitor cron expression (e.g. `* * * * *`). Grace period of N consecutive checks |
| **Multi-region** | Not documented. Checks run from the Kener server |
| **Alert channels** | Webhook, Discord, Slack, email ("Current runtime supports" these four) |
| **Status pages** | Multiple pages per instance, themes, custom CSS/JS, i18n, embeds and badges, RSS, SEO |
| **Subscribers** | Email subscriptions to incidents and maintenances, OTP-verified |
| **Incidents / maintenance** | Incident timelines and updates; maintenance windows with RRULE recurrence |
| **Users** | RBAC with admin / editor / member and custom roles; OIDC SSO |
| **API** | REST API with API keys |
| **Hosted offer** | None documented. Free, self-hosted only |

## Where Kener is ahead

- **The public status page.** Theming, custom CSS and fonts, multiple pages,
  embeddable widgets, analytics hooks, RSS. It is the product's focus.
- **Subscriber email** with OTP verification and per-event preferences.
- **Recurring maintenance with RRULE.**
- **Custom roles** with fine-grained permissions, and OIDC group-to-role mapping.
- **MySQL support** (SolidPing: SQLite or PostgreSQL).

## Where SolidPing is ahead

- **Check types**: 43 against 12.
- **Alerting**: Teams, Telegram, PagerDuty, ntfy, Matrix, Pushover, Google Chat,
  Zulip and more, plus SMS and voice through your own Twilio, on-call rotations and
  escalation. Kener has four channels and no on-call.
- **Multi-region workers and private agents.** Kener checks from one place.
- **Fewer moving parts.** SolidPing is one binary on SQLite; Kener is Node.js plus
  a required Redis.
- **Hosted option.** SolidPing Cloud exists; Kener has none.
- **MCP server and CLI.** Not documented for Kener.

## Migration notes

SolidPing has **no Kener importer**. Do not claim one on the site.

## Positioning takeaway

Kener ranks for "open-source status page". Pages comparing to it should concede the
status page and argue on monitoring and alerting.

## Sources

- https://github.com/rajnandan1/kener (README, LICENSE, `gh api` 2026-10-06)
- Docs in repo, v4: `architecture.md`, `configuration.md`, `monitors/overview.md`,
  `monitors/prometheus.md`, `alerting/overview.md`, `user-management.md`,
  `oidc.md`, `subscriptions.md`, `setup/redis-setup.md`, `guides/comparison.md`
