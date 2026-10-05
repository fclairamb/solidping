# OneUptime — Analysis (vs SolidPing)

*Researched 2026-10-06 from primary sources only (GitHub repository, in-repo docs,
oneuptime.com/pricing). OneUptime ships a release almost every day — re-verify
before quoting.*

## Overview

OneUptime (HackerBay, Inc.) is an open-source observability platform that bundles
uptime monitoring, status pages, incidents, on-call, logs, traces, metrics, error
tracking, workflows and an AI agent in one product. It sells a hosted cloud and
offers a free self-hosted Community Edition. Its README pitch is to replace
"Pingdom / UptimeRobot, StatusPage.io, PagerDuty / Opsgenie, Incident.io,
Datadog / New Relic, Loggly, Sentry" with one app.

It is the broadest open-source tool in our space. The overlap with SolidPing is
the uptime + status page + incident + on-call core. The difference is scope and
weight: OneUptime is a full telemetry platform on three datastores, SolidPing is
an uptime monitor in one Go binary.

- **Website**: https://oneuptime.com · **Docs**: https://oneuptime.com/docs
- **Repository**: https://github.com/OneUptime/oneuptime
- **License**: Apache-2.0, except the `ee/` directory (OneUptime Enterprise
  License). The Community image contains no `ee/` code (README, LICENSE).

## At a glance

| Attribute | Value (2026-10-06) |
|---|---|
| **GitHub** | 7,703★, 469 forks, created 2021-06 |
| **Latest release** | 14.0.14 (2026-10-04); 14.0.10 → 14.0.14 in five days |
| **Language** | TypeScript (plus Go agents) |
| **Datastores** | PostgreSQL (config/state), ClickHouse (all telemetry), Valkey/Redis (cache, queues, sessions) — all three required in production (`installation/sizing.md`) |
| **Self-host footprint** | Docker Compose: recommended 16 GB RAM, 8 cores, 400 GB disk; "homelab" minimum 8 GB RAM, 4 cores, 20 GB (`installation/docker-compose.md`). Helm chart for Kubernetes |
| **Editions** | Community (free, Apache-2.0, includes SAML & OIDC SSO) · Enterprise (contact sales: SCIM, audit logs, compliance dashboards, support, data residency) |
| **Cloud pricing** | Free $0 (1 status page, 100 subscribers) · Growth **$22/user/mo** ($20 yearly) · Scale **$99/user/mo** ($84 yearly, SSO) · Enterprise custom. Active monitors billed on top, from **$1/monitor/mo**. SMS / WhatsApp $0.10 each, calls $0.10/min, or bring your own Twilio. Telemetry $0.10/GB ingested (15-day retention) |
| **Monitor types** | Docs list ~40 monitor pages: website, API, ping, IP, port, SSL, DNS, DNSSEC, domain, SQL query, database health, synthetic (Playwright), custom JS, external status page, incoming request (heartbeat), incoming email, host/server, Kubernetes, Docker, Docker Swarm, Podman, Proxmox, VMware, Ceph, IoT, network device, logs, metrics, traces, exceptions, profiles, manual |
| **Check interval** | Cloud pricing page: "monitor resource every minute". Self-hosted probes floor at 1 minute: [#2937](https://github.com/OneUptime/oneuptime/issues/2937) still **open, 0 comments** on 2026-10-06. Monitor creation defaults to every 5 minutes (`monitor/create-monitor.md`) |
| **Multi-region** | Cloud probes in several regions; self-hosted can run custom probes (`probe/custom-probe.md`) |
| **On-call** | Schedules, escalation policies, SMS / call / push / email / Slack |
| **Status pages** | Public and private, subscribers, scheduled maintenance |
| **Automation** | REST API, CLI, Terraform/OpenTofu provider, MCP server, no-code workflows |

## Where OneUptime is ahead

- **Scope.** Logs, traces, metrics, APM, error tracking, RUM, profiles and
  infrastructure agents (Kubernetes, Docker, Proxmox, VMware, Ceph, network
  devices). SolidPing has none of these beyond up/down checks.
- **Synthetic Playwright monitors** and an **external status page** monitor
  (watch a vendor's own status page).
- **Incident management depth**: post-mortems, runbooks, workflows into Jira,
  GitHub, Teams and "5,000+ apps".
- **Terraform provider** and a native mobile app.
- **SAML and OIDC SSO in the free Community Edition.**
- **Hosted SMS, WhatsApp and voice** sold at $0.10, so a cloud user does not need
  a Twilio account.
- Large, fast-shipping team; daily releases.

## Where SolidPing is ahead

- **Footprint.** One Go binary on SQLite runs on a small VPS. OneUptime needs
  PostgreSQL + ClickHouse + Valkey and recommends 16 GB RAM / 8 cores for Compose.
- **Self-hosted sub-minute checks.** SolidPing's floor is 10 s
  (`GlobalMinPeriod`) on both self-hosted and cloud. OneUptime self-hosted stops at
  1 minute (#2937).
- **Protocol coverage outside HTTP.** SolidPing has dedicated checks for UDP,
  SMTP, IMAP, POP3, SSH, FTP/SFTP, WebSocket, gRPC, SIP, NTP, RDP, VNC, MQTT and
  game servers. None of these has its own monitor page in OneUptime's docs (its
  port and ping monitors cover reachability).
- **Pricing shape on the cloud.** SolidPing Cloud is flat per organisation
  (free for 100 checks, then €5 / €15 / €45). OneUptime cloud is per user plus
  $1 per active monitor, so 100 monitors and 3 users on Growth is roughly
  $66 + $100 a month.

## Migration notes

SolidPing has **no OneUptime importer**. Moving means recreating checks, by hand
or through the SolidPing API / YAML apply. Do not claim one on the site.

## Positioning takeaway

Do not compete on breadth; OneUptime wins it. The honest pitch is weight and
self-hosted parity: same core uptime + status page + on-call, one binary, 10-second
checks self-hosted. Users who want logs and traces in the same tool should pick
OneUptime. Corrects the 2026-07-12 `indie-watch.md` line "Growth $22/mo": the
pricing page says **per user**.

## Sources

- https://github.com/OneUptime/oneuptime (README, LICENSE, `gh api` 2026-10-06)
- `packages/App/FeatureSet/Docs/Content/en/installation/sizing.md`
- `packages/App/FeatureSet/Docs/Content/en/installation/docker-compose.md`
- `packages/App/FeatureSet/Docs/Content/en/monitor/` (directory listing, `create-monitor.md`)
- https://oneuptime.com/pricing (fetched 2026-10-06; per-user label is in the page script)
- https://github.com/OneUptime/oneuptime/issues/2937
