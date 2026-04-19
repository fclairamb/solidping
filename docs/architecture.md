# SolidPing Architecture

This document describes the architecture of SolidPing, a distributed monitoring platform for checking availability and performance of services across 30+ protocols.

## System Overview

SolidPing is a multi-tenant distributed monitoring platform that enables organizations to monitor their infrastructure through distributed workers executing health checks across network, email, database, message queue, and infrastructure protocols.

It ships as a **single binary** containing the API server, check workers, job workers, CLI, and embedded frontend assets. The only external dependency is the database server (PostgreSQL or SQLite, with an embedded PostgreSQL option for zero-config deployments).

### Key Components

1. **API Server**: REST API server handling user requests and orchestrating monitoring
2. **Frontends**: Main dashboard (dash0) and public status page (status0), both embedded in the binary
3. **Check Workers**: Distributed agents that execute monitoring checks via lease-based scheduling
4. **Job Workers**: Background job processors for email, webhooks, aggregation, notifications, and state cleanup
5. **Database**: PostgreSQL or SQLite for storing configuration, users, and time-series monitoring results
6. **MCP Server**: Model Context Protocol integration for AI tool access

## Architecture Patterns

### Handler-Service Pattern

The backend strictly separates HTTP concerns from business logic through a two-layer architecture:

#### Handlers Layer (`*_handler.go`)
- **Responsibility**: HTTP request/response handling
- **Uses**: `base.HandlerBase` for common functionality
- **Tasks**:
  - Input validation and parameter parsing
  - Authentication and authorization via middleware
  - Error translation from domain errors to HTTP status codes
  - JSON response formatting
- **Constraint**: No direct database access

#### Services Layer (`*_service.go`)
- **Responsibility**: Business logic and data operations
- **Uses**: Bun ORM for database operations
- **Tasks**:
  - Business logic implementation
  - Database operations and transaction management
  - Domain-specific validation
  - Inter-service communication
- **Constraint**: Returns domain errors, not HTTP errors

#### Dependency Injection
- Services registered in `services.Registry` (`internal/app/services/`)
- Handlers receive services via constructor injection
- Services can depend on other services
- Handlers never injected into services (one-way dependency)

### Multi-Tenancy

All resources are scoped to organizations using `organization_uid`:
- Data isolation at the database level
- Organization context injected via middleware
- URL-based organization identification (`/api/v1/orgs/$org/...`)
- Results table partitioned by organization for performance

### Two-Tier Worker System

#### Check Workers (`checkworker/`)

The system uses a lease-based mechanism for distributing checks across workers:

1. **Check Jobs**: Each check has a corresponding job with scheduling metadata
2. **Lease Acquisition**: Workers acquire time-limited leases on jobs
3. **Execution**: Worker executes check and stores results
4. **Lease Renewal**: Lease expires if worker fails, allowing another worker to take over
5. **Results Storage**: Time-series data stored with aggregation by period

Workers acquire jobs using PostgreSQL's `SELECT FOR UPDATE SKIP LOCKED` to prevent conflicts:

```sql
UPDATE check_jobs
SET
  lease_worker_uid = $worker_uid,
  lease_expires_at = now() + interval '500ms',
  lease_starts = lease_starts + 1,
  updated_at = now()
WHERE uid = (
  SELECT uid FROM check_jobs
  WHERE scheduled_at <= now()
    AND (lease_expires_at IS NULL OR lease_expires_at < now())
  ORDER BY scheduled_at
  FOR UPDATE SKIP LOCKED
  LIMIT 1
)
RETURNING *;
```

**Lease lifecycle:**
1. Worker selects job with `FOR UPDATE SKIP LOCKED` (non-blocking)
2. Sets `lease_expires_at` to 500ms in the future
3. Increments `lease_starts` counter
4. Executes check and stores result
5. On success: resets `lease_starts` to 0, sets `scheduled_at` to next run
6. On failure: lease expires, another worker can pick it up
7. If `lease_starts` reaches 10+, the check is considered problematic and should be disabled

#### Job Workers (`jobs/jobworker/`)

Background job processing system for asynchronous tasks:

- **Sleep**: No-op job for testing and scheduling
- **Email**: Sends notification and alert emails
- **Webhook**: Delivers webhook payloads to external endpoints
- **Startup**: Initialization tasks on server start
- **Aggregation**: Computes aggregated metrics from raw results (daily, monthly, yearly)
- **StateCleanup**: Cleans up expired leases, stale workers, and old data
- **Notification**: Dispatches notifications across all configured channels

Jobs are defined in `jobdef/`, registered in `jobtypes/`, managed by `jobsvc/`, and executed by `jobworker/`.

## Technology Stack

### Backend
- **Language**: Go 1.24+
- **HTTP Router**: bunrouter (lightweight, fast routing)
- **ORM**: Bun (type-safe SQL builder for PostgreSQL and SQLite)
- **Configuration**: koanf (YAML files + environment variables)
- **CLI**: urfave/cli (command structure)
- **Testing**: testcontainers, gotestsum
- **Observability**: Sentry (error tracking), Prometheus (metrics), OpenTelemetry

### Frontend
- **Dashboard (dash0)**: React, TanStack Router
- **Status Page (status0)**: React, lightweight public-facing status page
- **Testing**: Playwright for E2E tests

### Infrastructure
- **Database**: PostgreSQL (primary) or SQLite (lightweight deployments), with embedded PostgreSQL option
- **Containerization**: Docker Compose for development

## Directory Structure

```
solidping/
  server/
    cmd/sp/                    # Standalone CLI entry point
    internal/
      app/
        server.go              # HTTP server, routing, middleware, DI
        services/              # Service registry
      checkers/                # 30 check type implementations
        checkhttp/             # HTTP/HTTPS checks
        checktcp/              # TCP connectivity
        checkicmp/             # ICMP ping
        checkdns/              # DNS resolution
        checkssl/              # SSL certificate validation
        checkdomain/           # Domain expiration (WHOIS)
        checkwebsocket/        # WebSocket connectivity
        checkudp/              # UDP connectivity
        checksmtp/             # SMTP email
        checkpop3/             # POP3 email
        checkimap/             # IMAP email
        checkpostgres/         # PostgreSQL database
        checkmysql/            # MySQL database
        checkmongodb/          # MongoDB database
        checkmssql/            # MSSQL database
        checkoracle/           # Oracle database
        checkredis/            # Redis cache
        checkkafka/            # Kafka message queue
        checkrabbitmq/         # RabbitMQ message queue
        checkmqtt/             # MQTT message queue
        checkssh/              # SSH connectivity
        checkftp/              # FTP file transfer
        checksftp/             # SFTP file transfer
        checkgrpc/             # gRPC services
        checkdocker/           # Docker container health
        checksnmp/             # SNMP monitoring
        checkgameserver/       # Game server queries
        checkjs/               # JavaScript custom checks
        checkbrowser/          # Browser-based checks
        checkheartbeat/        # Heartbeat (passive, push-based)
        checkerdef/            # Check type definitions and registry
        registry/              # Check type registration
      checkworker/             # Distributed check execution (lease-based)
        checkjobsvc/           # Check job service
      jobs/                    # Background job system
        jobdef/                # Job definitions and interfaces
        jobworker/             # Job execution engine
        jobtypes/              # Job implementations (email, webhook, aggregation, etc.)
        jobsvc/                # Job service layer
      handlers/                # HTTP handlers (20 domains)
        base/                  # HandlerBase, common utilities
        auth/                  # Authentication and authorization
        badges/                # Badge generation
        checkconnections/      # Check-to-connection mappings
        checkgroups/           # Check group management
        checks/                # Check CRUD and import/export
        checktypes/            # Check type metadata and samples
        connections/           # Connection management
        events/                # Event log
        heartbeat/             # Heartbeat endpoint
        incidents/             # Incident management (escalation, acknowledgment)
        jobs/                  # Job management API
        maintenancewindows/    # Maintenance window scheduling
        members/               # Organization member management
        regions/               # Region definitions
        results/               # Check results and metrics
        statuspages/           # Public status page configuration
        system/                # System info, health checks
        testapi/               # Test-only endpoints
        workers/               # Worker management
      integrations/            # External service integrations
        slack/                 # Slack app integration
        discord/               # Discord bot integration
      notifications/           # 9 notification senders
        slack.go               # Slack notifications
        discord.go             # Discord notifications
        email.go               # Email notifications
        webhook.go             # Webhook notifications
        googlechat.go          # Google Chat notifications
        mattermost.go          # Mattermost notifications
        ntfy.go                # Ntfy push notifications
        opsgenie.go            # Opsgenie alerting
        pushover.go            # Pushover notifications
      db/                      # Database layer
        models/                # Bun ORM models
        postgres/              # PostgreSQL driver and migrations
        sqlite/                # SQLite driver and migrations
        dbctx/                 # Database context utilities
      middleware/              # Auth, CORS, logging, Sentry
      email/                   # Email sending system
      notifier/                # Event notification (PG NOTIFY/LISTEN or in-memory)
      config/                  # koanf configuration
      regions/                 # Region definitions
      mcp/                     # Model Context Protocol server
      state/                   # Application state management
      stats/                   # Statistics collection
      systemconfig/            # System-level configuration
      otelsetup/               # OpenTelemetry setup
      profiler/                # Runtime profiling
      prommetrics/             # Prometheus metrics
      version/                 # Version information
    pkg/
      cli/                     # CLI commands
      client/                  # API client library
  web/
    dash0/                     # Main dashboard (React + TanStack Router)
    status0/                   # Public status page (React)
    dash/                      # Legacy dashboard (deprecated)
  docs/                        # Documentation
  specs/                       # Feature specifications
```

## Monitoring Features

### Check Types (30)

**Network:**
- HTTP/HTTPS: Status code validation, response time, content checks, custom headers
- TCP: Port connectivity, response time
- UDP: UDP connectivity checks
- ICMP: Ping, packet loss, round-trip time
- DNS: Record resolution, response time
- WebSocket: WebSocket connectivity
- SSL: Certificate validation, expiration monitoring
- Domain: WHOIS-based domain expiration tracking

**Email:**
- SMTP: Mail server connectivity and authentication
- POP3: POP3 mailbox access
- IMAP: IMAP mailbox access

**Databases:**
- PostgreSQL, MySQL, MongoDB, MSSQL, Oracle, Redis: Connection and query checks

**Message Queues:**
- Kafka, RabbitMQ, MQTT: Broker connectivity and message checks

**Infrastructure:**
- SSH: Remote server connectivity
- FTP/SFTP: File transfer server checks
- gRPC: gRPC service health
- Docker: Container health monitoring
- SNMP: SNMP device monitoring
- Game Server: Game server query protocol

**Specialized:**
- JavaScript: Custom check logic via JS scripts
- Browser: Full browser-based checks (Playwright)
- Heartbeat: Passive push-based monitoring (services report in)

### Notification Channels (9)

- **Slack**: Rich message formatting with incident details
- **Discord**: Webhook-based notifications
- **Email**: SMTP-based email alerts
- **Webhooks**: Generic HTTP webhook delivery
- **Google Chat**: Google Workspace notifications
- **Mattermost**: Open-source Slack alternative
- **Ntfy**: Push notifications via ntfy.sh
- **Opsgenie**: On-call alerting and escalation
- **Pushover**: Mobile push notifications

### Incident Management
- Automatic incident creation on status changes
- Escalation policies
- Acknowledgment tracking
- Relapse detection (re-opening incidents on repeated failures)
- Incident timeline and event log

### Status Pages
- Public-facing status pages with custom domains
- Sections and resource grouping
- Real-time status updates
- Embedded status page frontend (status0)

### Maintenance Windows
- Scheduled maintenance periods
- Recurrence support (daily, weekly, monthly)
- Automatic check suppression during windows

### Additional Features
- **Check Groups**: Logical grouping of related checks
- **Two-Factor Authentication**: TOTP-based 2FA
- **MCP Support**: Model Context Protocol for AI tool integration
- **Badges**: Embeddable status badges (SVG)
- **Check Import/Export**: Bulk check management
- **Connections**: Reusable connection configurations shared across checks

## Data Model

### Core Entities

#### Organizations
- Multi-tenant isolation boundary
- Unique slug for URL identification
- Soft delete support

#### Users
- Organization members with role-based access (admin, user, viewer)
- Password hash for local authentication
- Optional auth provider link for OAuth
- TOTP-based two-factor authentication

#### Auth Providers
- Per-organization authentication methods
- Supported: email/password, Google, GitHub, GitLab, Microsoft, Slack, Discord
- JSONB configuration for provider-specific settings

#### Workers
- Distributed check executors
- Self-identifying via unique identifier
- Context metadata (e.g., region, environment) for intelligent routing
- Heartbeat mechanism via `last_active_at`

#### Checks
- Monitoring target definitions with 30 protocol types
- JSONB configuration for check-specific parameters
- Configurable frequency (period)
- Enable/disable flag
- Organization-scoped with unique slugs

#### Check Jobs
- Scheduler state for distributed execution
- One-to-one with checks
- Lease-based distribution fields: `lease_worker_uid`, `lease_expires_at`, `lease_starts`
- `context_conditions` (JSONB): Criteria to match on worker context

#### Results
- Time-series monitoring data
- Partitioned by organization
- Period-based aggregation (YYYY, YYYY-MM, YYYY-MM-DD)
- Status tracking: created (1), running (2), up (3), down (4), timeout (5), error (6) — lifecycle order
- Response time metrics: avg/min/max duration
- Availability percentage
- JSONB output for detailed results/errors

#### Incidents
- Automatic creation on check status changes
- Escalation level tracking
- Acknowledgment and resolution timestamps
- Relapse detection

#### Status Pages
- Public status page definitions with sections
- Resource-to-section mappings
- Custom slugs for public URLs

#### Maintenance Windows
- Scheduled maintenance periods with start/end times
- Recurrence rules
- Check association

## Authentication & Authorization

### Authentication Methods
1. **Email/Password**: Local authentication with password hashing
2. **OAuth2**: Google, GitHub, GitLab, Microsoft, Slack, Discord
3. **Personal Access Tokens (PAT)**: Long-lived API access tokens
4. **Two-Factor Authentication**: TOTP-based 2FA

### Authorization
- JWT-based with refresh tokens
- Middleware enforces organization-scoped access
- Role-based permissions (admin, user, viewer)

## API Design Principles

### REST Conventions
- Never return arrays directly (wrap in `data` property)
- Use `$uid` in path parameters
- Use `q` for search/query parameters
- `PATCH` for all update operations
- camelCase for JSON properties and query parameters
- Singular form for multi-value parameters (comma-separated)

### Error Format
All errors return:
```json
{
  "title": "Human-readable description",
  "code": "MACHINE_READABLE_CODE",
  "detail": "Detailed explanation"
}
```

### Standard Error Codes
- `INTERNAL_ERROR` - Unexpected server error
- `VALIDATION_ERROR` - Input validation failed
- `NOT_FOUND` - Resource not found
- `UNAUTHORIZED` - Authentication required
- `FORBIDDEN` - Permission denied
- `CONFLICT` - Resource conflict
- `ORGANIZATION_NOT_FOUND`, `USER_NOT_FOUND`, `CHECK_NOT_FOUND` - Specific resource errors

## Scalability Considerations

### Database
- Organization-based partitioning for results table
- Soft deletes for data recovery
- JSONB for flexible schema evolution
- Efficient indexing on `organization_uid`
- Dual database support (PostgreSQL for production, SQLite for lightweight deployments)

### Workers
- Horizontal scaling via multiple workers
- Lease-based distribution prevents conflicts
- Worker context for intelligent routing (region, environment)

### API
- Stateless design for horizontal scaling
- JWT-based authentication (no session storage)
- Organization-scoped queries for efficient data access

## Event Notification

The `notifier` package provides real-time event propagation:
- **PostgreSQL mode**: Uses `NOTIFY`/`LISTEN` for cross-process communication
- **In-memory mode**: For single-process or SQLite deployments

This enables immediate reaction to check state changes, incident creation, and other system events without polling.

## Configuration Management

### koanf Configuration
- **YAML primary**: `config.yaml` for base configuration
- **Environment overrides**: `SP_` prefix for all environment variables
- **Hierarchy**: defaults -> YAML file -> environment variables

### Key Configuration Sections
```yaml
server:
  listen: ":4000"
db:
  url: "postgres://..."
auth:
  jwt_secret: "..."
notifications:
  slack:
    webhook_url: "..."
```

### Per-Organization Settings
- Stored in `parameters` table with `secret` flag for sensitive values
- Hierarchical: global -> organization -> check-specific

## Development Workflow

1. **Infrastructure**: `docker-compose up -d` for PostgreSQL
2. **Full stack**: `make dev` (backend + dash0 + status0 with hot reload)
3. **Test mode**: `make dev-test` (same but with `SP_RUNMODE=test`)
4. **Database changes**: Add migration, run `make migrate`

## Testing Strategy

### Backend
- Table-driven tests with testcontainers for integration tests
- Separate handler and service test files
- gotestsum for enhanced test output
- `testify/require` for all assertions

### Frontend
- Playwright for end-to-end testing
- Component testing

## Security Considerations

- Password hashing for local auth
- JWT token expiration and refresh
- TOTP-based two-factor authentication
- OAuth2 for third-party authentication
- Organization-scoped data access
- SQL injection prevention via ORM
- Input validation at handler layer
- CORS middleware for browser security
- Sentry integration for error monitoring
