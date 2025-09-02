# Database Model

This document describes all database tables, their purposes, and relationships.

## Entity Relationship Overview

```
organizations (root)
├── parameters (org-scoped config)
├── auth_methods (auth config + optional provider identity)
├── workers (distributed workers)
├── checks
│   ├── labels (via check_labels M2M)
│   ├── check_jobs (1:n per region)
│   ├── results (1:many check results)
│   ├── incidents (1:many downtime periods)
│   └── check_connections (M2M to integrations)
├── integration_connections
│   ├── integration_user_mappings
│   └── check_connections (M2M to checks)
├── jobs (background tasks)
├── events (audit log)
├── state_entries (KV store)
└── organization_members
    └── users
        ├── user_providers (OAuth links)
        └── user_tokens (API tokens)
```

---

## Core Infrastructure Tables

### organizations
Multi-tenant root entity for isolating monitoring resources.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| slug | text | URL-friendly unique identifier (3-20 chars) |
| created_at, updated_at, deleted_at | timestamptz | Timestamps with soft delete |

**Foreign Keys**: None (root entity)

---

### parameters
Key-value configuration storage per organization or system-wide.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations (NULL = system-wide) |
| key | text | Configuration key |
| value | jsonb | Configuration value |
| secret | boolean | Whether value is sensitive/encrypted |

**Foreign Keys**: `organization_uid` → organizations(uid)

---

## Authentication & User Management Tables

### users
Global user accounts with authentication.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| email | text | Globally unique email (case-insensitive) |
| name | text | Display name |
| avatar_url | text | Profile picture URL |
| password_hash | text | Argon2id hash (NULL for SSO-only) |
| email_verified_at | timestamptz | Email verification timestamp |
| super_admin | boolean | Can access all organizations |
| last_active_at | timestamptz | Last login/activity |

**Foreign Keys**: None (global entity)

---

### auth_methods
Authentication methods available to an organization. Combines auth configuration with optional provider identity (e.g., Slack team ID).

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations |
| type | text | Auth method: email-password, google, github, gitlab, microsoft, twitter, oauth2, slack, saml, oidc |
| config | jsonb | Method-specific configuration |
| provider_id | text | External provider ID (e.g., Slack Team ID) - nullable |
| provider_name | text | Human-readable provider name (e.g., "My Slack Workspace") - nullable |

**Foreign Keys**: `organization_uid` → organizations(uid)

**Indexes**:
- Unique on (organization_uid, type) - one method per type per org
- Unique on (type, provider_id) where provider_id is not null - for provider lookups

**Purpose**: Defines what auth methods are available to an org and optionally links to external provider identity. When a user authenticates via Slack OAuth, the `provider_id` (Slack team ID) determines which organization they belong to.

---

### user_providers
Links users to external auth providers (OAuth, SAML, OIDC).

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| user_uid | uuid | FK to users |
| provider_type | text | Provider: google, github, gitlab, microsoft, twitter, slack, saml, oidc |
| provider_id | text | External identifier (e.g., OAuth sub claim) |
| metadata | jsonb | Provider-specific data (profile, tokens) |

**Foreign Keys**: `user_uid` → users(uid)

**Purpose**: Authentication identity - "How does this user authenticate to SolidPing?"

---

### organization_members
Links users to organizations with role-based access control.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| user_uid | uuid | FK to users |
| organization_uid | uuid | FK to organizations |
| role | text | Role: admin, user, viewer |
| invited_by_uid | uuid | FK to users (who invited) |
| invited_at | timestamptz | Invitation timestamp |
| joined_at | timestamptz | Acceptance timestamp (NULL = pending) |

**Foreign Keys**:
- `user_uid` → users(uid)
- `organization_uid` → organizations(uid)
- `invited_by_uid` → users(uid)

---

### user_tokens
Personal Access Tokens (PAT) and refresh tokens.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| user_uid | uuid | FK to users |
| organization_uid | uuid | FK to organizations (for PAT scope) |
| token | text | The actual token string |
| type | text | Token type: pat, refresh |
| properties | jsonb | Token metadata (name, scopes) |
| expires_at | timestamptz | Token expiration |
| last_active_at | timestamptz | Last usage |

**Foreign Keys**:
- `user_uid` → users(uid)
- `organization_uid` → organizations(uid)

---

## Monitoring Configuration Tables

### workers
Distributed workers that execute monitoring checks.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| slug | text | Unique system identifier |
| name | text | Human-readable name |
| region | text | Region identifier (e.g., eu-west-1) |
| last_active_at | timestamptz | Last heartbeat |

**Foreign Keys**: None

---

### checks
Monitoring configurations that define what to monitor.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations |
| name | text | Check name |
| slug | text | URL-friendly identifier (per org) |
| description | text | Documentation |
| type | text | Check type: http, tcp, icmp, dns, ssl, etc. |
| config | jsonb | Check-specific configuration |
| regions | text[] | Regions where check runs |
| enabled | boolean | Whether active |
| period | interval | Check frequency (default: 1 minute) |
| incident_threshold | integer | Failures before incident |
| escalation_threshold | integer | Failures before escalation |
| recovery_threshold | integer | Successes before recovery |
| status | smallint | Current: 0=unknown, 1=up, 2=down, 3=degraded |
| status_streak | integer | Current status streak count |
| status_changed_at | timestamptz | Last status change |

**Foreign Keys**: `organization_uid` → organizations(uid)

---

### labels
Key-value pairs for organizing and filtering checks.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations |
| key | text | Label key (1-50 chars) |
| value | text | Label value (max 200 chars) |

**Foreign Keys**: `organization_uid` → organizations(uid)

---

### check_labels
Many-to-many relationship between checks and labels.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| check_uid | uuid | FK to checks |
| label_uid | uuid | FK to labels |

**Foreign Keys**:
- `check_uid` → checks(uid)
- `label_uid` → labels(uid)

---

## Job Scheduling Tables

### check_jobs
Scheduled execution jobs for checks with worker lease tracking.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations |
| check_uid | uuid | FK to checks (1:n, one job per region) |
| region | text | Specific region for execution |
| type | text | Check type (copied for performance) |
| config | jsonb | Check configuration |
| encrypted | boolean | Whether config is encrypted |
| period | interval | Execution interval |
| scheduled_at | timestamptz | Next scheduled execution |
| lease_worker_uid | uuid | FK to workers (assigned worker) |
| lease_expires_at | timestamptz | Lease expiration |
| lease_starts | smallint | Execution attempt counter |

**Foreign Keys**:
- `organization_uid` → organizations(uid)
- `check_uid` → checks(uid)
- `lease_worker_uid` → workers(uid)

---

### jobs
Background tasks scheduled for asynchronous execution.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations (optional) |
| type | text | Job type: email, webhook, check-run |
| config | jsonb | Job configuration |
| retry_count | integer | Retry attempts |
| scheduled_at | timestamptz | Execution time |
| status | text | pending, running, success, retried, failed |
| output | jsonb | Execution output |
| previous_job_uid | uuid | FK to jobs (retry chain) |

**Foreign Keys**:
- `organization_uid` → organizations(uid)
- `previous_job_uid` → jobs(uid) (self-reference)

---

## Monitoring Results & Incidents Tables

### results
Time-series check execution results (raw and aggregated).

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations |
| check_uid | uuid | FK to checks |
| period_type | text | Granularity: raw, hour, day, month, year |
| period_start | timestamptz | Period start |
| period_end | timestamptz | Period end |
| region | text | Execution region |
| worker_uid | uuid | FK to workers (raw only) |
| status | smallint | 0=initial, 1=up, 2=down, 3=timeout, 4=error |
| duration | real | Execution duration in seconds |
| metrics | jsonb | Numerical metrics |
| output | jsonb | Diagnostic output |
| last_for_status | boolean | Most recent per check+status |
| total_checks | integer | Total count (aggregated) |
| successful_checks | integer | Success count (aggregated) |
| availability_pct | double | Uptime percentage (aggregated) |
| duration_min/max/p95 | real | Duration stats (aggregated) |

**Foreign Keys**:
- `organization_uid` → organizations(uid)
- `check_uid` → checks(uid)
- `worker_uid` → workers(uid)

---

### incidents
Tracks periods when a check is down.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations |
| check_uid | uuid | FK to checks |
| region | text | Region where incident occurred |
| state | smallint | 1=active, 2=resolved |
| started_at | timestamptz | When failures started |
| resolved_at | timestamptz | When recovered (NULL = ongoing) |
| escalated_at | timestamptz | When escalation triggered |
| acknowledged_at | timestamptz | When acknowledged |
| acknowledged_by | uuid | FK to users |
| failure_count | integer | Total failures during incident |
| title | text | Auto-generated title |
| description | text | Human-readable description |
| details | jsonb | Additional data |

**Foreign Keys**:
- `organization_uid` → organizations(uid)
- `check_uid` → checks(uid)
- `acknowledged_by` → users(uid)

---

## Audit & Event Logging Tables

### events
Audit log for incident lifecycle and system events.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations |
| incident_uid | uuid | FK to incidents (optional) |
| check_uid | uuid | FK to checks (optional) |
| job_uid | uuid | FK to jobs (optional) |
| event_type | varchar | Event type: check.created, incident.created, etc. |
| actor_type | varchar | system or user |
| actor_uid | uuid | FK to users (optional) |
| payload | jsonb | Event-specific data |

**Foreign Keys**:
- `organization_uid` → organizations(uid)
- `incident_uid` → incidents(uid)
- `check_uid` → checks(uid)
- `actor_uid` → users(uid)

---

## State & Configuration Tables

### state_entries
Key-value state storage for notifications, tokens, and distributed locking.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations (optional) |
| user_uid | uuid | FK to users (optional) |
| key | text | Namespaced key (e.g., email_confirm/{token}) |
| value | jsonb | JSON value |
| expires_at | timestamptz | Optional TTL |

**Foreign Keys**:
- `organization_uid` → organizations(uid)
- `user_uid` → users(uid)

---

## Integration & Notification Tables

### integration_connections
Generic table for all integration connections (Slack, webhook, email, etc.).

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations |
| type | varchar | Integration type: slack, webhook, email, betterstack |
| name | varchar | Human-readable name |
| enabled | boolean | Whether active |
| is_default | boolean | Auto-attach to new checks |
| settings | jsonb | Type-specific configuration |

**Foreign Keys**: `organization_uid` → organizations(uid)

---

### integration_user_mappings
Links external users (e.g., Slack users) to SolidPing users.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations |
| connection_uid | uuid | FK to integration_connections |
| connection_user_id | text | User ID on remote system |
| properties | jsonb | User properties (email, name, avatar) |
| user_uid | uuid | FK to users (optional) |

**Foreign Keys**:
- `organization_uid` → organizations(uid)
- `connection_uid` → integration_connections(uid)
- `user_uid` → users(uid)

**Purpose**: Integration data mapping - "Which external user maps to which SolidPing user for notifications?"

**Note**: This is distinct from `user_providers`:
- `user_providers` = authentication identity (required user link, global)
- `integration_user_mappings` = integration mapping (optional user link, per-connection)

---

### check_connections
Junction table linking checks to integration connections for notifications.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| check_uid | uuid | FK to checks |
| connection_uid | uuid | FK to integration_connections |
| organization_uid | uuid | FK to organizations |

**Foreign Keys**:
- `check_uid` → checks(uid)
- `connection_uid` → integration_connections(uid)
- `organization_uid` → organizations(uid)

---

## Design Patterns

1. **Soft Deletes**: Most tables have `deleted_at` for recovery
2. **Timestamps**: All tables track `created_at` and `updated_at`
3. **Organization Scoping**: All operational data is scoped to organizations
4. **UUID Primary Keys**: All tables use UUID for distributed system compatibility
5. **JSONB for Flexibility**: config, settings, metadata, output stored as JSONB
6. **Lease-based Job Distribution**: check_jobs uses worker leases for distributed execution
7. **Time-series Results**: results table supports both raw and aggregated data

---

## File Locations

- **PostgreSQL migrations**: `server/internal/db/postgres/migrations/`
- **SQLite migrations**: `server/internal/db/sqlite/migrations/`
- **Go models**: `server/internal/db/models/`
