<div align="center">

<img src="https://raw.githubusercontent.com/fclairamb/solidping/main/res/logo_256.png" alt="SolidPing" width="120">

# SolidPing

**Distributed, self-hostable uptime monitoring.**
40 check types, multi-region workers, private agents, status pages,
incidents and on-call escalation — in a single Go binary.

[![Build](https://img.shields.io/github/actions/workflow/status/fclairamb/solidping/ci.yml?branch=main&label=build&logo=github)](https://github.com/fclairamb/solidping/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/fclairamb/solidping?label=release&logo=github)](https://github.com/fclairamb/solidping/releases/latest)
[![Go](https://img.shields.io/github/go-mod/go-version/fclairamb/solidping?filename=server%2Fgo.mod&logo=go)](https://github.com/fclairamb/solidping/blob/main/server/go.mod)
[![Go Reference](https://pkg.go.dev/badge/github.com/fclairamb/solidping/server.svg)](https://pkg.go.dev/github.com/fclairamb/solidping/server)
[![License](https://img.shields.io/github/license/fclairamb/solidping?color=blue)](LICENSE)

[![Live](https://img.shields.io/badge/live-www.solidping.io-2ea44f?logo=icloud&logoColor=white)](https://www.solidping.io)
[![Status](https://img.shields.io/badge/status-status.solidping.io-2ea44f?logo=statuspage&logoColor=white)](https://status.solidping.io)
[![Docs](https://img.shields.io/badge/docs-docs.solidping.io-informational?logo=readthedocs&logoColor=white)](https://docs.solidping.io)
[![Container](https://img.shields.io/badge/ghcr.io-fclairamb%2Fsolidping-blue?logo=docker&logoColor=white)](https://github.com/fclairamb/solidping/pkgs/container/solidping)

</div>

---

<div align="center">

### Create an HTTP check, start to finish — 18 seconds

<img src="https://raw.githubusercontent.com/fclairamb/solidping/main/res/screenshots/create-http-check.gif" alt="Creating an HTTP check in SolidPing, from the new-check form to the first result" width="800">

</div>

## Screenshots

| Checks list | New-check form | Check detail |
|:---:|:---:|:---:|
| <img src="https://raw.githubusercontent.com/fclairamb/solidping/main/res/screenshots/checks-list.png" alt="Checks list in the SolidPing dashboard" width="270"> | <img src="https://raw.githubusercontent.com/fclairamb/solidping/main/res/screenshots/check-form.png" alt="New-check form, filled in" width="270"> | <img src="https://raw.githubusercontent.com/fclairamb/solidping/main/res/screenshots/check-detail.png" alt="Check detail page with response-time history" width="270"> |

## Try it

| | |
|---|---|
| **Hosted** | [www.solidping.io](https://www.solidping.io) — sign up, no card needed |
| **Live status page** | [status.solidping.io](https://status.solidping.io) — a real SolidPing instance watching the production one, from another provider in another country |
| **Documentation** | [docs.solidping.io](https://docs.solidping.io) |
| **Self-host** | `docker run -p 4000:4000 ghcr.io/fclairamb/solidping` — SQLite by default, no other service needed. First login is `admin@solidping.io` / `solidpass`, and you must change it (see [Default Credentials](#default-credentials)). |

## Overview

SolidPing is a multi-tenant monitoring system that enables organizations to monitor their infrastructure through distributed workers executing health checks. It's designed for low resource consumption and easy self-hosting.

### Key Features

- **40 check types**: HTTP, TCP, UDP, ICMP, DNS, DNSBL, NTP, SSL/Domain, SSH, RDP, FTP/SFTP, SMTP/POP3/IMAP, Email (JMAP passive inbox), WebSocket, SIP, gRPC, Prometheus, 7 databases (Postgres, MySQL, MSSQL, Oracle, ClickHouse, MongoDB, Redis), 3 message queues (Kafka, RabbitMQ, MQTT), Docker, Kubernetes, SNMP, Freebox line, game server (Source/A2S, Minecraft), headless browser, custom JS, heartbeat
- **Distributed workers**: Multi-region check execution with lease-based scheduling, per-region check periods with spread control, and per-org check-rate quotas
- **Private locations**: Deported agents run checks from inside your own network over an outbound WebSocket, with per-org agent quotas
- **Multi-tenant**: Organization-scoped data isolation, RBAC, 2FA (TOTP), labels with autocomplete
- **Low footprint**: Single binary; SQLite, embedded Postgres, or external Postgres
- **Fast checks**: Sub-minute frequencies supported
- **Notifications (10 native)**: Slack (OAuth + threads + Marketplace install), Discord (OAuth + webhook), Email, Webhooks, Google Chat, Mattermost, Ntfy, PagerDuty, Pushover, Web Push (VAPID)
- **Incidents**: Adaptive resolution with cooldown, group-incident correlation (one alert per outage, not per check), acknowledgment, snooze, manual resolve, and per-incident comments
- **Check groups**: Organize checks into groups with grouped pagination and group-level incident correlation
- **On-call & escalation**: Rotation schedules with overrides, multi-step escalation policies (user / schedule / connection / all-admins targets, repeats)
- **Credentials encryption at rest**: Envelope encryption with out-of-band master key; secrets never echoed back to the dashboard
- **SSH tunnels**: Reach otherwise-unreachable targets through SSH jump hosts
- **Status pages**: Sections, resources, public availability metrics, locale-aware date formatting
- **Maintenance windows**: Recurring suppression of alerts
- **JavaScript scripting**: Sandboxed custom monitoring logic
- **Browser monitoring**: Headless Chrome via Rod
- **MCP server**: AI/LLM tool access via Model Context Protocol
- **SSO / OAuth**: Google, GitHub, GitLab, Microsoft, Slack, Discord, plus generic OIDC, SAML, and LDAP / Active Directory (per-provider enable toggle, with self-service token revocation)
- **Observability**: Prometheus `/metrics`, Sentry integration, OpenTelemetry
- **CLI client**: Manage checks and results from the terminal
- **i18n**: Multi-language dashboard (English, French, German, Spanish)

## Quick Start

### Prerequisites
- Go 1.24+
- PostgreSQL 15+
- Docker (for development)
- Bun (for frontend development)

### Development Setup

```bash
# Start PostgreSQL
docker-compose up -d

# Build and run
make build && ./solidping serve

# Or use hot reload for development
make dev-test   # Backend + frontend with hot reload
```

### Default Credentials
- Email: `admin@solidping.io`
- Password: `solidpass`
- Organization: `default`

Both halves of that pair are published here, so the first login on a fresh
database **must set a new password** before the account can do anything else.
The dashboard takes you straight to the form; over the API the login succeeds
and every endpoint except `POST /api/v1/auth/change-password`,
`GET /api/v1/auth/me` and `POST /api/v1/auth/logout` answers `403` with code
`PASSWORD_CHANGE_REQUIRED` until you rotate it.

### API Example
```bash
# Get a JWT token
TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"org":"default","email":"admin@solidping.io","password":"solidpass"}' \
  'http://localhost:4000/api/v1/auth/login' | jq -r '.accessToken')

# List checks
curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:4000/api/v1/orgs/default/checks'
```

## Supported Check Types

### Network
| Protocol | Description |
|----------|-------------|
| HTTP/HTTPS | Status codes, body matching, JSON assertions, Basic Auth |
| TCP | Port connectivity |
| UDP | Port reachability |
| ICMP | Ping |
| DNS | Record resolution |
| DNSBL | DNS blocklist (RBL) membership |
| NTP | Time server reachability and clock drift |
| WebSocket | Connection check |
| SIP | VoIP SIP server (OPTIONS ping) |

### Security & Certificates
| Protocol | Description |
|----------|-------------|
| SSL/TLS | Certificate validity and expiration |
| Domain | Domain name expiration (WHOIS) |

### Email
| Protocol | Description |
|----------|-------------|
| SMTP | Server connectivity, STARTTLS, AUTH |
| POP3 | Server availability |
| IMAP | Server availability |
| Email (JMAP) | Passive inbox monitoring — receive a known message via JMAP and assert delivery |

### Databases
| Protocol | Description |
|----------|-------------|
| PostgreSQL | Connection + query execution |
| MySQL/MariaDB | Connection + query execution |
| MSSQL | Connection + query execution |
| Oracle | Connection + query execution |
| ClickHouse | Connection + query execution |
| MongoDB | Ping command |
| Redis | PING command |

### Remote Access
| Protocol | Description |
|----------|-------------|
| SSH | Server availability |
| RDP | Pre-auth RDP negotiation handshake (no credentials) |
| FTP | Server availability |
| SFTP | Server availability |

### Message Queues
| Protocol | Description |
|----------|-------------|
| Kafka | Broker connectivity |
| RabbitMQ | Broker connectivity |
| MQTT | Broker connectivity |

### Infrastructure
| Type | Description |
|------|-------------|
| Docker | Container health |
| Kubernetes | Cluster / API server health |
| SNMP | Device monitoring |
| gRPC | Service health |
| A2S | Source / Steam game server query (Valve A2S) |
| Minecraft | Minecraft server query |
| Prometheus | Scrape a Prometheus endpoint and assert on a metric |

### Specialized
| Type | Description |
|------|-------------|
| Heartbeat | Passive monitoring via incoming pings |
| JavaScript | Sandboxed custom monitoring logic |
| Browser | Headless Chrome (Rod) — JS, CSS, full render |
| Freebox Line | Freebox xDSL/fiber line quality (via connected Freebox) |

## Environment Variables

All `SP_` prefixed variables are handled by the configuration system. Precedence: **Environment variables** > `config.local.yml` > `config.yml` > defaults.

### Core

| Variable | Description | Default |
|----------|-------------|---------|
| `SP_DB_TYPE` | Database type: `postgres`, `sqlite`, `sqlite-memory`, `postgres-embedded` | `sqlite` |
| `SP_DB_URL` | PostgreSQL connection string | — |
| `SP_DB_DIR` | SQLite data directory | `.` |
| `SP_DB_RESET` | Reset database on startup (`true`/`1`) | `false` |
| `SP_SERVER_LISTEN` | HTTP listen address | `:4000` |
| `SP_BASE_URL` | Public URL where SolidPing is accessible | `http://localhost:4000` |
| `SP_SHUTDOWN_TIMEOUT` | Graceful shutdown timeout (duration) | `30s` |
| `SP_RUN_MODE` | Runtime mode: `test`, `demo` | — |
| `SP_LOG_LEVEL` | Log level: `debug`, `info`, `warn`, `error` | `info` |
| `SP_NODE_ROLE` | Node role: `all`, `api`, `jobs`, `checks`, `agent` — or a comma-separated combination such as `api,jobs` | `all` |
| `SP_NODE_REGION` | Worker region (required when the role includes `checks`) | — |
| `SP_SERVER_JOB_WORKER_NB` | Concurrent job workers | `2` |
| `SP_SERVER_CHECK_WORKER_NB` | Concurrent check workers | `3` |
| `PORT` | HTTP port (overrides `SP_SERVER_LISTEN`) | — |

### Authentication

| Variable | Description | Default |
|----------|-------------|---------|
| `SP_AUTH_JWT_SECRET` | JWT signing secret (auto-generated if unset) | — |
| `SP_AUTH_REGISTRATION_EMAIL_PATTERN` | Restrict registration by email regex | — |

### Email (SMTP)

| Variable | Description | Default |
|----------|-------------|---------|
| `SP_EMAIL_ENABLED` | Enable email sending | `false` |
| `SP_EMAIL_HOST` | SMTP server hostname | — |
| `SP_EMAIL_PORT` | SMTP port | `587` |
| `SP_EMAIL_USERNAME` | SMTP username | — |
| `SP_EMAIL_PASSWORD` | SMTP password | — |
| `SP_EMAIL_FROM` | Sender email address | — |
| `SP_EMAIL_FROMNAME` | Sender display name | — |
| `SP_EMAIL_AUTHTYPE` | Auth type: `plain`, `login`, `cram-md5` | `login` |
| `SP_EMAIL_PROTOCOL` | Encryption: `none`, `starttls`, `ssl` | `starttls` |
| `SP_EMAIL_INSECURESKIPVERIFY` | Skip TLS certificate verification | `false` |

### OAuth Providers

Set both `_CLIENT_ID` and `_CLIENT_SECRET` to enable an OAuth provider.

| Provider | Variables |
|----------|-----------|
| Google | `SP_GOOGLE_CLIENT_ID`, `SP_GOOGLE_CLIENT_SECRET` |
| GitHub | `SP_GITHUB_CLIENT_ID`, `SP_GITHUB_CLIENT_SECRET` |
| GitLab | `SP_GITLAB_CLIENT_ID`, `SP_GITLAB_CLIENT_SECRET` |
| Microsoft | `SP_MICROSOFT_CLIENT_ID`, `SP_MICROSOFT_CLIENT_SECRET`, `SP_MICROSOFT_TENANT_ID` (default `common`) |

### Slack Integration

| Variable | Description |
|----------|-------------|
| `SP_SLACK_APP_ID` | Slack app ID |
| `SP_SLACK_CLIENT_ID` | Slack client ID |
| `SP_SLACK_CLIENT_SECRET` | Slack client secret |
| `SP_SLACK_SIGNING_SECRET` | Slack signing secret |

### Development

| Variable | Description | Default |
|----------|-------------|---------|
| `SP_REDIRECTS` | Dev proxy redirects (format: `/path:host:port/target,...`) | — |
| `LOG_LEVEL` | Log level (read early, before config loads) | `info` |
| `NO_COLOR` | Disable colored terminal output | — |
| `FORCE_COLOR` | Force colored terminal output | — |

### CLI

| Variable | Description | Default |
|----------|-------------|---------|
| `SOLIDPING_CONFIG` | CLI config file path | `~/.config/solidping/settings.json` |
| `SOLIDPING_URL` | Server URL override | — |
| `SOLIDPING_ORG` | Organization override | — |
| `SOLIDPING_VERBOSE` | Verbose CLI logging | `false` |

## Architecture

### Core Components
- **API Server**: REST API for managing checks, incidents, and results
- **Dashboard** (`web/dash0`): Admin UI (React + TanStack Router + shadcn/ui)
- **Status Page** (`web/status0`): Public-facing status dashboard
- **Workers**: Distributed agents executing monitoring checks
- **Notifications**: Slack, Discord, Email, Webhooks, Google Chat, Mattermost, Ntfy, PagerDuty, Pushover, Web Push
- **Database**: PostgreSQL (partitioned results) or SQLite

### Technology Stack
- **Backend**: Go 1.24+, go-chi/chi v5, Bun ORM, koanf
- **Frontend**: React 19, TypeScript, Vite, TanStack Router/Query, Tailwind CSS, shadcn/ui
- **Database**: PostgreSQL (production), SQLite (development/single-node)

### Project Structure
```
solidping/
├── server/
│   ├── main.go                  # CLI entry point (serve, migrate, client)
│   └── internal/
│       ├── app/                 # Server setup, services, embedded assets
│       ├── handlers/            # HTTP handlers + business logic
│       ├── checkers/            # Protocol checker implementations
│       ├── notifications/       # Notification channels
│       ├── db/                  # Bun models + Postgres/SQLite migrations
│       └── middleware/          # Auth, CORS, org context
├── web/
│   ├── dash0/                   # Admin dashboard (React)
│   └── status0/                 # Public status page
├── docker-compose.yml           # Development PostgreSQL
├── Dockerfile                   # Production container
└── Makefile                     # Build targets
```

## Development

### Commands
```bash
make build            # Build complete application
make dev-test         # Hot-reload backend + frontend
make dev-backend      # Backend only with hot reload (air)
make dev-dash0        # Dashboard dev server
make test             # Run backend tests
make lint             # Lint all code
make fmt              # Format all code
make docker-build     # Build Docker image
```

### CLI Client
```bash
# Build the CLI
make build-cli

# Usage
./bin/sp auth login
./bin/sp checks list
./bin/sp results list
```

## Goals

### Primary
- Many protocols and test types
- Low memory footprint
- Fast execution (sub-minute checks)
- Easy self-hosting (single binary + PostgreSQL)
- Cross-platform (Linux, macOS, Windows)
- Public status pages

### Non-Goals
- Multi-step transactional / scripted user-flow testing (use Playwright directly for that)
- Application Performance Monitoring / RUM (use Datadog, New Relic, or Site24x7)

## Inspiration

- [uptime-kuma](https://github.com/louislam/uptime-kuma) - Great self-hosted monitoring tool

## License

AGPL-3.0 - See [LICENSE](LICENSE).
