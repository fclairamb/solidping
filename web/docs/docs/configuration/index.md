---
sidebar_position: 1
title: Overview
---

# Configuration

SolidPing is configured primarily through environment variables. All environment variables use the `SP_` prefix. Configuration uses hierarchical precedence: **Environment variables** > `config.local.yml` > `config.yml` > defaults.

## Configuration Methods

1. **Environment Variables** - Recommended for Docker and production
2. **Configuration File** - `config.yml` in the working directory (with `config.local.yml` for local overrides)
3. **Command Line** - Some options can be passed via CLI flags

## Quick Reference

### Essential Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `SP_DB_TYPE` | `sqlite` | Database type: `postgres`, `sqlite`, `sqlite-memory`, `postgres-embedded` |
| `SP_DB_URL` | - | PostgreSQL connection string |
| `SP_DB_DIR` | `.` | SQLite database directory |
| `SP_DB_RESET` | `false` | Reset database on startup |
| `SP_SERVER_LISTEN` | `:4000` | Server address and port |
| `SP_BASE_URL` | `http://localhost:4000` | Public URL where SolidPing is accessible |

### Server Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `SP_SERVER_LISTEN` | `:4000` | Listen address (e.g., `:4000`, `0.0.0.0:8080`) |
| `SP_SERVER_JOB_WORKER_NB` | `2` | Number of job runner goroutines |
| `SP_SERVER_CHECK_WORKER_NB` | `3` | Number of check runner goroutines |
| `SP_SHUTDOWN_TIMEOUT` | `30s` | Graceful shutdown timeout |
| `PORT` | - | Alternative to `SP_SERVER_LISTEN` (for PaaS compatibility) |

### Custom Domains & TLS

| Variable | Default | Description |
|----------|---------|-------------|
| `SP_CUSTOM_DOMAIN_CNAME_TARGET` | host of `base_url` | Hostname customers point their status-page `CNAME` at |
| `SP_CUSTOM_DOMAIN_CNAME_MODE` | `shared` | `shared` (plain target) or `token` (per-page `<token>.cname.<target>`) |
| `SP_ACME_ENABLED` | `false` | Terminate TLS in-server with Let's Encrypt certificates obtained on demand |
| `SP_ACME_EMAIL` | - | ACME account contact — **required** when `SP_ACME_ENABLED=true` |
| `SP_ACME_CA_URL` | Let's Encrypt prod | ACME directory URL (point at LE staging while testing) |
| `SP_ACME_LISTEN_HTTP` | `:80` | HTTP-01 challenge listener; redirects everything else to HTTPS |
| `SP_ACME_LISTEN_HTTPS` | `:443` | TLS listener, feeding the normal routing |

Enabling `SP_ACME_ENABLED` makes the process bind two extra ports and removes
the need for a TLS-terminating reverse proxy. Leave it off to keep TLS at your
own edge. See [Custom Domains](/features/custom-domains) for the full setup.

### Distributed Workers

| Variable | Default | Description |
|----------|---------|-------------|
| `SP_NODE_ROLE` | `all` | Node role: `all`, `api`, `jobs`, `checks` |
| `SP_NODE_REGION` | - | Worker region (required when role=`checks`) |
| `SP_REGIONS` | - | Region display definitions, as a JSON list of `{slug, emoji, name}` |

Use `SP_NODE_ROLE` to run SolidPing in a distributed configuration:
- `all` - Single node running everything (default)
- `api` - Only serve the API and dashboard
- `jobs` - Only run background jobs (scheduling, cleanup)
- `checks` - Only execute health checks (worker mode)

`SP_REGIONS` names your regions declaratively: the dashboard renders
`{emoji} {name}` wherever a region appears (check form, results, worker
load), falling back to the raw slug for undefined regions. Set it on the
main server; it seeds the `regions` system parameter at startup:

```bash
SP_REGIONS='[{"slug": "default", "emoji": "🇪🇺", "name": "EU1 (default)"}, {"slug": "us-1", "emoji": "🇺🇸", "name": "US1"}]'
```

When unset, the stored parameter (or the built-in `default` region) is
used, so edits made through the API are preserved across restarts.

### Logging

| Variable | Default | Description |
|----------|---------|-------------|
| `SP_LOG_LEVEL` | `info` | Log level: `debug`, `info`, `warn`, `error` |

### Authentication

| Variable | Default | Description |
|----------|---------|-------------|
| `SP_AUTH_JWT_SECRET` | auto-generated | JWT signing secret |
| `SP_AUTH_REGISTRATION_EMAIL_PATTERN` | - | Restrict registration by email regex |
| `SP_AUTH_PASSWORD_ALGORITHM` | `argon2id` | Password hashing algorithm (`argon2id` or `bcrypt`) — see [Password Hashing](/configuration/authentication#password-hashing) for cost parameters |

### OAuth Providers

Set both `_CLIENT_ID` and `_CLIENT_SECRET` to enable each provider:

| Provider | Variables |
|----------|-----------|
| Google | `SP_GOOGLE_CLIENT_ID`, `SP_GOOGLE_CLIENT_SECRET` |
| GitHub | `SP_GITHUB_CLIENT_ID`, `SP_GITHUB_CLIENT_SECRET` |
| GitLab | `SP_GITLAB_CLIENT_ID`, `SP_GITLAB_CLIENT_SECRET` |
| Microsoft | `SP_MICROSOFT_CLIENT_ID`, `SP_MICROSOFT_CLIENT_SECRET`, `SP_MICROSOFT_TENANT_ID` (default `common`) |
| Slack | `SP_SLACK_CLIENT_ID`, `SP_SLACK_CLIENT_SECRET` |
| Discord | `SP_DISCORD_CLIENT_ID`, `SP_DISCORD_CLIENT_SECRET` |

Users can also enable TOTP two-factor authentication on their accounts. See [Authentication](/configuration/authentication) for details.

### Run Mode

| Variable | Default | Description |
|----------|---------|-------------|
| `SP_RUN_MODE` | - | Runtime mode: `test` (seed test data), `demo` |

### Encryption

| Variable | Default | Description |
|----------|---------|-------------|
| `SP_ENCRYPTION_MASTER_KEY` | - | Base64-encoded 32-byte key for credential encryption at rest |
| `SP_ENCRYPTION_MASTER_KEY_FILE` | - | Path to a file holding the base64 master key |
| `SP_ENCRYPTION_AUTO_MIGRATE` | `true` | Encrypt existing plaintext credentials on startup |

See [Security & Encryption](/configuration/security) for the full guide.

### Observability

| Variable | Default | Description |
|----------|---------|-------------|
| `SP_PROMETHEUS_ENABLED` | `true` | Enable the Prometheus `/metrics` endpoint |
| `SP_PROMETHEUS_PATH` | `/metrics` | Metrics endpoint path |
| `SP_SENTRY_DSN` | - | Sentry DSN for error tracking |
| `SP_OTEL_ENABLED` | `false` | Enable OpenTelemetry export |
| `SP_OTEL_ENDPOINT` | - | OTLP collector endpoint |

See [Observability](/features/observability) for Sentry and OpenTelemetry details.

### Development

| Variable | Description |
|----------|-------------|
| `SP_REDIRECTS` | Dev proxy redirects (format: `/path:host:port/target,...`) |

## Example Configuration

### Environment Variables

```bash
# Database (PostgreSQL)
SP_DB_TYPE=postgres
SP_DB_URL=postgresql://solidping:password@localhost:5432/solidping?sslmode=disable

# Server
SP_BASE_URL=https://monitoring.example.com
SP_SERVER_LISTEN=:4000

# Workers
SP_SERVER_JOB_WORKER_NB=4
SP_SERVER_CHECK_WORKER_NB=8

# Authentication
SP_AUTH_JWT_SECRET=your-secure-random-secret
SP_GOOGLE_CLIENT_ID=your-google-client-id
SP_GOOGLE_CLIENT_SECRET=your-google-client-secret

# Logging
SP_LOG_LEVEL=info
```

### Configuration File (config.yml)

```yaml
db:
  type: postgres
  url: postgresql://solidping:password@localhost:5432/solidping?sslmode=disable

base_url: https://monitoring.example.com

server:
  listen: ":4000"
  job_worker_nb: 4
  check_worker_nb: 8
  shutdown_timeout: 30s

auth:
  jwt_secret: your-secure-random-secret

email:
  enabled: true
  host: smtp.example.com
  port: 587
  username: noreply@example.com
  password: smtp-password
  from: noreply@example.com
  from_name: SolidPing

slack:
  app_id: your-slack-app-id
  client_id: your-slack-client-id
  client_secret: your-slack-client-secret
  signing_secret: your-slack-signing-secret
```

## CLI Configuration

The SolidPing CLI client (`sp`) uses its own configuration:

| Variable | Default | Description |
|----------|---------|-------------|
| `SOLIDPING_CONFIG` | `~/.config/solidping/settings.json` | CLI config file path |
| `SOLIDPING_URL` | - | Server URL override |
| `SOLIDPING_ORG` | - | Organization override |
| `SOLIDPING_VERBOSE` | `false` | Verbose CLI logging |

## Sections

- [Database Configuration](/configuration/database) - PostgreSQL and SQLite options
- [Notifications](/configuration/notifications) - Email, Slack, Discord, webhooks, and more
- [Authentication](/configuration/authentication) - OAuth providers, 2FA, and access control
- [Security & Encryption](/configuration/security) - Credentials encryption at rest

## Security Recommendations

:::warning Production Security
Always change these in production:
- Set `SP_AUTH_JWT_SECRET` to a strong random value
- Database passwords - Use strong, unique passwords
- Set `SP_BASE_URL` to your public URL
- Email credentials - Store securely, never commit to version control
:::
