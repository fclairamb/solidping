# Internal Checks

## Problem Statement

Some checks are used for internal purposes (system health verification, infrastructure monitoring, monitoring system self-tests) and should not clutter the default check listing. Users need a way to distinguish between user-facing checks and internal/operational checks.

## Use Cases

1. **System self-tests**: Checks that verify the monitoring system itself is working correctly
2. **Infrastructure monitoring**: Backend health checks that operators need but end users don't
3. **Development/staging checks**: Temporary checks used during development
4. **Always-on baseline checks**: Checks that must always run but don't need user attention unless they fail

## Solution

Add an `internal` boolean flag to checks that controls their visibility in listings by default.

### Database Schema

New migration (both PostgreSQL and SQLite):

```sql
ALTER TABLE checks ADD COLUMN internal BOOLEAN NOT NULL DEFAULT FALSE;
```

### Model Changes

Add to the `Check` struct in `back/internal/db/models/check.go`:

```go
type Check struct {
    UID                       string             `json:"uid"`
    OrganizationUID           string             `json:"organizationUid"`
    CheckGroupUID             *string            `json:"checkGroupUid,omitempty"`
    Name                      *string            `json:"name,omitempty"`
    Slug                      *string            `json:"slug,omitempty"`
    Description               *string            `json:"description,omitempty"`
    Type                      string             `json:"type"`
    Config                    JSONMap            `json:"config"`
    Regions                   []string           `json:"regions"`
    Enabled                   bool               `json:"enabled"`
    Internal                  bool               `json:"internal"`          // NEW
    Period                    timeutils.Duration `json:"period"`
    IncidentThreshold         int                `json:"incidentThreshold"`
    EscalationThreshold       int                `json:"escalationThreshold"`
    RecoveryThreshold         int                `json:"recoveryThreshold"`
    ReopenCooldownMultiplier  *int               `json:"reopenCooldownMultiplier,omitempty"`
    MaxAdaptiveIncrease       *int               `json:"maxAdaptiveIncrease,omitempty"`
    Status                    CheckStatus        `json:"status"`
    StatusStreak              int                `json:"statusStreak"`
    StatusChangedAt           *time.Time         `json:"statusChangedAt,omitempty"`
    CreatedAt                 time.Time          `json:"createdAt"`
    UpdatedAt                 time.Time          `json:"updatedAt"`
    DeletedAt                 *time.Time         `json:"deletedAt,omitempty"`
}
```

Add to `CheckUpdate` struct:

```go
Internal *bool `json:"internal,omitempty"`
```

Add to `ListChecksFilter` struct:

```go
type ListChecksFilter struct {
    Labels        map[string]string
    CheckGroupUID *string
    Query         string
    Internal      *string  // "true", "false", or "all" — NEW
    Limit         int
    CursorCreatedAt *time.Time
    CursorUID       string
}
```

### API Changes

#### Check Object

Add `internal` field to check responses (already handled by model JSON tags).

#### List Checks Endpoint

`GET /api/v1/orgs/$org/checks`

Add query parameter to the handler in `back/internal/handlers/checks/handler.go`:
- `internal` - Filter by internal status
  - Not set / `false` (default): Show only non-internal checks
  - `true`: Show only internal checks
  - `all`: Show all checks regardless of internal status

This integrates alongside existing query params: `with`, `labels`, `checkGroupUid`, `q`, `cursor`, `limit`.

Examples:
```bash
# Default: only user-facing checks
GET /api/v1/orgs/default/checks

# Only internal checks
GET /api/v1/orgs/default/checks?internal=true

# All checks
GET /api/v1/orgs/default/checks?internal=all
```

#### Create/Update Check

`POST /api/v1/orgs/$org/checks` and `PATCH /api/v1/orgs/$org/checks/$uid`

Accept `internal` boolean field in request body:

```json
{
  "name": "System Health Check",
  "internal": true
}
```

### Database Query Changes

Update the list checks queries in both PostgreSQL (`back/internal/db/postgres/`) and SQLite (`back/internal/db/sqlite/`) implementations to filter on the `internal` column based on `ListChecksFilter.Internal`.

### CLI Changes

Update `back/pkg/cli/checks.go`:

```bash
# Default: hide internal checks
sp checks list

# Show only internal checks
sp checks list --internal

# Show all checks (internal + non-internal)
sp checks list --all
```

Add `--internal` (bool) and `--all` (bool) flags to the list command, which set the `internal` query parameter accordingly.

### UI Changes (dash0)

Update `apps/dash0/src/routes/orgs/$org/checks.index.tsx`:

1. **Default view**: Hide internal checks from the checks list (pass `internal=false` to API by default)
2. **Filter control**: Add a toggle or dropdown alongside the existing search input to show:
   - User checks only (default)
   - Internal checks only
   - All checks
3. **Visual indicator**: When internal checks are shown, display a subtle badge/icon to distinguish them
4. **Check groups**: Internal checks should respect group membership — they appear in their group section when the internal filter includes them

### Behavior Notes

- Internal checks execute normally on schedule
- Results are recorded in the database as usual
- Alerts and notifications work the same as non-internal checks
- Internal status has no effect on check execution, only on listing visibility
- Status page visibility is controlled separately (via status page check associations), not by the internal flag

## Implementation Steps

### Step 1: Database Migration
1.1. Add PostgreSQL migration in `back/internal/db/postgres/migrations/`
1.2. Add SQLite migration in `back/internal/db/sqlite/migrations/`
1.3. Both add `internal BOOLEAN NOT NULL DEFAULT FALSE` to `checks` table

### Step 2: Model
2.1. Add `Internal` field to `Check` struct
2.2. Add `Internal` field to `CheckUpdate` struct
2.3. Add `Internal` filter to `ListChecksFilter` struct

### Step 3: Database Layer
3.1. Update PostgreSQL list checks query to filter on `internal`
3.2. Update SQLite list checks query to filter on `internal`
3.3. Include `internal` in create/update operations

### Step 4: API Handler
4.1. Parse `internal` query parameter in `ListChecks` handler
4.2. Accept `internal` in create/update request bodies
4.3. Return `internal` in check responses

### Step 5: CLI
5.1. Add `--internal` and `--all` flags to `checks list` command
5.2. Pass `internal` query parameter to API

### Step 6: Frontend
6.1. Add internal filter toggle to checks list page
6.2. Pass `internal` param to `useInfiniteChecks` hook
6.3. Add visual indicator for internal checks

## Migration

Existing checks will have `internal = false` by default, preserving current behavior.

---

**Status**: In Progress | **Created**: 2026-01-01 | **Updated**: 2026-03-22

## Implementation Plan

### Step 1: Database Migration
- Add PostgreSQL migration `20260322000003_check_internal.up.sql` with `ALTER TABLE checks ADD COLUMN internal BOOLEAN NOT NULL DEFAULT FALSE`
- Add matching SQLite migration
- Add down migrations

### Step 2: Model Changes
- Add `Internal bool` to `Check` struct with json tag and bun column
- Add `Internal *bool` to `CheckUpdate` struct
- Add `Internal *string` to `ListChecksFilter` struct

### Step 3: Database Layer (PostgreSQL + SQLite)
- Update list checks queries to filter on `internal` column
- Ensure create/update include the `internal` field

### Step 4: Service Layer
- Add `Internal *bool` to `CheckResponse`, `CreateCheckRequest`, `UpdateCheckRequest`, `UpsertCheckRequest`
- Update `convertCheckToResponse` to include internal field
- Update create/update/upsert logic to handle internal field
- Update list to parse and pass internal filter

### Step 5: Handler Layer
- Parse `internal` query parameter in ListChecks handler
- Accept `internal` in create/update request bodies (already via service structs)

### Step 6: CLI
- Add `--internal` and `--all` flags to `checks list` command
- Pass `internal` query parameter to API

### Step 7: Frontend (dash0)
- Add `internal` to Check type
- Add internal filter toggle to checks list page
- Add visual indicator (badge) for internal checks
- Pass `internal` param to `useInfiniteChecks` hook
