# SolidPing

A distributed monitoring platform for checking availability and performance of services across multiple protocols.

## Overview

SolidPing is a multi-tenant monitoring system that enables organizations to monitor their infrastructure through distributed workers executing health checks. It's designed for low resource consumption and easy self-hosting.

### Key Features

- **20 check types**: HTTP, TCP, ICMP, DNS, SSL, databases, mail servers, and more
- **Distributed workers**: Execute checks from multiple locations/regions
- **Multi-tenant**: Organization-scoped data isolation
- **Low footprint**: Single binary, PostgreSQL-only dependency
- **Fast checks**: Sub-minute frequencies supported
- **Notifications**: Slack, Discord, Email, Webhooks
- **Public status pages**: Embeddable dashboard for transparency
- **Adaptive incident resolution**: Smart thresholds with cooldown and escalation
- **JavaScript scripting**: Custom monitoring logic via JS checks
- **OAuth**: Google, GitHub, GitLab, Microsoft SSO support
- **CLI client**: Manage checks and results from the terminal

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
- Email: `admin@solidping.com`
- Password: `solidpass`
- Organization: `default`

### API Example
```bash
# Get a JWT token
TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"org":"default","email":"admin@solidping.com","password":"solidpass"}' \
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
| WebSocket | Connection check |

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

### Databases
| Protocol | Description |
|----------|-------------|
| PostgreSQL | Connection + query execution |
| MySQL/MariaDB | Connection + query execution |
| MongoDB | Ping command |
| Redis | PING command |

### Remote Access
| Protocol | Description |
|----------|-------------|
| SSH | Server availability |
| FTP | Server availability |
| SFTP | Server availability |

### Other
| Type | Description |
|------|-------------|
| Heartbeat | Passive monitoring via incoming pings |
| JavaScript | Custom monitoring scripts |

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
| `SP_NODE_ROLE` | Node role: `all`, `api`, `jobs`, `checks` | `all` |
| `SP_NODE_REGION` | Worker region (required when role=`checks`) | — |
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
| Microsoft | `SP_MICROSOFT_CLIENT_ID`, `SP_MICROSOFT_CLIENT_SECRET` |

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
- **Notifications**: Slack, Discord, Email, Webhook integrations
- **Database**: PostgreSQL with partitioned results table

### Technology Stack
- **Backend**: Go 1.24+, bunrouter, Bun ORM, koanf
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
│       ├── models/              # Database entities
│       ├── migrations/          # Database migrations
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
- End-to-end browser testing (use Playwright directly)
- Complex test scenarios (use dedicated testing tools)

## Inspiration

- [uptime-kuma](https://github.com/louislam/uptime-kuma) - Great self-hosted monitoring tool

## License

AGPL-3.0 - See [LICENSE.md](LICENSE.md).
