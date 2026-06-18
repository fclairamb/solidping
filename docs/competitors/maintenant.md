# Maintenant - Complete Analysis

## Overview

Maintenant is a unified, self-hosted **open-source supervision tool** created by
Benjamin Touchard (**kOlapsis**), a French independent developer. It consolidates
monitoring functions that usually require several separate tools — container
monitoring, HTTP/TCP endpoint checks, SSL/TLS certificate tracking, and
scheduled-task (heartbeat/cron) monitoring — into a single Go binary shipped as
one Docker container.

It is one of SolidPing's **closest direct competitors**: same self-hosted +
single-binary + Go stack, same "all-in-one observability without external
dependencies" pitch, and — notably — it also ships an **MCP server for AI
assistant integration**, a feature SolidPing shares and almost no other
competitor offers.

**Website**: https://maintenant.dev

**Repository**: https://github.com/kOlapsis/maintenant

**License**: AGPL-3.0

**Technology**: Go

**Database**: SQLite (no external dependencies — no Redis, no PostgreSQL)

**Editions**: Community (free, feature-limited) and Pro (€29/month or €290/year)

**Source for this analysis**: [IT-Connect — "Maintenant : la supervision open source"](https://www.it-connect.fr/maintenant-supervision-open-source/)

## At a Glance

| Attribute | Value |
|-----------|-------|
| **Developer** | Benjamin Touchard (kOlapsis), independent (France) |
| **License** | AGPL-3.0 |
| **Deployment** | Single Docker container (amd64 + arm64) |
| **Language / DB** | Go / SQLite |
| **Resource usage** | < 30 MB RAM |
| **Pricing** | Community (free) / Pro (€29/mo or €290/yr) |
| **Philosophy** | Read-only — observes containers, never controls them |
| **Standout feature** | MCP server for AI assistants (Claude Desktop compatible) |

## Technical Architecture

- **Language**: Go
- **Database**: SQLite — deliberately **no external dependencies** (no Redis,
  no PostgreSQL). This is the same "zero-dependency single binary" positioning
  Uptime Kuma and Gatus use, and contrasts with SolidPing's dual
  PostgreSQL/SQLite backend.
- **Resource footprint**: under **30 MB RAM** — extremely lightweight.
- **Deployment**: a **single Docker container**.
- **Platforms**: `amd64` and `arm64` architectures.
- **Read-only philosophy**: Maintenant *observes* Docker/Kubernetes containers
  but does **not** control them (no start/stop/restart actions), which it frames
  as a security feature. Docker group permission handling is documented for
  least-privilege access to the socket.

## Key Features

### Container Management

This is Maintenant's headline differentiator — none of SolidPing's other
surveyed competitors lead with full container observability:

- **Automatic discovery** of Docker and Kubernetes containers
- **Real-time state and resource monitoring**: CPU, memory, network I/O, disk
- **Live log streaming** with search / regex filtering
- **Health-check tracking** (Docker `HEALTHCHECK` status)
- **Restart-loop detection**
- **Docker Compose project grouping**
- **Image update detection** — flags when a newer image is available
- **Configuration via Docker labels**

### Endpoint Monitoring (HTTP / TCP)

- HTTP and TCP probe configuration
- Response-time measurement and **status-code validation**
- Configurable **check intervals** and **failure thresholds**
- **Custom headers** and expected status codes

### SSL/TLS Monitoring

- **Automatic certificate detection** on HTTPS endpoints
- **Expiration alerts** at 30, 14, 7, 3, and 1-day thresholds
- **Complete certificate-chain validation**

### Heartbeat / Cron Monitoring

- Ping-based monitoring for **scheduled tasks**
- Custom URLs for task-execution confirmation
- Alerts for **missed job-execution windows**
- Activity-log tracking

### Alerting

- **Community edition**: Webhooks and Discord
- **Pro edition**: Slack, Microsoft Teams, email (SMTP)
- **Severity-based filtering**
- **Maintenance windows** (suppress alerts during planned work)
- **Incident escalation** — Pro only

### Status Pages

- Public status page with **real-time updates**
- **Community**: up to **3 components**, 1 status page
- **Pro**: incident timeline, maintenance scheduling, **subscriber notifications**

### API & Integrations

- **REST API** under `/api/v1/`
- **MCP server** for AI-assistant integration (Claude Desktop compatible),
  with **OAuth2 authentication** for the MCP endpoint
- Configuration via **Docker labels**

### Configuration & Operations

- **Telemetry** is opt-out (anonymized usage data)
- Custom **organization name**
- **Base URL** configuration for public access
- **SMTP** settings (Pro only)
- Docker group permission handling for least-privilege socket access

## Editions & Pricing

| Capability | Community (free) | Pro (€29/mo · €290/yr) |
|------------|------------------|------------------------|
| **Endpoints (HTTP/TCP)** | 10 | Higher / unlocked |
| **Heartbeats** | 5 | Higher / unlocked |
| **Certificates** | 5 | Higher / unlocked |
| **Status pages** | 1 (max 3 components) | Incident timeline, maintenance scheduling, subscriber notifications |
| **Alert channels** | Webhooks, Discord | + Slack, Microsoft Teams, email |
| **Incident escalation** | ❌ | ✅ |
| **SMTP / email** | ❌ | ✅ |
| **Container monitoring** | ✅ | ✅ |
| **REST API + MCP** | ✅ | ✅ |

**Community edition limits**: 10 endpoints, 5 heartbeats, 5 certificates, 1 status
page with up to 3 components.

## Comparison with SolidPing

### Similarities (this is the closest analogue to SolidPing surveyed)

Both are:

- **Self-hosted, open-source** monitoring tools
- Written in **Go**
- Shipped as a **single binary / single Docker container**
- Able to run on **SQLite** with no external dependencies (SolidPing also
  supports SQLite as one of its two backends)
- HTTP/TCP endpoint monitoring with status-code + response-time validation
- **SSL/TLS certificate expiration** monitoring
- **Heartbeat / cron** monitoring for scheduled jobs
- **Public status pages** with maintenance windows
- A **REST API** (`/api/v1/`)
- An **MCP server for AI/LLM integration** — a rare overlap; almost no other
  competitor ships this

### Maintenant Advantages

1. **Container-native observability**: automatic Docker/Kubernetes discovery,
   live resource metrics (CPU/mem/net/disk), live log streaming with regex,
   restart-loop detection, Compose grouping, and image-update detection. This is
   far deeper than SolidPing's Docker *container check* (which probes container
   state, not full resource/log observability).
2. **Tiny footprint**: < 30 MB RAM.
3. **Docker-label configuration**: declarative config straight from container
   labels (a lightweight config-as-code path SolidPing lacks).
4. **OAuth2 on the MCP endpoint** out of the box.

### SolidPing Advantages

1. **Far broader protocol coverage** — 32 check types vs Maintenant's four core
   surfaces (HTTP, TCP, SSL, heartbeat). SolidPing adds Ping/ICMP, UDP, DNS,
   SMTP/POP3/IMAP, SSH, FTP/SFTP, WebSocket, gRPC, databases (6 engines), message
   queues (Kafka/RabbitMQ/MQTT), SNMP, game servers, JMAP inbox, browser (Rod),
   and sandboxed JS checks.
2. **Dual PostgreSQL / SQLite backend** with horizontal scaling via distributed
   workers — Maintenant is SQLite-only and single-instance.
3. **Multi-tenancy + RBAC** (org-scoped isolation, admin/user/viewer roles).
4. **All notification channels in the free/open tier** — SolidPing ships 9 native
   channels (Slack, Discord, Email, Webhooks, Google Chat, Mattermost, Ntfy,
   Opsgenie, Pushover) without paywalling Slack/Teams/email behind a Pro plan.
5. **Richer incident management**: adaptive resolution, group-incident
   correlation, ack/snooze/manual-resolve, on-call schedules, multi-step
   escalation policies — all in the self-hosted product, not a paid tier.
6. **Credentials encryption at rest** (envelope encryption with out-of-band
   master key).
7. **No feature paywall on core monitoring** — Maintenant's Community edition caps
   endpoints (10), heartbeats (5), certificates (5), and gates escalation,
   Slack/Teams/email, and most status-page features behind Pro.

### Licensing Contrast

Maintenant is **AGPL-3.0** with a Community/Pro **open-core** model (limits +
paid features). This is a different posture from permissively-licensed OSS
competitors (Uptime Kuma is MIT, Gatus is Apache-2.0) and worth noting when
positioning SolidPing's own OSS-vs-paid code separation (see
`specs/questions/2026-05-03-oss-vs-paid-code-separation.md`).

## Strengths

1. ✅ **Container observability done well** — discovery, live metrics, log
   streaming, restart-loop and image-update detection
2. ✅ **Single Go binary, < 30 MB RAM** — trivial to deploy, tiny footprint
3. ✅ **Zero external dependencies** (SQLite only)
4. ✅ **MCP server + OAuth2** for AI-assistant integration
5. ✅ **SSL chain validation** with graduated expiry alerts (30/14/7/3/1 day)
6. ✅ **Docker-label configuration** (lightweight config-as-code)
7. ✅ **Clear read-only security model** for the Docker socket

## Weaknesses (relative to SolidPing)

1. ❌ **Narrow protocol coverage** — only HTTP, TCP, SSL, and heartbeat checks
   (no ICMP/DNS/mail/DB/queue/etc.)
2. ❌ **SQLite-only**, single-instance — no PostgreSQL, no horizontal scaling
3. ❌ **No multi-tenancy / RBAC** evident
4. ❌ **Key features gated behind Pro** — escalation, Slack/Teams/email,
   subscriber notifications, and most status-page capabilities
5. ❌ **Tight Community-edition caps** (10 endpoints / 5 heartbeats / 5 certs)
6. ⚠️ **No multi-region / distributed checking** — single host only

## Use Cases

**Best for**:
- Docker / Docker Compose / Kubernetes operators who want **container health +
  endpoint + cert + cron monitoring in one tiny container**
- Homelab and small-team setups prioritizing a minimal footprint
- Teams that want **AI-assistant (MCP) access** to their monitoring data

**Not ideal for**:
- Broad multi-protocol monitoring (DNS, mail, DB, queues, SNMP, game servers)
- Multi-tenant / RBAC deployments
- High-scale or multi-region monitoring needing distributed workers
- Teams wanting Slack/Teams/email alerting without paying for Pro

## Takeaways for SolidPing

Maintenant validates several of SolidPing's bets (Go, single-binary,
self-hosted, MCP for AI) while pointing at two areas worth watching:

1. **Container observability depth** — Maintenant's live resource metrics, log
   streaming, restart-loop detection, and image-update detection go well beyond
   SolidPing's container *check*. If container monitoring becomes a priority,
   this is the reference implementation to study.
2. **Docker-label configuration** — a low-friction config-as-code path that
   complements (but doesn't replace) a full YAML/Terraform story.

Conversely, SolidPing's protocol breadth (32 types), dual-DB + distributed
workers, multi-tenancy/RBAC, and **un-paywalled** alerting/incident features are
clear differentiators against Maintenant's open-core, SQLite-only, four-protocol
design.

## Sources

- [IT-Connect — "Maintenant : la supervision open source"](https://www.it-connect.fr/maintenant-supervision-open-source/) (primary source for this analysis)
- [Maintenant website](https://maintenant.dev)
- [Maintenant repository (GitHub/kOlapsis)](https://github.com/kOlapsis/maintenant)
