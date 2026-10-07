# Upptime — Analysis (vs SolidPing)

*Researched 2026-10-06 from primary sources only (GitHub repositories, upptime.js.org
docs, GitHub Actions docs).*

## Overview

Upptime is an uptime monitor and status page that runs entirely on GitHub: Actions
runs the checks, Issues hold the incidents, Pages hosts the status site, and the
response-time history is committed to the repository. There is no server to run.
It is built by Anand Chowdhary. You create a repository from the template and edit
one file, `.upptimerc.yml`.

It is the most-starred tool in our space after Uptime Kuma, and it competes with
SolidPing mostly for the "free status page for a few URLs" use case.

- **Website / docs**: https://upptime.js.org
- **Repository**: https://github.com/upptime/upptime (template) and
  https://github.com/upptime/uptime-monitor (the engine the workflows run)
- **License**: MIT. Data in `./history` is under the Open Database License.

## At a glance

| Attribute | Value (2026-10-06) |
|---|---|
| **GitHub** | 17,177★, 1,042 forks (template repo); engine 315★ |
| **Releases** | Template last tagged v2.0.0 (2020-10-13), updated through a weekly "Update Template CI" workflow. Engine `uptime-monitor` v1.44.1 (2026-09-21) |
| **Runtime** | GitHub Actions (hosted or self-hosted runners). Status site: Svelte/Sapper on GitHub Pages |
| **Storage** | The git repository itself (YAML history files, commits) |
| **Check types** | HTTP(S) with method, headers, body, expected status codes, max response time, body text match; `tcp-ping` to a host and port. `icmp-ping` is not supported. Optional Globalping checks (HTTP or ping) from a chosen location |
| **Interval** | Every 5 minutes by default. 5 minutes is GitHub's minimum for scheduled workflows, and GitHub says scheduled runs "can be delayed during periods of high loads" and some queued jobs "may be dropped" |
| **Incidents** | A GitHub issue opens when a site goes down and closes when it comes back |
| **Notifications** | Slack, Telegram, Discord, Zulip, Microsoft Teams, Gotify, custom webhook, email (SendGrid, SES, SparkPost, Mailgun, SMTP), SMS (46elks, Callr, Clickatell, Infobip, Nexmo, OVH, Plivo, Twilio) |
| **Status page** | Static PWA on GitHub Pages, custom domain, i18n, scheduled maintenance |
| **Private repos** | Status site only works from a public repo by default; private needs a proxy API (FAQ, issue #54) |
| **Cost** | Free; uses your GitHub Actions minutes |

## Where Upptime is ahead

- **Nothing to host.** No server, no database, no container. A public repository
  on GitHub's free tier is enough.
- **Git as the audit trail.** Every config change, incident and history point is a
  commit or an issue.
- **SMS providers** wired in natively (eight of them); SolidPing does SMS through
  your own Twilio account.
- **Zero cost for a handful of public URLs.**

## Where SolidPing is ahead

- **Interval and timing.** SolidPing checks every 10 s if asked; Upptime every
  5 minutes at best, and GitHub may delay or drop scheduled runs.
- **Check types.** 43 against HTTP and TCP.
- **Multi-region from your own workers**; Upptime runs from the Actions runner or
  from Globalping locations (with Globalping's own rate limits).
- **Alerting model.** Escalation, on-call rotations, maintenance windows that mute
  alerts, incident correlation. Upptime opens an issue and notifies.
- **Private by default.** No public repository needed for the dashboard or status
  page.
- **API, CLI, MCP server.** Upptime's API is the GitHub API on the repo.

## Migration notes

SolidPing has **no Upptime importer**. The `.upptimerc.yml` sites list is simple
enough to recreate as SolidPing HTTP checks via the API or YAML apply, but nothing
does it automatically. Do not claim an importer on the site.

## Positioning takeaway

Upptime is the right answer for "a free status page for three URLs, and I already
live on GitHub". SolidPing is the step up when 5-minute best-effort timing is not
enough, or when the targets are not HTTP. Never frame GitHub-run timing as a defect
of Upptime; it is GitHub's documented scheduling behaviour and Upptime says so.

## Sources

- https://github.com/upptime/upptime (README, `.upptimerc.yml`, `gh api` 2026-10-06)
- https://github.com/upptime/uptime-monitor (releases)
- https://github.com/upptime/upptime.js.org — `docs/configuration.md`,
  `docs/notifications.md`, `docs/triggers.md`, `docs/faq.md`
- GitHub Docs, "Events that trigger workflows" → `schedule`
