# Database Model

This document describes all database tables, their purposes, and relationships.

## Entity Relationship Overview

```
organizations (root)
├── parameters (org-scoped config)
├── organization_providers (external identity providers)
├── workers (distributed workers)
├── check_groups (flat check grouping)
├── checks
│   ├── labels (via check_labels M2M)
│   ├── check_jobs (1:n per region)
│   ├── results (1:many check results)
│   ├── incidents (1:many downtime periods)
│   └── check_connections (M2M to integrations)
├── integration_connections
│   └── check_connections (M2M to checks)
├── status_pages
│   └── status_page_sections
│       └── status_page_resources (links to checks)
├── maintenance_windows
│   └── maintenance_window_checks (links to checks or check_groups)
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
| name | text | Human-readable display name |
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
| totp_secret | text | Base32-encoded TOTP secret for 2FA (NULL if not configured) |
| totp_enabled | boolean | Whether TOTP two-factor authentication is active |
| totp_recovery_codes | jsonb | JSON array of hashed one-time recovery codes for 2FA bypass |

**Foreign Keys**: None (global entity)

---

### organization_providers
Maps organizations to external identity providers. One provider identity belongs to exactly one org.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations |
| provider_type | text | Provider type: slack, google, github, gitlab, microsoft, saml, oidc |
| provider_id | text | Unique identifier from the provider (e.g., Slack Team ID) |
| provider_name | text | Human-readable provider name (e.g., "Acme Corp Slack Workspace") |
| metadata | jsonb | Provider-specific metadata |

**Foreign Keys**: `organization_uid` → organizations(uid)

**Indexes**:
- Unique on (provider_type, provider_id) where not deleted - for provider lookups
- Index on organization_uid where not deleted

**Purpose**: Defines which external identity provider identities belong to which organization. When a user authenticates via Slack OAuth, the `provider_id` (Slack team ID) determines which organization they belong to.

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
| token | text | Authentication token for edge worker registration (NULL for manually registered) |
| last_active_at | timestamptz | Last heartbeat |

**Foreign Keys**: None

---

### check_groups
Flat organizational grouping for checks. A check belongs to zero or one group.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations |
| name | text | Display name for the group |
| slug | text | URL-friendly identifier, unique per organization |
| description | text | Optional description of what this group contains |
| sort_order | smallint | Display order (lower = higher). Default 0 |

**Foreign Keys**: `organization_uid` → organizations(uid)

---

### checks
Monitoring configurations that define what to monitor.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations |
| check_group_uid | uuid | FK to check_groups (NULL means ungrouped) |
| name | text | Check name |
| slug | text | URL-friendly identifier (per org) |
| description | text | Documentation |
| type | text | Check type: http, tcp, ping, dns, ssl, etc. |
| config | jsonb | Check-specific configuration |
| regions | text[] | Regions where check runs |
| enabled | boolean | Whether active |
| internal | boolean | Internal checks are hidden from public status pages |
| period | interval | Check frequency (default: 1 minute) |
| incident_threshold | integer | Failures before incident |
| escalation_threshold | integer | Failures before escalation |
| recovery_threshold | integer | Successes before recovery |
| reopen_cooldown_multiplier | integer | Multiplier for adaptive cooldown before reopening (NULL = system default) |
| max_adaptive_increase | integer | Maximum multiplier for adaptive resolution increase (NULL = system default) |
| status | smallint | Current: 1=created, 3=up, 4=down, 7=degraded |
| status_streak | integer | Current status streak count |
| status_changed_at | timestamptz | Last status change |

**Foreign Keys**:
- `organization_uid` → organizations(uid)
- `check_group_uid` → check_groups(uid)

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
| status | smallint | Lifecycle order: 1=created, 2=running, 3=up, 4=down, 5=timeout, 6=error |
| duration | real | Execution duration in milliseconds |
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
| relapse_count | integer | Number of times reopened after brief recoveries |
| last_reopened_at | timestamptz | When last reopened (NULL if never reopened) |
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
| type | varchar | Integration type: slack, discord, webhook, email, googlechat, mattermost, ntfy, opsgenie, pushover |
| name | varchar | Human-readable name |
| enabled | boolean | Whether active |
| is_default | boolean | Auto-attach to new checks |
| settings | jsonb | Type-specific configuration |

**Foreign Keys**: `organization_uid` → organizations(uid)

---

### check_connections
Junction table linking checks to integration connections for notifications.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| check_uid | uuid | FK to checks |
| connection_uid | uuid | FK to integration_connections |
| organization_uid | uuid | FK to organizations |
| settings | jsonb | Per-check override settings (e.g., Slack channel override) |

**Foreign Keys**:
- `check_uid` → checks(uid)
- `connection_uid` → integration_connections(uid)
- `organization_uid` → organizations(uid)

---

## Status Page Tables

### status_pages
Public-facing status pages displaying service health to end users.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations |
| name | text | Page title displayed to visitors |
| slug | text | URL-friendly identifier, unique per organization |
| description | text | Subtitle or description shown on the page |
| visibility | text | Access control: public or private |
| is_default | boolean | At most one default page per org |
| enabled | boolean | Whether the page is accessible |
| show_availability | boolean | Whether to display uptime percentage |
| show_response_time | boolean | Whether to display response time charts |
| history_days | integer | Number of days of history to display (default 90) |
| language | varchar(10) | ISO language code (e.g., en, fr). NULL uses system default |

**Foreign Keys**: `organization_uid` → organizations(uid)

---

### status_page_sections
Grouping sections within a status page.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| status_page_uid | uuid | FK to status_pages |
| name | text | Section heading displayed on the page |
| slug | text | URL-friendly identifier, unique per status page |
| position | integer | Display order (lower = higher on page) |

**Foreign Keys**: `status_page_uid` → status_pages(uid)

---

### status_page_resources
Checks displayed within a status page section.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| section_uid | uuid | FK to status_page_sections |
| check_uid | uuid | FK to checks |
| public_name | text | Override display name (NULL uses check name) |
| explanation | text | Optional description visible on the public page |
| position | integer | Display order within section (lower = higher) |

**Foreign Keys**:
- `section_uid` → status_page_sections(uid)
- `check_uid` → checks(uid)

---

## Maintenance Tables

### maintenance_windows
Scheduled maintenance periods that suppress incident alerts for affected checks.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| organization_uid | uuid | FK to organizations |
| title | text | Maintenance window title shown in notifications and status pages |
| description | text | Detailed description of the planned maintenance |
| start_at | timestamptz | When the maintenance window begins |
| end_at | timestamptz | When the maintenance window ends (must be after start_at) |
| recurrence | text | Recurrence pattern: none, daily, weekly, monthly |
| recurrence_end | timestamptz | When the recurring schedule stops (NULL = indefinite) |
| created_by | text | Identifier of the user or system that created this window |

**Foreign Keys**: `organization_uid` → organizations(uid)

---

### maintenance_window_checks
Links maintenance windows to individual checks or check groups. Exactly one of check_uid or check_group_uid must be set.

| Column | Type | Description |
|--------|------|-------------|
| uid | uuid PK | Primary key |
| maintenance_window_uid | uuid | FK to maintenance_windows |
| check_uid | uuid | FK to checks (NULL if targeting a group) |
| check_group_uid | uuid | FK to check_groups (NULL if targeting an individual check) |

**Foreign Keys**:
- `maintenance_window_uid` → maintenance_windows(uid)
- `check_uid` → checks(uid)
- `check_group_uid` → check_groups(uid)

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
