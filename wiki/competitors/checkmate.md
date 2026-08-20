# Checkmate — Analysis (vs SolidPing)

*Researched 2026-08. Checkmate moves fast — re-verify against https://docs.checkmate.so/ before quoting.*

## Overview

Checkmate (by Bluewave Labs) is an open-source, self-hosted monitoring platform
that grew out of *server and infrastructure* monitoring, where SolidPing grew out
of *protocol-deep, multi-region* service monitoring. On paper the overlap is
large: both are AGPL-3.0, both ship status pages, incident tracking, maintenance
windows and a wide set of notification channels.

**Website**: https://checkmate.so
**GitHub**: https://github.com/bluewave-labs/checkmate (~10k stars, 90+ contributors)
**License**: AGPL-3.0
**Stack**: Node.js backend + React (MUI) frontend, MongoDB; optional Go "Capture" agent

## At a glance

| | SolidPing | Checkmate |
|---|---|---|
| License | AGPL-3.0 | AGPL-3.0 |
| Runtime | Single Go binary, dashboard and docs embedded | Node.js backend + React frontend |
| Database | SQLite or PostgreSQL | MongoDB |
| Check types | 40+ | ~12 (HTTP, ping, TCP/port, DNS, gRPC, WebSocket, SSL, Docker, page speed, game server, JSON query, hardware) |
| Multi-region probing | First-party regions/workers you deploy and control | Via the public [GlobalPing](https://globalping.io/) probe network (HTTP, ping, TCP only) |
| Private networks | Org-scoped deported agents, outbound-only WebSocket, any check type | Capture agent reports hardware metrics outbound |
| Hardware metrics (CPU/RAM/disk) | No dedicated agent (nearest: `prometheus` check on node_exporter) | Yes — the Capture agent (Go; Linux, Windows, macOS, RPi) |
| Page speed / Lighthouse scores | No (browser check measures load timing, not scores) | Yes |
| On-call schedules & escalation | Yes, built in | No |
| Heartbeat (push) checks | Yes | No |
| Status pages | Yes, with custom domains | Yes, themed |
| Incident workflow | Auto open/close, ack/comment from Slack & Telegram, escalation policies | Auto open/close, incident tracking |
| Automation surface | Full OpenAPI spec, generated clients, `sp` CLI, MCP server | REST API ("for bulk setup the API accepts JSON") |
| Team roles | Yes | Yes (Admin / Editor / Viewer) |
| UI languages | English | 16 languages |

Notification channels are near-parity — both cover Slack, Discord, Telegram,
Microsoft Teams, Matrix, ntfy, Pushover, Twilio SMS, PagerDuty, email and
webhooks. SolidPing adds WhatsApp, Google Chat, Mattermost, web push and Twilio
voice, plus interactive ack/comment from Slack and Telegram; Checkmate adds
Rocket.Chat.

## Check type mapping

For recreating Checkmate monitors as SolidPing checks (support/migration aid):

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
| Game server | `a2s` or `minecraft` |
| Page speed | No equivalent |
| Infrastructure (Capture agent) | No direct equivalent — `prometheus` check on a node_exporter endpoint approximates the alerting, not the dashboards |

## Where Checkmate is ahead

- **Infrastructure monitoring** — the Capture agent reports CPU, memory, disk,
  network and temperature; per-server metric dashboards are a core feature.
  This is their identity, not a bolt-on.
- **Page speed** — Lighthouse-style scoring over time.
- **Localization** — UI in 16 languages.
- **Community size** — large star count and contributor base; active Discord.

## Where SolidPing is ahead

- **Protocol depth** — mail round-trip, message queues (Kafka/RabbitMQ/MQTT),
  databases (PG/MySQL/MSSQL/Oracle/Mongo/Redis/ClickHouse), SSH/SFTP/FTP, SNMP,
  NTP, SIP, RDP, Kubernetes, DNSBL, WHOIS/domain expiry, scripted browser and
  JS checks, heartbeats.
- **Distribution you control** — Checkmate's global checks ride the public
  GlobalPing probe network and only cover HTTP/ping/TCP; SolidPing regions are
  first-party workers running any check type, extended into private networks by
  org-scoped outbound-only agents.
- **Paging** — on-call schedules and escalation policies built in rather than
  delegated to PagerDuty.
- **Automation** — OpenAPI-specified API, CLI, MCP server.
- **Operational footprint** — one binary, SQLite→PostgreSQL; no MongoDB.

## Migration notes

No Checkmate importer exists (unlike Uptime Kuma, Gatus and Better Stack — see
the published migration guides in `web/docs/docs/features/migrate-from-*.md`).
Checkmate has no config-file export the import framework could consume; adding
an importer would mean reading their Mongo collections or their REST API. Until
someone asks, manual recreation via the mapping table above (or a short loop
over `POST /api/v1/orgs/{org}/checks`) is the answer.

## Positioning takeaway

Checkmate wins evaluations where "is my server's disk filling up" is the core
question and a hardware agent plus uptime checks in one tool is the draw.
SolidPing wins where the question is "is my whole stack reachable from
everywhere, and who gets paged" — protocol breadth, controlled multi-region,
on-call. Don't pitch against their agent; pitch the check types and the
distribution model.

## Sources

- https://github.com/bluewave-labs/checkmate (README, releases)
- https://checkmate.so/ (feature marketing, 2026-08)
- https://docs.checkmate.so/
