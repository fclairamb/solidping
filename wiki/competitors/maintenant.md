# Maintenant — Complete Analysis

## Overview

Maintenant is a unified, self-hosted **infrastructure-monitoring tool** built by
Benjamin Touchard (**kOlapsis**), an independent developer in Bordeaux, France.
Its pitch — *"Monitor everything. Manage nothing."* — is to collapse three to
five separate tools (a Uptime-Kuma-style prober, a Dozzle-style log viewer, a
Grafana-style resource dashboard, a Healthchecks-style cron monitor, a cert
tracker) into **one Go binary in one container** with zero configuration.

It is one of SolidPing's **closest direct competitors**: same self-hosted +
single-binary + Go stack, same SQLite-only "no external dependencies" posture,
same "all-in-one observability" framing, and — notably — it also ships an **MCP
server for AI-assistant integration**, an overlap almost no other competitor
shares. Where it diverges sharply is focus: Maintenant leads with **container
observability** (Docker/Kubernetes discovery, live metrics, logs, restart-loop
and image-update detection, network-security insights), whereas SolidPing leads
with **protocol breadth** (30+ check types) and **multi-tenant, multi-worker
operations**.

This analysis was re-researched against primary sources (June 2026): the
official site, the documentation site, the GitHub repository, and the original
IT-Connect article, cross-checked against community discussion on LinuxFr. It
corrects several claims from the earlier single-source writeup — see
[Corrections from the prior analysis](#corrections-from-the-prior-analysis).

- **Website**: https://maintenant.dev · **Docs**: https://docs.maintenant.dev
- **Repository**: https://github.com/kOlapsis/maintenant
- **License**: AGPL-3.0 (or a separate commercial license)

## At a Glance

| Attribute | Value |
|-----------|-------|
| **Developer** | Benjamin Touchard (kOlapsis), independent — Bordeaux, France; bootstrapped, no VC |
| **License** | AGPL-3.0 **or** commercial; Community/Pro open-core |
| **Deployment** | Single Docker container; also Kubernetes, systemd, or standalone (`amd64` + `arm64`) |
| **Language / DB** | Go (binary) + embedded Vue 3 frontend / SQLite (WAL, single-writer) |
| **Resource usage** | **~17 MB RAM** (project's own figure; the IT-Connect article rounds to "< 30 MB") |
| **Auth** | **No built-in authentication** — designed to run behind a reverse proxy (e.g. Traefik + Authelia) |
| **Pricing** | Community (free) · Pro (€29/mo or €290/yr, 14-day trial) · Enterprise (custom) |
| **Philosophy** | Read-only — *"observe, never act"*; Docker socket mounted read-only |
| **Standout features** | Deep container observability · MCP server (OAuth2) · network-security insights |
| **Maturity** | v1.3.2 (2026-06-17), 24 releases, ~367★ — young but shipping fast |

## Project maturity & cadence

Maintenant is a **young, single-maintainer, fast-moving** project. As of June
2026: **~367 GitHub stars**, ~16 forks, ~24 tagged releases, ~334 commits on
`main`, and a latest release (**v1.3.2**, "Endpoint monitors detail panel") cut
on **2026-06-17** — five days before this analysis. The codebase is **Go ~67% /
Vue ~24% / TypeScript ~9%**. Treat feature details as a fast-moving target:
verify against the docs before relying on any single capability.

## Technical Architecture

- **Single Go binary** with the Vue 3 + TypeScript + Tailwind frontend embedded
  via `embed.FS`. Real-time UI over **SSE**, charts via **uPlot**, PWA support.
- **SQLite only** (WAL mode, single-writer) — deliberately **no external
  dependencies**: no Redis, no PostgreSQL, no message queue. Default DB path
  `/data/maintenant.db`. This matches the "zero-dependency single binary"
  posture of Uptime Kuma and Gatus, and contrasts with SolidPing's dual
  PostgreSQL/SQLite backend.
- **Resource footprint**: ~17 MB RAM — extremely lightweight.
- **Three runtime modes** (`MAINTENANT_RUNTIME` auto-detects): **Docker**
  (native socket), **Kubernetes** (in-cluster API, read-only RBAC), or
  **standalone** (no container runtime required — it can run purely as an
  endpoint/cert/heartbeat prober).
- **Read-only philosophy**: it *observes* containers but never controls them (no
  start/stop/restart). The Docker socket is mounted **read-only**, and the
  recommended compose run sets `read_only: true` and
  `security_opt: no-new-privileges:true`. This is framed as a security feature.

## Security & Authentication model

> **Important correction.** Maintenant has **no built-in authentication or user
> model at all** — by design. The UI and API are expected to sit behind a
> reverse proxy that provides auth (the docs cite Traefik + Authelia). The MCP
> endpoint's OAuth2 is the *only* first-party auth surface, and it protects the
> AI-assistant integration, **not** the dashboard.

Consequences:

- **No users, no roles, no RBAC, no multi-tenancy** in the product itself. A
  single deployment is single-organization, and anyone who can reach the port
  has full read access unless a proxy gates it.
- On Kubernetes, the agent uses a **cluster-wide read-only role**. A community
  reviewer flagged this as over-broad; the author acknowledged it as a
  deliberate **simplicity-over-isolation tradeoff**, "unsuitable for multi-tenant
  enterprise environments." Namespace scoping is available via
  `MAINTENANT_K8S_NAMESPACES` / `MAINTENANT_K8S_EXCLUDE_NAMESPACES`.
- **Public unauthenticated endpoints by design**: `/ping/{uuid}` (heartbeat
  ingest) and `/status/` (public status page).
- **Multi-host agents** authenticate to the central server with a **zero-PKI**
  scheme: Ed25519 keypairs, challenge-response, one-time enrollment tokens,
  revocable from the UI (see [Multi-host monitoring](#multi-host-monitoring-pro)).

This is the single biggest gap versus SolidPing, which ships built-in auth,
org-scoped multi-tenancy, and admin/user/viewer RBAC out of the box.

## Key Features

### Container observability (the headline)

None of SolidPing's other surveyed competitors lead with full container
observability — this is Maintenant's differentiator:

- **Zero-config auto-discovery** of Docker and Kubernetes containers.
- **State, health-check, and restart-loop detection** (Docker `HEALTHCHECK`
  status; flagged restart loops).
- **Live log streaming** with stdout/stderr demultiplexing and search/regex.
- **Docker Compose project auto-grouping**.
- **Kubernetes workloads**: Deployments, DaemonSets, StatefulSets, via the
  in-cluster API with read-only RBAC and namespace allow/deny lists.

### Resource metrics

- **Real-time CPU, memory, network I/O, disk I/O per container**.
- **Historical charts** (1 hour to 30 days — the long retention is **Pro**).
- **Per-container alert thresholds** and a **"top consumers"** view.
- In multi-host deployments, **per-host** resource metrics with a host selector.

### Endpoint monitoring (HTTP / TCP)

- HTTP and TCP probes, **configured declaratively via Docker labels**
  (`maintenant.endpoint.http` / `.tcp`, indexed for multiple endpoints) — also
  manageable from the UI/API.
- Response-time tracking with **90-day sparklines**, status-code validation.
- Tunable `interval`, `timeout`, `failure-threshold`, `recovery-threshold`.
- Community cap: **10 endpoints**; Pro: unlimited.

### Heartbeat / cron monitoring

- Unique **ping URL** per job (`/ping/{uuid}`) for scheduled-task confirmation.
- Tracks **start/finish time, duration, and exit code** — richer than a bare
  "did it ping" beat — and fires on **missed deadlines**.
- Community cap: **5 heartbeats**; Pro: unlimited.

### SSL/TLS monitoring

- **Auto-detection** of certificates on HTTPS endpoints, plus standalone domain
  cert monitors.
- **Graduated expiry alerts at 30 / 14 / 7 / 3 / 1 day**.
- **Full certificate-chain validation** (`chain_invalid` event).
- Community cap: **5 certificates**; Pro: unlimited.

### Network-security insights (new vs prior analysis)

A whole feature category the earlier writeup missed:

- **Dangerous-configuration detection**: `0.0.0.0` bindings, exposed database
  ports, host-network mode, privileged containers.
- **Kubernetes-specific risks**: `NodePort` / `LoadBalancer` exposure without a
  `NetworkPolicy`.
- **OCI manifest inspection** for CVE context. Pro layers on a **unified
  security-posture dashboard**, **CVE enrichment, and risk scoring**.

### Update intelligence

- **Image-update detection** via **OCI registry scanning with digest
  comparison** (flags when a newer image is available).
- **Compose-aware update/rollback commands** to act on findings (suggested
  commands — Maintenant itself stays read-only).

## Alerting

A single alert engine spans every source above, with **severity-based filtering**
and **exponential-backoff retry**. Default event/severity mapping:

| Source | Events | Default severity |
|--------|--------|------------------|
| Container | `restart_loop`, `health_unhealthy` | Warning |
| Endpoint | `consecutive_failure` | Critical |
| Heartbeat | `deadline_missed` | Critical |
| Certificate | `expiring`, `expired`, `chain_invalid` | Critical |
| Resource | `cpu_threshold`, `memory_threshold` | Warning |
| Update | `available` | Info |

Channels and routing:

- **Community**: Webhooks and Discord; **silence rules** for maintenance.
- **Pro**: adds **Slack, Microsoft Teams, email (SMTP)**, plus **escalation
  policies**, **maintenance windows**, and **advanced trigger filters**
  (scope/tag-based, multi-level routing).

## Status pages

- Public status page with **live SSE updates**.
- **Community**: 1 page, up to **3 components**.
- **Pro**: **unlimited components**, **incident timelines**, and **subscriber
  notifications** (email/webhook).

## Multi-host monitoring (Pro)

> **Correction.** The prior analysis called Maintenant "single host only." That
> is no longer accurate: **Pro ships agent-based multi-host monitoring.**

- A **central server** plus **lightweight agents** deployed on remote hosts.
- Agents stream container state, endpoint/cert results, and per-host resource
  metrics over **persistent, mutually-authenticated gRPC**.
- **Zero-PKI security**: Ed25519 keypairs, one-time enrollment tokens,
  challenge-response, UI-revocable. **No shared database or message queue
  required.**

Caveat on scope: this is **fan-in remote collection across hosts**, not the same
thing as SolidPing's distributed workers. The core remains a **single SQLite
instance with no HA / no horizontal DB scaling**, and agents are oriented to
per-host infrastructure observability rather than multi-region synthetic
probing. It does, however, mean Maintenant can monitor a fleet from one pane.

## REST API & MCP server

- **REST API** under `/api/v1/`: containers, endpoints, heartbeats,
  certificates, resources, agents, alerts, webhooks, status page, updates,
  security, events (SSE), health.
- **MCP server** for AI-assistant integration (Claude-compatible), over both
  **stdio** and **Streamable HTTP** transports, with **OAuth2** for remote
  clients. Enabled via `MAINTENANT_MCP=true`. This directly inspired SolidPing's
  own MCP OAuth2 work (`specs/done/2026/06/2026-06-20-03-mcp-oauth2-authorization.md`).

## Configuration, telemetry & ops

- **Configuration** by environment variables (`MAINTENANT_ADDR`,
  `MAINTENANT_DB`, `MAINTENANT_BASE_URL`, `MAINTENANT_RUNTIME`,
  `MAINTENANT_LICENSE_KEY`, `MAINTENANT_MCP`, …) **and** Docker labels
  (`maintenant.endpoint.*`, `maintenant.tls.certificates`, `maintenant.ignore`,
  `maintenant.group`, `maintenant.alert.severity`). The Docker-label path is a
  lightweight config-as-code story SolidPing only partly matches.
- **Telemetry** is **opt-out** (`MAINTENANT_DISABLE_TELEMETRY=1`). It sends
  **anonymized hourly snapshots** to `metrics.kolapsis.com` via the author's
  separate [`shm`](https://github.com/kOlapsis/shm) project — counts and system
  metadata only (edition, totals, OS/arch/Go version, memory, an install ID).
  It explicitly does **not** collect hostnames, IPs, container names, endpoint
  URLs, credentials, or webhook targets.

## Editions & Pricing

| Capability | Community (free, AGPL) | Pro (€29/mo · €290/yr) | Enterprise |
|------------|------------------------|------------------------|------------|
| Endpoints (HTTP/TCP) | 10 | Unlimited | Unlimited |
| Heartbeats | 5 | Unlimited | Unlimited |
| Certificates | 5 | Unlimited | Unlimited |
| Status-page components | 3 (1 page) | Unlimited + incidents + subscribers | + |
| Container/resource/security/update monitoring | ✅ | ✅ (+ CVE enrichment, risk scoring) | ✅ |
| Alert channels | Webhook, Discord | + Slack, Teams, Email | + |
| Escalation / maintenance windows | ❌ | ✅ | ✅ |
| Multi-host (agents) | ❌ | ✅ | ✅ |
| Incident mgmt + subscriber notifications | ❌ | ✅ | ✅ |
| Metric retention | short | up to 30 days | + |
| SSO / audit logs / on-prem contract | ❌ | ❌ | ✅ (custom) |
| REST API + SSE + MCP | ✅ | ✅ | ✅ |
| Support | community | priority email | dedicated |

Notes:

- **14-day Pro trial**; billing via **Mollie or Stripe**.
- **Pricing has moved**: a community comment (May 2026) notes Pro rose from an
  originally advertised **€9/mo to €29/mo** — a >3× increase, worth flagging for
  any pricing comparison that cites older figures.
- **Licensing posture**: AGPL-3.0 *or* a commercial license. Edition limits are
  **hard-coded in the source** (e.g. heartbeat caps), so the "open-core" model
  gates features in-code rather than via closed modules — see
  [Community reception](#community-reception).

## Comparison with SolidPing

### Similarities (closest analogue surveyed)

Both are **self-hosted, open-source, Go single-binary** tools that run on
**SQLite with no external dependencies** (SolidPing also supports SQLite as one
of two backends), do **HTTP/TCP** checks with status-code + response-time
validation, **SSL/TLS expiry** monitoring, **heartbeat/cron** monitoring, ship
**public status pages with maintenance windows**, expose a **REST API
(`/api/v1/`)**, and — rarely — both ship an **MCP server for AI/LLM
integration**.

### Maintenant advantages

1. **Container-native observability** — auto-discovery, live CPU/mem/net/disk
   metrics, log streaming with regex, restart-loop detection, Compose grouping,
   image-update detection. Far deeper than SolidPing's container *check*.
2. **Network-security insights** — exposed-port / privileged-container / risky
   K8s-exposure detection, with CVE enrichment in Pro. SolidPing has no
   equivalent.
3. **Update intelligence** — OCI digest comparison and compose-aware
   update/rollback suggestions.
4. **Tiny footprint** — ~17 MB RAM.
5. **Docker-label configuration** — declarative config straight from container
   labels (a low-friction config-as-code path).
6. **OAuth2 on the MCP endpoint** out of the box.

### SolidPing advantages

1. **Far broader protocol coverage** — 30+ check types vs Maintenant's four
   network/endpoint surfaces (HTTP, TCP, SSL, heartbeat). SolidPing adds
   Ping/ICMP, UDP, DNS, SMTP/POP3/IMAP, SSH, FTP/SFTP, WebSocket, gRPC,
   databases (6 engines), message queues (Kafka/RabbitMQ/MQTT), SNMP, game
   servers, JMAP inbox, browser (chromedp/CDP), and sandboxed JS checks.
2. **Built-in authentication + multi-tenancy + RBAC** — org-scoped isolation and
   admin/user/viewer roles. **Maintenant has no built-in auth at all** and
   relies on an external reverse proxy; it is single-organization with no roles.
3. **Dual PostgreSQL / SQLite backend with distributed workers** for horizontal
   scale. Maintenant's Pro multi-host agents add fleet *coverage* but the core is
   still a single SQLite instance with no HA.
4. **All notification channels in the free/open tier** — SolidPing ships 10 native
   channels (Slack, Discord, Email, Webhooks, Google Chat, Mattermost, Ntfy,
   Opsgenie, Pushover) without paywalling Slack/Teams/email behind Pro.
5. **Richer incident management in the open product** — adaptive resolution,
   group-incident correlation, ack/snooze/manual-resolve, on-call schedules,
   multi-step escalation — not gated behind a paid tier.
6. **Credentials encryption at rest** (envelope encryption with out-of-band
   master key).
7. **No feature paywall on core monitoring** — Maintenant's Community edition
   caps endpoints (10), heartbeats (5), certificates (5), status components (3),
   and gates escalation, Slack/Teams/email, multi-host, incidents, and
   subscriber notifications behind Pro.

## Strengths

1. ✅ **Container observability done well** — discovery, live metrics, log
   streaming, restart-loop and image-update detection.
2. ✅ **Network-security insights + CVE enrichment** — a genuinely differentiated
   surface.
3. ✅ **Single Go binary, ~17 MB RAM, zero external dependencies.**
4. ✅ **MCP server + OAuth2** for AI-assistant integration.
5. ✅ **SSL chain validation** with graduated expiry alerts (30/14/7/3/1 day).
6. ✅ **Docker-label configuration** (lightweight config-as-code).
7. ✅ **Clear read-only security model** for the Docker socket; fast release
   cadence.

## Weaknesses (relative to SolidPing)

1. ❌ **No built-in authentication / users / RBAC / multi-tenancy** — fully
   reliant on an external reverse proxy for access control.
2. ❌ **Narrow protocol coverage** — only HTTP, TCP, SSL, and heartbeat probes
   (no ICMP/DNS/mail/DB/queue/SNMP/etc.).
3. ❌ **SQLite-only, single core instance** — no PostgreSQL, no HA; Pro multi-host
   adds coverage but not horizontal scaling or a redundant datastore.
4. ❌ **Key features gated behind Pro** — escalation, Slack/Teams/email,
   multi-host, incidents, subscriber notifications, long retention.
5. ❌ **Tight Community caps** (10 endpoints / 5 heartbeats / 5 certs / 3
   components).
6. ⚠️ **Young, single-maintainer project** — fast-moving but low bus factor and a
   short track record.
7. ⚠️ **No multi-region synthetic probing** — agents are per-host infra collectors,
   not geo-distributed probes.

## Use cases

**Best for**:
- Docker / Compose / Kubernetes operators who want **container health + endpoint
  + cert + cron + resource + security monitoring in one tiny container**.
- Homelab and small-team setups prioritizing a minimal footprint, already behind
  a reverse proxy that handles auth.
- Teams that want **AI-assistant (MCP) access** to their monitoring data.

**Not ideal for**:
- Broad multi-protocol monitoring (DNS, mail, DB, queues, SNMP, game servers).
- **Multi-tenant / RBAC deployments** or anywhere built-in auth is required.
- High-scale, HA, or multi-region monitoring needing distributed workers and a
  redundant datastore.
- Teams wanting Slack/Teams/email alerting or incidents without paying for Pro.

## Community reception

From the LinuxFr discussion and other coverage (June 2026):

- **Positive**: the "30-seconds-to-full-visibility, zero-config" container story
  lands well; reviewers like the single-binary, low-footprint design.
- **Open-core / "open-source washing" critique** (Exagone313): the project uses a
  **Contributor License Agreement** that lets the company relicense
  contributions, and **feature limits are hard-coded in source** (you can
  recompile to remove them, but that's friction). The author defended it as
  AGPL-compatible and compared it to Nextcloud/GitLab; the critic disputed the
  analogy (Nextcloud has no CLA; GitLab's is narrower). Relevant context for
  SolidPing's own OSS-vs-paid stance —
  `specs/questions/2026-05-03-oss-vs-paid-code-separation.md`.
- **Kubernetes permissions**: the cluster-wide read role drew concern; the author
  framed it as an intentional simplicity tradeoff (above).
- **Pricing**: the Pro tier's jump from €9 to €29/mo over a few months was noted.

## Takeaways for SolidPing

Maintenant validates several SolidPing bets (Go, single-binary, self-hosted, MCP
for AI) while pointing at areas worth watching:

1. **Container observability depth** — live resource metrics, log streaming,
   restart-loop and image-update detection go well beyond a container *check*.
   If container monitoring becomes a priority, this is the reference
   implementation to study. (SolidPing has already mined it for restart-loop
   detection, SSL graduated-expiry/chain reporting, MCP OAuth2, and config-as-code
   — see the `specs/done/2026/06/2026-06-20-*` series.)
2. **Network-security insights** — exposed-port / privileged-container / CVE
   surfacing is a differentiated category SolidPing could consider.
3. **Docker-label configuration** — a low-friction config-as-code path that
   complements a full YAML/Terraform story.

Conversely, SolidPing's **protocol breadth (30+ types), built-in auth +
multi-tenancy + RBAC, dual-DB + distributed workers, credentials encryption, and
un-paywalled alerting/incident features** are clear differentiators against
Maintenant's no-auth, SQLite-only, four-probe, open-core design.

## Corrections from the prior analysis

The earlier single-source writeup was broadly right on shape but wrong or
incomplete on several specifics now verified against primary sources:

- **Authentication**: previously implied OAuth2 was a general auth feature; in
  fact the app has **no built-in auth** and OAuth2 covers only the MCP endpoint.
- **"Single host only"**: outdated — **Pro adds agent-based multi-host
  monitoring** over gRPC.
- **RAM**: the project's own figure is **~17 MB** (the "< 30 MB" came from the
  article rounding).
- **Missing categories**: **network-security insights** and **OCI-based update
  intelligence** were absent.
- **Editions**: there is also an **Enterprise** tier (SSO, audit logs, on-prem).
- **Standalone mode**, the **Vue 3 frontend**, **telemetry via `shm`**, and the
  **€9→€29 price history** were not captured.

## Sources

- [IT-Connect (FR) — "Maintenant : la supervision open source"](https://www.it-connect.fr/maintenant-supervision-open-source/)
- [IT-Connect (EN) — "Now: monitor containers, endpoints and certificates without tool sprawl"](https://www.it-connect.tech/now-monitor-containers-endpoints-and-certificates-without-tool-sprawl/)
- [Maintenant website](https://maintenant.dev) · [Documentation](https://docs.maintenant.dev)
- [Maintenant repository (GitHub / kOlapsis)](https://github.com/kOlapsis/maintenant)
- [`shm` telemetry project (GitHub / kOlapsis)](https://github.com/kOlapsis/shm)
- [LinuxFr discussion — "monitorer toute sa stack Docker depuis un seul conteneur"](https://linuxfr.org/news/maintenant-monitorer-toute-sa-stack-docker-depuis-un-seul-conteneur)
- [ComputaSYS — "un outil de supervision open source tout-en-un"](https://www.computasys.com/un-outil-de-supervision-open-source-tout-en-un/)
- [Benjamin Touchard / kOlapsis](https://kolapsis.com)
