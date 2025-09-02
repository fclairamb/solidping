# SolidPing

A distributed monitoring platform for checking availability and performance of services across multiple protocols.

## Overview

SolidPing is a multi-tenant monitoring system that enables organizations to monitor their infrastructure through distributed workers executing health checks. It's designed for low resource consumption and easy self-hosting.

### Key Features

- **Multi-protocol monitoring**: HTTP/HTTPS, TCP, ICMP, DNS, SSL certificates, and more
- **Distributed workers**: Execute checks from multiple locations/regions
- **Multi-tenant**: Organization-scoped data isolation
- **Low footprint**: Single binary, PostgreSQL-only dependency
- **Fast checks**: Sub-minute frequencies supported
- **Flexible notifications**: Slack, Discord, Email, Webhooks

## Quick Start

### Prerequisites
- Go 1.24+
- PostgreSQL 15+
- Docker (for development)

### Development Setup

```bash
# Start PostgreSQL
docker-compose up -d

# Run migrations
./solidping migrate

# Start with hot reload
make air
```

### Default Credentials
- Email: `demo@solidping.com`
- Password: `demosolidping`
- PAT Token: `pat_demo`

### API Example
```bash
curl -s -H 'Authorization: Bearer pat_demo' \
  'http://localhost:4000/api/v1/orgs/demo/checks'
```

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

See [ARCHITECTURE.md](ARCHITECTURE.md) for detailed system design.

### Core Components
- **API Server**: REST API for managing checks and viewing results
- **Frontend**: Web interface (React)
- **Workers**: Distributed agents executing monitoring checks
- **Database**: PostgreSQL for configuration and time-series data

### Technology Stack
- **Backend**: Go, bunrouter, Bun ORM, koanf
- **Frontend**: React, TanStack Router, shadcn/ui
- **Database**: PostgreSQL with partitioned results table

## Protocol Support

### Network Protocols
- [ ] HTTP/HTTPS (status code, body matching, JSON validation)
- [ ] TCP (port check, request/response)
- [ ] SSL certificate validation
- [ ] ICMP Ping
- [ ] DNS record checks
- [ ] FTP/SFTP
- [ ] SSH
- [ ] SMTP
- [ ] LDAP/SNMP
- [ ] PostgreSQL/MySQL queries

### Applications
- [ ] Steam Game Server
- [ ] Metabase
- [ ] Home Assistant

### Notifications
- [ ] Slack
- [ ] Discord
- [ ] Email
- [ ] Webhooks

## Development

### Commands
```bash
make build       # Build binary
make air         # Hot-reload development
make gotest      # Run tests
make lint        # Lint code
make generate    # Generate OpenAPI code
```

### Project Structure
```
solidping/
├── main.go              # CLI entry point
├── back/
│   └── internal/
│       ├── app/         # Server setup, services
│       ├── handlers/    # HTTP handlers + business logic
│       ├── models/      # Database entities
│       └── middleware/  # Auth, CORS, org context
├── front/               # React frontend
```

## Goals

### Primary
- Many protocols and test types
- Low memory footprint
- Fast execution (every-second pings)
- Easy self-hosting (PostgreSQL only)
- 30-second setup
- Cross-platform (Linux, macOS, Windows)
- SLA calculation and reporting
- Public status pages for transparency

### Non-Goals
- End-to-end browser testing (use Playwright directly)
- Complex test scenarios (use dedicated testing tools)

## Inspiration

- [uptime-kuma](https://github.com/louislam/uptime-kuma) - Great self-hosted monitoring tool

## License

AGPL - Open source for self-hosting, commercial hosting rights reserved.
