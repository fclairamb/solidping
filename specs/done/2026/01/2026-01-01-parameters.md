# System Parameters Specification

## Overview

This specification describes how to store and manage system-wide configuration parameters in the database, allowing for persistent configuration that survives restarts while still supporting environment variable overrides.

## Problem Statement

Currently, all configuration is loaded from:
1. Hardcoded defaults in `config.go`
2. Optional `config.yaml` file
3. Environment variables with `SP_` prefix

This works well for static deployments but has limitations:
- **JWT secret**: Defaults to `"change-me-in-production"`, which is insecure. Users must manually set it via environment variable.
- **Worker counts**: Cannot be changed without redeploying or restarting the application.
- **Email configuration**: Must be configured via environment variables, which can be cumbersome for UI-based setup.
- **Base URL**: Required for generating links in emails/notifications, currently not configurable.

## Solution

Reuse the existing `parameters` table by making `organization_uid` nullable. When `organization_uid` is NULL, the parameter is a system-wide (global) configuration. This approach:
- Avoids creating a new table
- Reuses existing infrastructure (model, service methods)
- Maintains a single source of truth for all parameters

## Configuration Precedence

Values are resolved in the following order (highest to lowest priority):

```
1. Environment variables (SP_*)                          ← Always wins
2. Database (parameters WHERE organization_uid IS NULL)  ← Persistent storage
3. Hardcoded defaults                                    ← Fallback
```

This ensures:
- Environment variables can always override database values (useful for containerized deployments)
- Database values persist across restarts without needing to set environment variables
- Sensible defaults when nothing else is configured

## Parameters

### Worker Configuration

| Parameter | DB Key | Env Variable | Default | Description |
|-----------|--------|--------------|---------|-------------|
| Job Workers | `job_workers` | `SP_SERVER_JOB_WORKER_NB` | `2` | Number of concurrent job runner goroutines |
| Check Workers | `check_workers` | `SP_SERVER_CHECK_WORKER_NB` | `3` | Number of concurrent check runner goroutines |

### Authentication

| Parameter | DB Key | Env Variable | Default | Description |
|-----------|--------|--------------|---------|-------------|
| JWT Secret | `jwt_secret` | `SP_AUTH_JWT_SECRET` | (auto-generated) | Secret key for signing JWT tokens |

**Auto-generation behavior:**
- On first startup, if `jwt_secret` is not set in environment OR database, generate a cryptographically secure random string (32 bytes, base64 encoded)
- Save the generated secret to the database
- Log a warning: `"JWT secret auto-generated and saved to database"`
- This ensures the secret persists across restarts without manual configuration

### Email (SMTP)

| Parameter | DB Key | Env Variable | Default | Description |
|-----------|--------|--------------|---------|-------------|
| SMTP Host | `email.host` | `SP_EMAIL_HOST` | `""` | SMTP server hostname |
| SMTP Port | `email.port` | `SP_EMAIL_PORT` | `587` | SMTP server port |
| SMTP Username | `email.username` | `SP_EMAIL_USERNAME` | `""` | SMTP authentication username |
| SMTP Password | `email.password` | `SP_EMAIL_PASSWORD` | `""` | SMTP authentication password |
| From Address | `email.from` | `SP_EMAIL_FROM` | `""` | Default sender email address |
| From Name | `email.from_name` | `SP_EMAIL_FROMNAME` | `""` | Display name for sender |
| Enabled | `email.enabled` | `SP_EMAIL_ENABLED` | `false` | Enable/disable email sending |
| Auth Type | `email.auth_type` | `SP_EMAIL_AUTHTYPE` | `"login"` | SMTP auth: `plain`, `login`, `cram-md5` |
| Skip TLS Verify | `email.insecure_skip_verify` | `SP_EMAIL_INSECURESKIPVERIFY` | `false` | Skip TLS certificate verification |

### Application

| Parameter | DB Key | Env Variable | Default | Description |
|-----------|--------|--------------|---------|-------------|
| Base URL | `base_url` | `SP_BASE_URL` | `""` | Public URL of the application (e.g., `https://solidping.example.com`) |

## Database Schema

### Updated Table: `parameters`

**IMPORTANT: Update the existing migration file** (`20251207000001_initial.up.sql`) instead of creating a new migration. The change is to make `organization_uid` nullable:

```sql
-- PostgreSQL (back/internal/db/postgres/migrations/20251207000001_initial.up.sql)
create table parameters (
    uid               uuid primary key default gen_random_uuid(),
    organization_uid  uuid references organizations(uid) on delete cascade,  -- NOW NULLABLE
    key               text not null check (key ~ '^[a-z0-9_\.]+$'),
    value             jsonb not null,
    secret            boolean,
    created_at        timestamptz not null default now(),
    updated_at        timestamptz not null default now(),
    deleted_at        timestamptz
);

-- Unique index for organization-scoped parameters
create unique index on parameters (organization_uid, key) where deleted_at is null and organization_uid is not null;

-- Unique index for system parameters (organization_uid IS NULL)
create unique index on parameters (key) where deleted_at is null and organization_uid is null;
```

**Key changes to existing migration:**
1. Remove `NOT NULL` constraint from `organization_uid`
2. Split the unique index into two: one for org-scoped params, one for system params
3. Apply same changes to SQLite migration (`back/internal/db/sqlite/migrations/20251207000001_initial.up.sql`)

### Model

Update the existing `Parameter` model to use a pointer type for `OrganizationUID`:

```go
// Parameter represents a key-value configuration.
// When OrganizationUID is nil, this is a system-wide parameter.
type Parameter struct {
    UID             string     `bun:"uid,pk,type:varchar(36)"`
    OrganizationUID *string    `bun:"organization_uid"`  // nil = system parameter
    Key             string     `bun:"key,notnull"`
    Value           JSONMap    `bun:"value,type:jsonb,notnull"`
    Secret          *bool      `bun:"secret"`
    CreatedAt       time.Time  `bun:"created_at,notnull,default:current_timestamp"`
    UpdatedAt       time.Time  `bun:"updated_at,notnull,default:current_timestamp"`
    DeletedAt       *time.Time `bun:"deleted_at"`
}
```

## Implementation

### Configuration Loading Flow

```
┌─────────────────────┐
│   Application Start │
└──────────┬──────────┘
           │
           ▼
┌─────────────────────┐
│  Load defaults from │
│     config.go       │
└──────────┬──────────┘
           │
           ▼
┌─────────────────────┐
│  Load from database │
│ (parameters where   │
│  org_uid IS NULL)   │
└──────────┬──────────┘
           │
           ▼
┌─────────────────────┐
│ Override with env   │
│ variables (SP_*)    │
└──────────┬──────────┘
           │
           ▼
┌─────────────────────┐
│  Auto-generate JWT  │
│  secret if missing  │
│  (save to database) │
└──────────┬──────────┘
           │
           ▼
┌─────────────────────┐
│      Validate       │
└─────────────────────┘
```

### Service Interface

Extend the existing parameter service with methods for system parameters (where `organization_uid IS NULL`):

```go
// Add to existing ParameterService or create SystemParameterService
type SystemParameterService interface {
    // GetSystem retrieves a system parameter value, returns nil if not found
    GetSystem(ctx context.Context, key string) (*Parameter, error)

    // SetSystem creates or updates a system parameter
    SetSystem(ctx context.Context, key string, value any, secret bool) error

    // DeleteSystem removes a system parameter
    DeleteSystem(ctx context.Context, key string) error

    // ListSystem returns all system parameters (secrets are masked)
    ListSystem(ctx context.Context) ([]*Parameter, error)

    // GetSystemString returns string value or default if not found
    GetSystemString(ctx context.Context, key string, defaultValue string) (string, error)

    // GetSystemInt returns int value or default if not found
    GetSystemInt(ctx context.Context, key string, defaultValue int) (int, error)

    // GetSystemBool returns bool value or default if not found
    GetSystemBool(ctx context.Context, key string, defaultValue bool) (bool, error)
}
```

These methods query the `parameters` table with `WHERE organization_uid IS NULL`.

### Secret Handling

Parameters marked as `secret = true`:
- Are never exposed in API responses (masked as `"******"`)
- Are not logged
- Include: `jwt_secret`, `email.password`

### API Endpoints (Admin Only)

(update the OpenAPI schema)

```
GET    /api/v1/system/parameters          # List all parameters (secrets masked)
GET    /api/v1/system/parameters/:key     # Get single parameter
PUT    /api/v1/system/parameters/:key     # Set parameter value
DELETE /api/v1/system/parameters/:key     # Delete parameter
```

**Request/Response Examples:**

```json
// GET /api/v1/system/parameters
{
  "data": [
    {"key": "base_url", "value": "https://solidping.example.com", "secret": false},
    {"key": "jwt_secret", "value": "******", "secret": true},
    {"key": "email.host", "value": "smtp.example.com", "secret": false},
    {"key": "email.password", "value": "******", "secret": true}
  ]
}

// PUT /api/v1/system/parameters/base_url
// Request: {"value": "https://new-url.example.com"}
// Response: {"key": "base_url", "value": "https://new-url.example.com", "secret": false}
```

## Migration Path

**IMPORTANT: Do NOT create a new migration file. Update the existing migration files instead.**

1. **Update existing migration files** (do not create new ones):
   - `back/internal/db/postgres/migrations/20251207000001_initial.up.sql`
   - `back/internal/db/postgres/migrations/20251207000001_initial.down.sql`
   - `back/internal/db/sqlite/migrations/20251207000001_initial.up.sql`
   - `back/internal/db/sqlite/migrations/20251207000001_initial.down.sql`

   Changes:
   - Make `organization_uid` nullable in the `parameters` table
   - Update unique indexes to handle NULL `organization_uid`

2. **Update `Parameter` model** (`back/internal/db/models/parameter.go`):
   - Change `OrganizationUID` from `string` to `*string`

3. **Add system parameter methods** to existing parameter service or create `SystemParameterService`

4. **Modify `config.Load()`** to:
   - Accept database connection
   - Load system parameters from database after defaults
   - Auto-generate JWT secret if needed

5. **Add API endpoints** (admin-only) for system parameters

6. **Update frontend settings page** to manage system parameters

## Security Considerations

1. **JWT Secret Rotation**: Changing the JWT secret invalidates all existing tokens. Users will need to re-authenticate.
2. **Environment Override**: Production deployments can use environment variables to prevent database values from being used (e.g., for secrets managed by Kubernetes secrets).
3. **Admin Only**: Only admin users can view/modify system parameters.
4. **Audit Logging**: Parameter changes should be logged for auditing.

## Future Enhancements

- Parameter validation rules (e.g., `base_url` must be a valid URL)
- Parameter categories/grouping for UI organization
- Import/export configuration for backup/restore
- Parameter change notifications (webhook/email)
