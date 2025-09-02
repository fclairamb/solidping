# Heartbeat Checks

## Overview

Add a **heartbeat** check type for passive monitoring. Unlike active checks (HTTP, TCP, ICMP, DNS) where the server polls a target, heartbeat checks wait for an external service to call a URL to report it's alive. If no heartbeat is received within the expected interval, the check is marked as down. Useful for monitoring cron jobs, background workers, and any process that can make an outbound HTTP call.

## Motivation

1. Active checks can't monitor internal processes that aren't network-accessible (cron jobs, batch processors, queue workers).
2. Heartbeat monitoring is a standard feature in uptime tools (BetterStack, Healthchecks.io, Cronitor).
3. The existing check infrastructure (types, jobs, incidents, status tracking) can be reused with minimal additions.

## Design Decisions

- **Authentication**: Per-check token required via `?token=xxx` query parameter (auto-generated 32-char hex string)
- **HTTP methods**: Both GET and POST accepted for convenience (browser testing, simple integrations)
- **Down detection**: Heartbeat checks create check_jobs like other checks. The worker handles them specially: instead of calling a checker, it queries the last heartbeat result and returns UP/DOWN based on recency
- **Heartbeat URL**: Computed on the frontend using `window.location.origin` (no backend config needed)
- **No DB migration**: Heartbeat checks use the existing `checks` table with `type='heartbeat'` and `config={"token": "..."}`. The `period` field is reused as the expected heartbeat interval

---

## 1. Check Type Constant

**File**: `back/internal/checkers/checkerdef/types.go`

- Add `CheckTypeHeartbeat CheckType = "heartbeat"`
- Add to `ListCheckTypes()` return list

---

## 2. Heartbeat Checker Package

**New directory**: `back/internal/checkers/checkheartbeat/`

### checker.go

`HeartbeatChecker` implementing `checkerdef.Checker`:

- `Type()` returns `CheckTypeHeartbeat`
- `Validate(spec)`:
  - Auto-generates a random 32-char hex token and stores it in `spec.Config["token"]` if not already present
  - Sets default name/slug to `"heartbeat"` if empty
- `Execute()`: returns `ErrNotExecutable` (worker handles heartbeat checks specially)

### config.go

`HeartbeatConfig` implementing `checkerdef.Config`:

- Fields: `Token string`
- `FromMap`/`GetConfig` for serialization

---

## 3. Checker Registry

**File**: `back/internal/checkers/registry/registry.go`

- Add `checkheartbeat` import
- Add `CheckTypeHeartbeat` cases to `GetChecker()` and `ParseConfig()`

---

## 4. Worker Heartbeat Execution

**File**: `back/internal/checkworker/worker.go`

In `executeJob()`, before the standard checker flow, add a check:

```go
if checkerdef.CheckType(checkType) == checkerdef.CheckTypeHeartbeat {
    return r.executeHeartbeatJob(ctx, logger, checkJob)
}
```

New method `executeHeartbeatJob()`:

1. Query the latest result for this check using `GetLastResultForChecks`
2. If the last UP result is within the check's period, save an UP result
3. If not (or no UP result exists), save a DOWN result
4. Include descriptive output (e.g., `"Heartbeat received"`, `"Heartbeat overdue"`, `"No heartbeat received"`)
5. Process incidents and release lease as usual

---

## 5. Heartbeat Ingestion Endpoint

**New directory**: `back/internal/handlers/heartbeat/`

### handler.go

`Handler` with `HandlerBase` embedding:

- `ReceiveHeartbeat(writer, req)`:
  1. Extract `org` and `identifier` from URL params
  2. Extract `token` from query parameter
  3. Delegate to service
  4. Return `200 OK` with `{"status": "ok"}`

Error mapping:

| Error | HTTP Status | Code |
|-------|-------------|------|
| `ErrOrganizationNotFound` | 404 | `ORGANIZATION_NOT_FOUND` |
| `ErrCheckNotFound` | 404 | `CHECK_NOT_FOUND` |
| `ErrNotHeartbeatCheck` | 400 | `VALIDATION_ERROR` |
| `ErrMissingToken` | 401 | `UNAUTHORIZED` |
| `ErrInvalidToken` | 401 | `UNAUTHORIZED` |

### service.go

`Service` with `db.Service` and `checkjobsvc.Service` dependencies:

- `ReceiveHeartbeat(ctx, orgSlug, identifier, token)`:
  1. Look up org by slug
  2. Look up check by UID or slug within the org
  3. Verify check type is `"heartbeat"`
  4. Validate token matches `check.Config["token"]`
  5. Save a result with status=UP, duration=0 via `SaveResultWithStatusTracking`
  6. Process incidents (recovery)

---

## 6. Route Registration

**File**: `back/internal/app/server.go`

Public routes (no auth middleware):

```go
heartbeatService := heartbeat.NewService(s.dbService, jobService)
heartbeatHandler := heartbeat.NewHandler(heartbeatService, s.config)
api.POST("/heartbeat/:org/:identifier", heartbeatHandler.ReceiveHeartbeat)
api.GET("/heartbeat/:org/:identifier", heartbeatHandler.ReceiveHeartbeat)
```

---

## 7. Frontend — TypeScript Types

**File**: `apps/dash0/src/api/hooks.ts`

- Add `"heartbeat"` to `Check.type` union type
- Add `"heartbeat"` to `CreateCheckRequest.type` union type

---

## 8. Frontend — Check Form

**File**: `apps/dash0/src/components/shared/check-form.tsx`

- Add `"heartbeat"` to `CheckType` union
- Add heartbeat to `checkTypes` array: `{ value: "heartbeat", label: "Heartbeat", description: "Monitor via incoming pings" }`
- In `handleSubmit()`: heartbeat case sends empty config (token is auto-generated by backend)
- In `renderConfigFields()`: heartbeat case shows info text: "No additional configuration needed. A heartbeat URL will be generated after creation."
- Change interval label to "Expected Interval" when type is heartbeat, with helper text: "Check will be marked as down if no heartbeat is received within this interval"

---

## 9. Frontend — Check Detail Page

**File**: `apps/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx`

For heartbeat checks, add a "Heartbeat Endpoint" section in the Configuration card:

- Display the full URL: `{origin}/api/v1/heartbeat/{org}/{slug}?token={token}`
- Copy-to-clipboard button for the URL
- Sample curl command in a code block:
  ```
  curl "{origin}/api/v1/heartbeat/{org}/{slug}?token={token}"
  ```
- The token comes from `check.config.token`

---

## 10. Frontend — Check List Page

**File**: `apps/dash0/src/routes/orgs/$org/checks.index.tsx`

- The "Target" column displays `check.slug || "heartbeat"` for heartbeat checks instead of trying to extract `url`/`host` from config

---

## Key Files

| File | Change |
|------|--------|
| `back/internal/checkers/checkerdef/types.go` | Add `CheckTypeHeartbeat` |
| `back/internal/checkers/checkheartbeat/checker.go` | **New** — Heartbeat checker |
| `back/internal/checkers/checkheartbeat/config.go` | **New** — Heartbeat config |
| `back/internal/checkers/registry/registry.go` | Register heartbeat checker |
| `back/internal/checkworker/worker.go` | Handle heartbeat job execution |
| `back/internal/handlers/heartbeat/handler.go` | **New** — Ingestion endpoint handler |
| `back/internal/handlers/heartbeat/service.go` | **New** — Ingestion business logic |
| `back/internal/app/server.go` | Register heartbeat routes |
| `apps/dash0/src/api/hooks.ts` | Add heartbeat type |
| `apps/dash0/src/components/shared/check-form.tsx` | Add heartbeat form support |
| `apps/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx` | Show heartbeat URL |
| `apps/dash0/src/routes/orgs/$org/checks.index.tsx` | Handle heartbeat in target column |

## Verification

### Backend
```bash
go vet ./internal/checkers/... ./internal/handlers/heartbeat/... ./internal/checkworker/...
go test ./internal/checkers/... ./internal/handlers/heartbeat/... ./internal/checkworker/...
golangci-lint run ./internal/checkers/checkheartbeat/... ./internal/handlers/heartbeat/... ./internal/checkworker/...
```

### Frontend
```bash
cd apps/dash0 && bun run lint
```

### E2E Flow
```bash
# Login
TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"org":"default","email":"admin@solidping.com","password":"solidpass"}' \
  'http://localhost:4000/api/v1/auth/login' | jq -r '.accessToken')

# Create a heartbeat check
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"type":"heartbeat","name":"My Cron Job","period":"00:05:00"}' \
  'http://localhost:4000/api/v1/orgs/default/checks'

# Send heartbeat (use token from check's config in the response)
curl 'http://localhost:4000/api/v1/heartbeat/default/my-cron-job?token=TOKEN_FROM_RESPONSE'

# Verify check status is UP
curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:4000/api/v1/orgs/default/checks?with=last_result'
```

### Manual Testing
1. Create a heartbeat check via the UI
2. Verify the heartbeat URL is displayed on the detail page with copy button
3. Send a curl request to the heartbeat URL
4. Verify the check status changes to UP
5. Wait for the period to elapse without sending a heartbeat
6. Verify the check status changes to DOWN

---

**Status**: Implemented | **Created**: 2026-02-16
