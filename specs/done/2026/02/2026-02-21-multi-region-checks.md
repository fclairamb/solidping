# Multi-Region Checks

## Context

SolidPing currently has region infrastructure in place (workers have regions, check jobs have a region field, results track region) but doesn't actually create multiple check jobs per check for multi-region monitoring. There's a one-to-one relationship between checks and check_jobs.

This spec transforms that into a one-to-many relationship: each check produces one check_job per region, with staggered scheduling to maintain the overall check frequency. Region definitions are stored as parameters (global + org-scoped). Workers validate their `SP_REGION` against defined regions on startup.

Design decisions:
- **Region definitions as parameters** -- uses the existing `parameters` table, no new tables
- **Prefix matching** -- a worker with `SP_REGION=eu-fr-paris` claims jobs for region `eu-fr`
- **Period splitting** -- a 1-min check with 2 regions creates 2 jobs at 2-min period, offset by 1 min
- **Default "local"** -- when no regions are configured globally, the default is `[{slug: "local", emoji: "📍", name: "Local"}]`
- **SP_REGION defaults to "local"** -- when unset, workers get `SP_REGION=local`
- **Region hierarchy**: definitions use slug prefixes (e.g., `eu-fr`), instances use full names (e.g., `eu-fr-paris`)
- **Default regions cascade**: check regions > org default regions > global default regions > all defined regions

---

## 1. Region Definitions

### Storage

Stored in the `parameters` table using existing system/org parameter infrastructure. No schema changes to the parameters table.

**System parameter `regions`** -- list of all supported regions:
```json
{
  "value": [
    { "slug": "eu-fr", "emoji": "🇫🇷", "name": "France" },
    { "slug": "eu-de", "emoji": "🇩🇪", "name": "Germany" },
    { "slug": "us-east", "emoji": "🇺🇸", "name": "US East" }
  ]
}
```

When not configured, defaults to:
```json
{
  "value": [
    { "slug": "local", "emoji": "📍", "name": "Local" }
  ]
}
```

**System parameter `default_regions`** -- default region slugs for checks that don't specify regions:
```json
{ "value": ["eu-fr", "us-east"] }
```

When not configured, defaults to all defined region slugs.

**Org parameter `default_regions`** (with `organization_uid` set) -- overrides the global default for an org:
```json
{ "value": ["eu-fr"] }
```

When not configured, falls back to the global `default_regions`.

### Region Resolution for a Check

When creating/updating a check, the effective regions are resolved as:
1. If `check.Regions` is non-empty, use those
2. Else if the org has a `default_regions` parameter, use those
3. Else if the system has a `default_regions` parameter, use those
4. Else use all globally defined region slugs

### Region Model

**New file:** `back/internal/regions/regions.go`

```go
type RegionDefinition struct {
    Slug  string `json:"slug"`
    Emoji string `json:"emoji"`
    Name  string `json:"name"`
}
```

Service methods:
- `GetGlobalRegions(ctx) -> []RegionDefinition` -- from system `regions` parameter
- `GetOrgDefaultRegions(ctx, orgUID) -> []string` -- from org `default_regions` parameter
- `ResolveRegionsForCheck(ctx, check, orgUID) -> []string` -- implements the cascade
- `ValidateWorkerRegion(ctx, workerRegion) -> error` -- checks prefix match against defined regions
- `MatchesRegion(workerRegion, jobRegion) -> bool` -- `strings.HasPrefix(workerRegion, jobRegion)`

---

## 2. Database Migration

### Migration files
- `back/internal/db/postgres/migrations/YYYYMMDD000001_multi_region_checks.up.sql`
- `back/internal/db/postgres/migrations/YYYYMMDD000001_multi_region_checks.down.sql`
- `back/internal/db/sqlite/migrations/YYYYMMDD000001_multi_region_checks.up.sql`
- `back/internal/db/sqlite/migrations/YYYYMMDD000001_multi_region_checks.down.sql`

### Changes

Remove the UNIQUE constraint on `check_jobs.check_uid` to allow multiple jobs per check (one per region).

**New indexes:**
- `UNIQUE (check_uid, region) WHERE region IS NOT NULL` -- prevent duplicate jobs per check+region
- `UNIQUE (check_uid) WHERE region IS NULL` -- at most one NULL-region job per check (backward compat)
- `INDEX (check_uid)` -- efficient lookups for reconciliation

---

## 3. Configuration Changes

**File:** `back/internal/config/config.go`

### SP_REGION default

In `Load()`, after reading `SP_REGION`, default to `"local"` if unset:
```go
if cfg.Server.CheckWorker.Region == "" {
    cfg.Server.CheckWorker.Region = "local"
}
```

### Startup validation

In `back/internal/app/server.go`, after system config initialization, when `ShouldRunChecks()` is true:
- Load global region definitions
- Verify that `SP_REGION` starts with at least one defined region's slug
- If no match, log an error listing valid regions and return a fatal error

---

## 4. Check Job Creation with Period Splitting

### Formula

Given:
- `P` = check's original period (e.g., 60s)
- `N` = number of regions
- Job `i` (0-indexed) gets:
  - `period = P * N`
  - `scheduled_at = now + P * i`

Example: P=60s, N=2 (eu, us):
- Job 0 (eu): period=120s, scheduled_at=now
- Job 1 (us): period=120s, scheduled_at=now+60s

Result: one check runs globally every 60s, alternating between regions.

Example: P=60s, N=3 (eu-fr, eu-de, us-east):
- Job 0: period=180s, scheduled_at=now
- Job 1: period=180s, scheduled_at=now+60s
- Job 2: period=180s, scheduled_at=now+120s

### Modified CreateCheck

The `CreateCheck` DB method signature stays the same. The handler service resolves regions and sets `check.Regions` before calling `CreateCheck`. The DB layer reads `check.Regions` to decide how many jobs to create:

```go
// If check has regions, create one job per region with period splitting
// If check has no regions, create a single job with no region (backward compat)
```

---

## 5. Region Matching in ClaimJobs

**File:** `back/internal/checkworker/checkjobsvc/service.go`

Change `selectAvailableJobs` from exact region matching to prefix matching:

Current:
```go
WhereOr("region = ?", *region)
```

New:
```go
WhereOr("? LIKE region || '%'", *region)
```

This means a worker with `SP_REGION=eu-fr-paris` claims jobs where `region=eu-fr` because `"eu-fr-paris" LIKE "eu-fr%"`. Works for both PostgreSQL and SQLite.

---

## 6. Job Reconciliation on Check Update

When a check is updated and regions or period change, reconcile check_jobs:

1. Resolve new target regions
2. Fetch existing check_jobs for the check via `ListCheckJobsByCheckUID`
3. Compare:
   - Existing jobs with regions not in target -> delete
   - Target regions not in existing jobs -> create
   - Existing jobs with changed period -> update period
4. Recalculate periods for all jobs using the P*N formula

### New db.Service methods

```
ListCheckJobsByCheckUID(ctx, checkUID) -> []*CheckJob
DeleteCheckJob(ctx, uid) -> error
CreateCheckJob(ctx, job) -> error
```

### Enable/Disable handling

- `enabled: true -> false`: Delete all check_jobs for the check
- `enabled: false -> true`: Create check_jobs for all resolved regions

---

## 7. API Endpoints

### New endpoints

| Method | Path | Handler | Auth |
|--------|------|---------|------|
| GET | `/api/v1/regions` | ListGlobalRegions | No |
| GET | `/api/v1/orgs/:org/regions` | ListOrgRegions | Yes |

**GET /api/v1/regions** response:
```json
{
  "data": [
    { "slug": "eu-fr", "emoji": "🇫🇷", "name": "France" },
    { "slug": "us-east", "emoji": "🇺🇸", "name": "US East" }
  ]
}
```

**GET /api/v1/orgs/:org/regions** response:
```json
{
  "data": [
    { "slug": "eu-fr", "emoji": "🇫🇷", "name": "France" },
    { "slug": "us-east", "emoji": "🇺🇸", "name": "US East" }
  ],
  "defaultRegions": ["eu-fr", "us-east"]
}
```

### Changes to existing Check endpoints

- `CreateCheckRequest`: add `regions []string` (optional, empty = use defaults)
- `UpdateCheckRequest`: add `regions *[]string` (optional)
- `CheckResponse`: add `regions []string`

### Backend handler

**New directory:** `back/internal/handlers/regions/`
- `service.go` -- loads regions from parameters
- `handler.go` -- HTTP handlers

---

## 8. Frontend -- dash0

### Region selector in check form
**File:** `apps/dash0/src/components/shared/check-form.tsx`

- Fetch available regions from `GET /api/v1/orgs/$org/regions`
- Show a multi-select checkbox group for regions (only visible if >1 region defined globally)
- Pre-populate with org's `defaultRegions` for new checks
- Send `regions` in create/update requests

### Check display
- Show region badges (emoji + name) on check detail and check list views
- Results already have a `region` field -- display as a badge in result timeline

### API hooks
**File:** `apps/dash0/src/api/hooks.ts`
- Add `RegionDefinition` interface
- Add `useRegions(org)` hook
- Add `regions` to `Check`, `CreateCheckRequest`, `UpdateCheckRequest` types

---

## 9. Implementation Order

1. **Region package** -- `back/internal/regions/regions.go` with types and service
2. **New db.Service methods** -- `ListCheckJobsByCheckUID`, `DeleteCheckJob`, `CreateCheckJob`
3. **Database migration** -- remove UNIQUE constraint, add composite indexes
4. **Config default** -- `SP_REGION` defaults to `"local"`
5. **Startup validation** -- validate SP_REGION against defined regions
6. **Multi-job creation** -- update `CreateCheck` to create N jobs with period splitting
7. **Region prefix matching** -- update `ClaimJobs` to use `LIKE` prefix matching
8. **Job reconciliation** -- update `UpdateCheck` to reconcile jobs on region/period change
9. **API endpoints** -- `GET /api/v1/regions`, `GET /api/v1/orgs/:org/regions`, check request/response changes
10. **Frontend** -- region selector in check form, region badges in displays
11. **Verify end-to-end**

---

## 10. Key Files

| File | Change |
|------|--------|
| `back/internal/regions/regions.go` | **New** -- region types and service |
| `back/internal/db/postgres/migrations/..._multi_region_checks.up.sql` | **New** -- migration |
| `back/internal/db/sqlite/migrations/..._multi_region_checks.up.sql` | **New** -- migration |
| `back/internal/config/config.go` | Default SP_REGION to "local" |
| `back/internal/app/server.go` | Add startup region validation |
| `back/internal/db/service.go` | Add ListCheckJobsByCheckUID, DeleteCheckJob, CreateCheckJob |
| `back/internal/db/postgres/postgres.go` | Implement new methods, update CreateCheck |
| `back/internal/db/sqlite/sqlite.go` | Implement new methods, update CreateCheck |
| `back/internal/checkworker/checkjobsvc/service.go` | Prefix matching in ClaimJobs |
| `back/internal/handlers/checks/service.go` | Region resolution, reconciliation, request/response types |
| `back/internal/handlers/regions/service.go` | **New** -- region API service |
| `back/internal/handlers/regions/handler.go` | **New** -- region API handler |
| `apps/dash0/src/api/hooks.ts` | Add region types and hooks |
| `apps/dash0/src/components/shared/check-form.tsx` | Add region selector |

---

## 11. Verification

### Backend
```bash
make build
SP_DB_RESET=true make dev-backend

TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"org":"default","email":"admin@solidping.com","password":"solidpass"}' \
  'http://localhost:4000/api/v1/auth/login' | jq -r '.accessToken')

# List regions (should show default "local")
curl -s 'http://localhost:4000/api/v1/regions'

# List org regions
curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:4000/api/v1/orgs/default/regions'

# Create a check with regions
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"type":"http","name":"Multi-Region Check","config":{"url":"https://example.com"},"regions":["eu-fr","us-east"]}' \
  'http://localhost:4000/api/v1/orgs/default/checks'

# Verify multiple check_jobs were created (check via database or results)
```

### Startup validation
```bash
# Should fail: SP_REGION doesn't match any defined region
SP_REGION=invalid-region make dev-backend
# Expected: fatal error with list of valid regions

# Should succeed:
SP_REGION=local make dev-backend
```

### Tests
```bash
make test
make lint
```
