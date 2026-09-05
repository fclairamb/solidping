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
| **Self-host** | `docker run -p 4000:4000 --hostname solidping ghcr.io/fclairamb/solidping` — SQLite by default, no other service needed. First login is `admin@solidping.io` / `solidpass`, and you must change it (see [Default Credentials](#default-credentials)). |

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

## Configuration

Everything is configured with `SP_`-prefixed environment variables. Precedence:
**environment variables** > `config.local.yml` > `config.yml` > defaults.

You only need the database settings to get started — the defaults cover the rest.

### SQLite (the default — good for a single instance)

Nothing to configure. To keep the data across restarts, point `SP_DB_DIR` at a
directory you mount in:

```bash
mkdir -p ./solidping-data

docker run -p 4000:4000 \
  --hostname solidping \
  -u "$(id -u):$(id -g)" \
  -v "$PWD/solidping-data:/data" \
  -e SP_DB_DIR=/data \
  ghcr.io/fclairamb/solidping
```

Two flags there are not optional, and both are easy to trip over:

- **`--hostname solidping`** — the worker name is derived from the hostname and
  must match `^[a-z][a-z0-9-]{2,20}$`. Docker's default hostname is the random
  container ID, which starts with a digit most of the time, and the server then
  refuses to start. Pass `--hostname`, or set `SP_NODE_NAME` instead.
- **`-u "$(id -u):$(id -g)"`** — the image runs as the nonroot user `65532`,
  which cannot write to a directory your host created. Running as yourself, with
  a directory you own, is the simplest thing that works. (A named Docker volume
  hits the same wall: it is created root-owned.)

### PostgreSQL (recommended for production)

Schema migrations run on first boot, so point it at an empty database and let it
create its own tables:

```bash
docker run -p 4000:4000 \
  --hostname solidping \
  -e SP_DB_TYPE=postgres \
  -e SP_DB_URL='postgresql://solidping:password@postgres:5432/solidping?sslmode=disable' \
  ghcr.io/fclairamb/solidping
```

`sslmode=disable` is right for a database on the same private network, and is
what a stock `postgres` container accepts — it serves no TLS, so `sslmode=require`
fails against one with `SSL is not enabled on the server`. For a managed or
remote database, use `sslmode=require` (or `verify-full`) instead.

### The variables you actually need

| Variable | Default | Description |
|----------|---------|-------------|
| `SP_DB_TYPE` | `sqlite` | `sqlite` or `postgres` |
| `SP_DB_URL` | — | PostgreSQL connection string (required when `SP_DB_TYPE=postgres`) |
| `SP_DB_DIR` | `.` | Where the SQLite file lives — set it to a volume |
| `SP_SERVER_LISTEN` | `:4000` | Listen address |
| `SP_BASE_URL` | `http://localhost:4000` | Public URL, used in links and notifications |
| `SP_NODE_NAME` | the hostname | Worker name, `^[a-z][a-z0-9-]{2,20}$`. Set it, or pass `--hostname` — the server will not start if the hostname does not match |
| `SP_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

That is the whole getting-started surface. Everything else — authentication and
SSO, email and the other notification channels, custom domains and TLS,
multi-region workers, data retention, storage — is optional and documented at
**[docs.solidping.io](https://docs.solidping.io)**:

| | |
|---|---|
| [Configuration overview](https://docs.solidping.io/configuration/) | every variable, grouped by area |
| [Database](https://docs.solidping.io/configuration/database) | connection strings, SSL modes, pooling, backups |
| [Authentication](https://docs.solidping.io/configuration/authentication) | OAuth providers, OIDC, SAML, LDAP, 2FA |
| [Notifications](https://docs.solidping.io/configuration/notifications) | Slack, Discord, email, webhooks and the rest |

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
