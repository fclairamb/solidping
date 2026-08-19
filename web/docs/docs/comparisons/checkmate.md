---
sidebar_position: 1
title: SolidPing vs Checkmate
description: An honest comparison with Checkmate — stacks, check types, distribution models, and what to expect if you switch.
---

# SolidPing vs Checkmate

[Checkmate](https://github.com/bluewave-labs/checkmate) (by Bluewave Labs) and
SolidPing overlap a lot on paper: both are self-hosted, AGPL-3.0-licensed
monitoring platforms with status pages, incident tracking, maintenance windows
and a wide set of notification channels. The differences are in what each one
is built around — Checkmate grew out of *server and infrastructure* monitoring,
SolidPing out of *protocol-deep, multi-region* service monitoring.

:::info Comparisons go stale
Both projects move quickly. This page describes Checkmate as of mid-2026 —
verify anything load-bearing against [their documentation](https://docs.checkmate.so/)
before deciding.
:::

## At a glance

| | SolidPing | Checkmate |
|---|---|---|
| License | AGPL-3.0 | AGPL-3.0 |
| Runtime | Single Go binary, dashboard and docs embedded | Node.js backend + React frontend |
| Database | SQLite or PostgreSQL | MongoDB |
| Check types | 40+ — see [check types](../features/check-types.md) | ~12 (HTTP, ping, TCP/port, DNS, gRPC, WebSocket, SSL, Docker, page speed, game server, JSON query, hardware) |
| Multi-region probing | First-party [regions and workers](../features/private-locations.md) you deploy and control | Via the public [GlobalPing](https://globalping.io/) probe network (HTTP, ping, TCP) |
| Private networks | Org-scoped [deported agents](../features/private-locations.md), outbound-only WebSocket | Capture agent reports hardware metrics outbound |
| Hardware metrics (CPU/RAM/disk) | No dedicated agent — see [the workaround below](#hardware-monitoring) | Yes — the Capture agent (Go) |
| Page speed / Lighthouse scores | No | Yes |
| On-call schedules & escalation | Yes — [on-call](../features/on-call.md) | No |
| Heartbeat (push) checks | Yes | No |
| Status pages | Yes, with [custom domains](../features/custom-domains.md) | Yes, themed |
| Maintenance windows | [Yes](../features/maintenance-windows.md) | Yes |
| Incident workflow | Auto open/close, acknowledge and comment from Slack/Telegram, escalation policies | Auto open/close, incident tracking |
| Automation surface | Full [OpenAPI spec](../api/solidping-api.info.mdx), generated clients, `sp` CLI, [MCP server](../features/mcp.md) | REST API |
| Team roles | Yes | Yes (Admin / Editor / Viewer) |
| UI languages | English | 16 languages |

Notification channels are near-parity — both cover Slack, Discord, Telegram,
Microsoft Teams, Matrix, ntfy, Pushover, Twilio SMS, PagerDuty, email and
webhooks. SolidPing adds WhatsApp, Google Chat, Mattermost, web push and Twilio
voice calls, plus interactive acknowledge/comment from Slack and Telegram;
Checkmate adds Rocket.Chat.

## Check type mapping

If you are recreating Checkmate monitors in SolidPing, they map like this:

| Checkmate monitor | SolidPing |
|---|---|
| Uptime (HTTP) | `http` |
| JSON query | `http` with a JSONPath assertion |
| Ping | `icmp` |
| Port / TCP | `tcp` |
| DNS | `dns` |
| gRPC | `grpc` |
| WebSocket | `websocket` |
| SSL | `ssl` |
| Docker | `docker` |
| Game server | `a2s` (Source engine) or `minecraft` |
| Page speed | No equivalent — a [`browser` check](../features/check-types.md) measures real load timing, but does not compute Lighthouse scores |
| Infrastructure (Capture agent) | No direct equivalent — see below |

### Hardware monitoring

SolidPing deliberately has no hardware-metrics agent: CPU, RAM and disk are
Prometheus's home turf. The closest SolidPing pattern is a `prometheus` check
pointed at a [node_exporter](https://github.com/prometheus/node_exporter)
endpoint with a threshold assertion — it alerts through the same incident
pipeline as every other check, but it will not give you Checkmate's per-server
metric dashboards. If agent-based server dashboards are the core of your need,
Checkmate is genuinely the better fit.

## Where each one goes further

**Checkmate is ahead on:**

- **Infrastructure monitoring** — the Capture agent reports CPU, memory, disk,
  network and temperature from Linux, Windows, macOS and Raspberry Pi hosts.
- **Page speed** — Lighthouse-style scoring over time, not just availability.
- **Localization** — the UI ships in 16 languages.

**SolidPing goes further on:**

- **Protocol depth** — beyond the shared basics: mail round-trip (`email`,
  `smtp`, `imap`, `pop3`), message queues (`kafka`, `rabbitmq`, `mqtt`),
  databases (`postgresql`, `mysql`, `mssql`, `oracle`, `mongodb`, `redis`,
  `clickhouse`), plus `ssh`, `sftp`, `ftp`, `snmp`, `ntp`, `sip`, `rdp`,
  `kubernetes`, `dnsbl`, `domain` (WHOIS expiry), scripted `browser` and `js`
  checks, and [heartbeats](../features/check-types.md) for cron jobs.
- **Distribution you control** — Checkmate's global checks ride the public
  GlobalPing probe network and cover HTTP, ping and TCP; SolidPing's
  [regions](../features/private-locations.md) are first-party workers running
  *any* check type, and org-scoped agents extend that into private networks
  with an outbound-only connection.
- **Paging the right person** — [on-call schedules and escalation
  policies](../features/on-call.md) are built in, rather than delegated to
  PagerDuty.
- **Automation** — the full product is driven by one
  [OpenAPI-specified API](../api/solidping-api.info.mdx), with a CLI and an
  [MCP server](../features/mcp.md) for AI-assisted operations.
- **Operational footprint** — one binary, SQLite for small installs, PostgreSQL
  when you outgrow it; no MongoDB to run.

## Migrating from Checkmate

There is no Checkmate importer yet — unlike
[Uptime Kuma](../features/migrate-from-uptime-kuma.md),
[Gatus](../features/migrate-from-gatus.md) and
[Better Stack](../features/migrate-from-better-stack.md), Checkmate has no
config-file or API export the importer could consume today. Recreate monitors
in the dashboard using the mapping table above, or script the creation through
the API — bulk check creation is a short loop over
[`POST /api/v1/orgs/{org}/checks`](../api/create-check.api.mdx).

If a Checkmate importer would unblock your migration,
[open an issue](https://github.com/fclairamb/solidping/issues) — the import
framework the other three sources share makes adding one straightforward.
