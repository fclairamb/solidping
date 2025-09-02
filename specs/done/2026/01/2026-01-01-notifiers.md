# Notification System

## Overview

A notification system that sends alerts when incidents occur. Built on top of:
- **`integration_connections`** - unified table for all external integrations
- **`integration_user_mappings`** - links external users to SolidPing users
- **`jobs`** - existing job queue with `type = "notification"`

## Implementation Status

| Component | Status |
|-----------|--------|
| `integration_connections` table | Implemented |
| `integration_user_mappings` table | Implemented |
| Connections CRUD API | Implemented |
| Slack OAuth integration | Implemented |
| `check_connections` table | Not Yet Implemented |
| Check-Connection API endpoints | Not Yet Implemented |
| Notification job type | Not Yet Implemented |
| Notifier interface & senders | Not Yet Implemented |
| Incident notification hooks | Not Yet Implemented |

## Goals

1. Notify users when an incident is **created** (check goes down)
2. Notify users when an incident is **resolved** (check recovers)
3. Support multiple notification channels (Email, Slack, Webhook)
4. Allow checks to be associated with specific connections
5. Provide a default connection mechanism for new checks

---

## Data Model

### `integration_connections` table (Implemented)

Stores all external integration connections.

| Column | Type | Description |
|--------|------|-------------|
| `uid` | UUID | Primary key |
| `organization_uid` | UUID | Foreign key to organizations |
| `type` | VARCHAR(50) | `slack`, `webhook`, `email` |
| `name` | VARCHAR(255) | Human-readable name (e.g., "Ops Team Slack") |
| `enabled` | BOOLEAN | Whether the connection is active (default: true) |
| `is_default` | BOOLEAN | If true, auto-attach to new checks (default: false) |
| `settings` | JSONB | Type-specific configuration |
| `created_at` | TIMESTAMP | Creation timestamp |
| `updated_at` | TIMESTAMP | Last update timestamp |
| `deleted_at` | TIMESTAMP | Soft delete |

**Indexes:**
- `idx_integration_connections_org_type` - Filter by organization and type
- `idx_integration_connections_org_default` - Find default connections
- `idx_integration_connections_settings_team_id` - Lookup Slack connections by team ID (PostgreSQL only)

### `integration_user_mappings` table (Implemented)

Links external users (e.g., Slack users) to SolidPing users.

| Column | Type | Description |
|--------|------|-------------|
| `uid` | UUID | Primary key |
| `organization_uid` | UUID | Foreign key to organizations |
| `connection_uid` | UUID | Foreign key to integration_connections |
| `connection_user_id` | VARCHAR | User ID on remote system (e.g., Slack user ID) |
| `properties` | JSONB | User properties from remote system |
| `user_uid` | UUID | Foreign key to users (optional) |
| `created_at` | TIMESTAMP | Creation timestamp |
| `updated_at` | TIMESTAMP | Last update timestamp |

**Indexes:**
- Unique on `(connection_uid, connection_user_id)`

### `check_connections` table (Not Yet Implemented)

Links checks to connections for notification purposes.

| Column | Type | Description |
|--------|------|-------------|
| `uid` | UUID | Primary key |
| `check_uid` | UUID | Foreign key to checks |
| `connection_uid` | UUID | Foreign key to integration_connections |
| `created_at` | TIMESTAMP | When the association was created |

**Constraints:**
- Unique on `(check_uid, connection_uid)`

### `jobs` table (existing)

Notification jobs use the existing job infrastructure.

```json
{
  "type": "notification",
  "config": {
    "connection_uid": "uuid-of-connection",
    "incident_uid": "uuid-of-incident",
    "event_type": "incident_created"
  }
}
```

The `Output` field stores the result:
```json
{
  "sent_at": "2026-01-01T12:00:00Z",
  "response": { ... }
}
```

Or on failure:
```json
{
  "error": "Connection timeout",
  "last_attempt_at": "2026-01-01T12:00:00Z"
}
```

---

## Connection Settings by Type

### Email

```json
{
  "recipients": ["ops@example.com", "oncall@example.com"],
  "subject_prefix": "[SolidPing]"
}
```

### Slack (Implemented via OAuth)

OAuth-based connection with tokens stored from OAuth flow.

```json
{
  "team_id": "T0123456789",
  "team_name": "Acme Corp",
  "bot_user_id": "U0123456789",
  "access_token": "xoxb-...",
  "installed_by_user_id": "U0123456789",
  "scopes": ["chat:write", "channels:read", ...],
  "default_channel_id": "C0123456789",
  "default_channel_name": "#alerts"
}
```

### Webhook

```json
{
  "url": "https://example.com/webhook",
  "method": "POST",
  "headers": {
    "Authorization": "Bearer secret-token"
  },
  "secret": "optional-hmac-secret"
}
```

---

## Models (Implemented)

Located in `internal/db/models/integration.go`:

```go
// ConnectionType represents the type of integration connection.
type ConnectionType string

const (
    ConnectionTypeSlack   ConnectionType = "slack"
    ConnectionTypeWebhook ConnectionType = "webhook"
    ConnectionTypeEmail   ConnectionType = "email"
)

// IntegrationConnection represents a connection to an external integration.
type IntegrationConnection struct {
    UID             string         `bun:"uid,pk"`
    OrganizationUID string         `bun:"organization_uid,notnull"`
    Type            ConnectionType `bun:"type,notnull"`
    Name            string         `bun:"name,notnull"`
    Enabled         bool           `bun:"enabled,notnull,default:true"`
    IsDefault       bool           `bun:"is_default,notnull,default:false"`
    Settings        JSONMap        `bun:"settings,type:jsonb,notnull"`
    CreatedAt       time.Time      `bun:"created_at"`
    UpdatedAt       time.Time      `bun:"updated_at"`
    DeletedAt       *time.Time     `bun:"deleted_at"`
}

// IntegrationUserMapping links external users to SolidPing users.
type IntegrationUserMapping struct {
    UID              string    `bun:"uid,pk"`
    OrganizationUID  string    `bun:"organization_uid,notnull"`
    ConnectionUID    string    `bun:"connection_uid,notnull"`
    ConnectionUserID string    `bun:"connection_user_id,notnull"`
    Properties       JSONMap   `bun:"properties,type:jsonb,notnull"`
    UserUID          *string   `bun:"user_uid"`
    CreatedAt        time.Time `bun:"created_at"`
    UpdatedAt        time.Time `bun:"updated_at"`
}

// SlackSettings represents Slack-specific settings stored in the Settings JSONB.
type SlackSettings struct {
    TeamID             string   `json:"team_id"`
    TeamName           string   `json:"team_name"`
    BotUserID          string   `json:"bot_user_id"`
    AccessToken        string   `json:"access_token"`
    DefaultChannelID   string   `json:"default_channel_id,omitempty"`
    DefaultChannelName string   `json:"default_channel_name,omitempty"`
    InstalledByUserID  string   `json:"installed_by_user_id"`
    Scopes             []string `json:"scopes"`
}
```

---

## API Endpoints

### Connections (Implemented)

Located in `internal/handlers/connections/`:

| Method | Endpoint | Description | Status |
|--------|----------|-------------|--------|
| GET | `/api/v1/orgs/:org/connections` | List all connections | Implemented |
| POST | `/api/v1/orgs/:org/connections` | Create connection | Implemented |
| GET | `/api/v1/orgs/:org/connections/:uid` | Get connection | Implemented |
| PATCH | `/api/v1/orgs/:org/connections/:uid` | Update connection | Implemented |
| DELETE | `/api/v1/orgs/:org/connections/:uid` | Delete connection | Implemented |

Query params: `?type=slack` to filter by type.

**Create Request:**
```json
{
  "type": "webhook",
  "name": "PagerDuty Webhook",
  "enabled": true,
  "isDefault": false,
  "settings": {
    "url": "https://events.pagerduty.com/...",
    "method": "POST"
  }
}
```

**Response:**
```json
{
  "uid": "uuid",
  "type": "webhook",
  "name": "PagerDuty Webhook",
  "enabled": true,
  "isDefault": false,
  "settings": { ... },
  "createdAt": "2026-01-01T12:00:00Z",
  "updatedAt": "2026-01-01T12:00:00Z"
}
```

Note: `access_token` is filtered from settings in API responses for security.

### Slack Integration (Implemented)

Located in `internal/integrations/slack/`:

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/v1/integrations/slack/oauth` | OAuth callback (redirects to frontend) |
| POST | `/api/v1/integrations/slack/events` | Slack Events API webhook |
| POST | `/api/v1/integrations/slack/command` | Slack slash command handler |
| POST | `/api/v1/integrations/slack/interaction` | Slack interactive components handler |

All `/events`, `/command`, and `/interaction` endpoints use request verification middleware.

### Check-Connection Associations (Not Yet Implemented)

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/v1/orgs/:org/checks/:uid/connections` | List connections for a check |
| PUT | `/api/v1/orgs/:org/checks/:uid/connections` | Replace all connections for a check |
| POST | `/api/v1/orgs/:org/checks/:uid/connections/:connection_uid` | Add connection to check |
| DELETE | `/api/v1/orgs/:org/checks/:uid/connections/:connection_uid` | Remove connection from check |

### Notification History (via jobs, Not Yet Implemented)

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/v1/orgs/:org/jobs?type=notification` | List notification jobs |
| GET | `/api/v1/orgs/:org/jobs/:uid` | Get job details |

---

## Notification Workflow (Not Yet Implemented)

```
┌─────────────┐
│ Check fails │
│ (threshold  │
│   reached)  │
└──────┬──────┘
       │
       ▼
┌─────────────┐
│  Incident   │
│  created    │
└──────┬──────┘
       │
       ▼
┌───────────────────────────────────┐
│ Query check_connections           │
│ JOIN integration_connections      │
│ WHERE enabled = true              │
│                                   │
│ For each connection:              │
│   → Create job with               │
│     type = "notification"         │
└───────────────┬───────────────────┘
                │
                ▼
┌───────────────────────────────────┐
│ Job Worker (existing)             │
│  1. Pick pending notification job │
│  2. Load connection settings      │
│  3. Load incident + check data    │
│  4. Call Notifier.Send()          │
│  5. Update job status             │
└───────────────────────────────────┘
```

### Retry Strategy

Uses existing job retry mechanism:
- Max retries: 3
- Backoff: 30s, 2min, 10min
- After final failure: `status = failed`

---

## Notifier Interface (Not Yet Implemented)

```go
package notifiers

type EventType string

const (
    EventTypeIncidentCreated  EventType = "incident_created"
    EventTypeIncidentResolved EventType = "incident_resolved"
)

// SendRequest contains all information needed to send a notification.
type SendRequest struct {
    // Connection settings (type-specific)
    Connection *models.IntegrationConnection

    // The incident that triggered this notification
    Incident *models.Incident

    // The check associated with the incident
    Check *models.Check

    // The result that caused the status change (may be nil for resolved)
    Result *models.Result

    // What happened
    EventType EventType
}

// Sender defines the interface for sending notifications.
type Sender interface {
    // Type returns the connection type this sender handles (email, slack, webhook)
    Type() string

    // Send delivers the notification. Returns error if failed.
    Send(ctx context.Context, req *SendRequest) error
}

// Registry maps connection types to senders.
type Registry struct {
    senders map[string]Sender
}

func (r *Registry) Send(ctx context.Context, req *SendRequest) error {
    sender, ok := r.senders[req.Connection.Type]
    if !ok {
        return fmt.Errorf("unknown connection type: %s", req.Connection.Type)
    }
    return sender.Send(ctx, req)
}
```

---

## Existing Job Types (Related)

The following job types already exist and can be reused:

- **`email`** - Sends email via configured SMTP (`internal/jobs/jobtypes/job_email.go`)
- **`webhook`** - Sends HTTP webhooks (`internal/jobs/jobtypes/job_webhook.go`)

The notification job type would wrap these with incident-specific context.

---

## Default Connection Behavior (Not Yet Implemented)

When a check is created:

1. Query all connections where `is_default = true` AND `enabled = true`
2. Create `check_connections` entries linking the new check to each
3. User can modify associations afterward via API

---

## Implementation Order

1. ~~`integration_connections` table~~ ✓
2. ~~`integration_user_mappings` table~~ ✓
3. ~~Models for IntegrationConnection and IntegrationUserMapping~~ ✓
4. ~~Connections CRUD handler~~ ✓
5. ~~Slack OAuth integration~~ ✓
6. Create `check_connections` table migration
7. Models for `CheckConnection`
8. Check-connection API handlers
9. Notification job type handler in `jobtypes/`
10. Sender interface and registry
11. Webhook sender implementation
12. Slack sender implementation (uses existing OAuth tokens)
13. Email sender implementation (wraps existing email job)
14. Hook into incident creation/resolution to queue notification jobs
15. Default connection auto-attachment on check creation

---

## Example Webhook Payload (Planned)

```json
{
  "event": "incident_created",
  "timestamp": "2026-01-01T12:00:00Z",
  "incident": {
    "uid": "abc-123",
    "started_at": "2026-01-01T11:58:00Z",
    "title": "Check failed: api-health"
  },
  "check": {
    "uid": "def-456",
    "name": "API Health Check",
    "slug": "api-health",
    "type": "http"
  },
  "result": {
    "status": "down",
    "duration_ms": 5000,
    "error": "Connection timeout"
  }
}
```

---

## File Locations

### Implemented

| Component | Location |
|-----------|----------|
| IntegrationConnection model | `internal/db/models/integration.go` |
| IntegrationUserMapping model | `internal/db/models/integration.go` |
| SlackSettings helper | `internal/db/models/integration.go` |
| DB service methods | `internal/db/service.go` (interface) |
| PostgreSQL implementation | `internal/db/postgres/postgres.go` |
| SQLite implementation | `internal/db/sqlite/sqlite.go` |
| Connections handler | `internal/handlers/connections/handler.go` |
| Connections service | `internal/handlers/connections/service.go` |
| Slack handler | `internal/integrations/slack/handler.go` |
| Slack service | `internal/integrations/slack/service.go` |
| Slack client | `internal/integrations/slack/client.go` |
| Slack types | `internal/integrations/slack/types.go` |
| Route registration | `internal/app/server.go` |

### Planned

| Component | Planned Location |
|-----------|------------------|
| CheckConnection model | `internal/db/models/check_connection.go` |
| Notification job type | `internal/jobs/jobtypes/job_notification.go` |
| Notifier registry | `internal/notifiers/registry.go` |
| Slack sender | `internal/notifiers/slack.go` |
| Webhook sender | `internal/notifiers/webhook.go` |
| Email sender | `internal/notifiers/email.go` |
