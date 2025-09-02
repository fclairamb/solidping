# Bulk Test Check Creation API

## Overview

Extend the `/test` API with an endpoint to bulk-create a high number of checks for performance testing. The endpoint accepts a count and templates for slug/URL with a `{nb}` placeholder that gets replaced with the check number (0, 1, 2, ...). This is guarded by `SP_RUNMODE=test` only.

## Motivation

1. Performance testing requires exercising the system with hundreds or thousands of active checks.
2. Manually creating checks one-by-one through the standard API is impractical for this use case.
3. A dedicated test endpoint makes it trivial to spin up a load test scenario.

## Design Decisions

- **Bypass the checks service**: Use `dbService.CreateCheck()` directly (same pattern as `loadSampleChecks()` in `job_startup.go`) to avoid per-check slug conflict resolution, event emission overhead, and connection auto-attach logic that would make bulk creation extremely slow.
- **Query parameters**: Use query params (not JSON body) for consistency with the existing `/fake` endpoint style. Use POST since it's mutating.
- **Single event notification**: Send one `check.created` event after the entire loop, not per-check, to avoid flooding workers.
- **Deterministic slugs**: No slug conflict resolution; if a slug already exists the individual insert fails and is counted as an error.
- **Config validation**: Each check is validated through its checker's `Validate()` method before insertion.

---

## 1. Handler Struct Update

**File**: `back/internal/handlers/testapi/handler.go`

- Add `eventNotifier notifier.EventNotifier` field to the `Handler` struct
- Update `NewHandler` to accept and store the event notifier

---

## 2. Route Registration

**File**: `back/internal/app/server.go`

- Update `testapi.NewHandler(s.jobSvc, s.dbService)` call to pass `s.services.EventNotifier`
- Register new routes inside the existing `if s.config.RunMode == "test"` block:
  - `POST /api/v1/test/checks/bulk` -> `testHandler.BulkCreateChecks`
  - `DELETE /api/v1/test/checks/bulk` -> `testHandler.BulkDeleteChecks`

---

## 3. Bulk Create Endpoint

**New file**: `back/internal/handlers/testapi/bulk_checks.go`

### `POST /api/v1/test/checks/bulk`

Query parameters:
- `type` (required): Check type, e.g. `http`. Must be a valid checker type via `registry.GetChecker()`.
- `slug` (required): Slug template with `{nb}` placeholder, e.g. `http-{nb}`.
- `name` (optional): Name template with `{nb}`. Defaults to slug value.
- `url` (required for http): URL template with `{nb}`, e.g. `http://localhost:4000/api/v1/fake?nb={nb}`.
- `period` (optional): Duration string (e.g. `10s`, `1m`). Defaults to `30s`.
- `count` (required): Number of checks to create, 1-10000.
- `org` (optional): Organization slug. Defaults to `test`.

Response:
```json
{
  "created": 100,
  "failed": 0,
  "errors": [],
  "firstSlug": "http-0",
  "lastSlug": "http-99"
}
```

Logic:
1. Parse and validate query params
2. Resolve org via `dbService.GetOrganizationBySlug()`
3. Get checker via `registry.GetChecker()` for validation
4. Loop 0..count-1:
   - Replace `{nb}` in slug, name, url templates with `strconv.Itoa(i)`
   - Create `models.NewCheck(orgUID, slug, checkType)` with name, config (`{"url": url}` for http), period, enabled=true
   - Validate with `checker.Validate()`
   - Call `dbService.CreateCheck()` (this creates check + check_job + initial result in one DB transaction)
5. Send single `eventNotifier.Notify("check.created", "{}")` to wake workers
6. Return response

---

## 4. Bulk Delete Endpoint

Same file.

### `DELETE /api/v1/test/checks/bulk`

Query parameters:
- `slug` (required): Slug template with `{nb}`.
- `count` (required): Number of checks to delete.
- `org` (optional): Defaults to `test`.

Response:
```json
{
  "deleted": 100
}
```

Logic: Loop 0..count-1, resolve each slug, call `dbService.DeleteCheck()` (soft delete). Skip missing checks silently.

---

## 5. Tests

**New file**: `back/internal/handlers/testapi/bulk_checks_test.go`

Test cases:
- Bulk create N=5 checks, verify all created with correct slugs/names/URLs
- Verify check_jobs were created alongside checks
- Verify `{nb}` replacement in slug, name, URL
- Invalid check type returns error
- Count > 10000 returns error
- Missing required params return errors
- Bulk delete removes created checks
- Creating duplicate slugs reports failures

---

## Files to Modify/Create

| File | Action |
|------|--------|
| `back/internal/handlers/testapi/handler.go` | Modify: add `eventNotifier` field, update `NewHandler` |
| `back/internal/app/server.go` | Modify: update `NewHandler` call, add route registrations |
| `back/internal/handlers/testapi/bulk_checks.go` | Create: `BulkCreateChecks` and `BulkDeleteChecks` handlers |
| `back/internal/handlers/testapi/bulk_checks_test.go` | Create: tests |

## Verification

1. Start server with `SP_RUN_MODE=test make dev-backend`
2. Create 100 checks: `curl -X POST 'http://localhost:4000/api/v1/test/checks/bulk?type=http&slug=http-{nb}&url=http://localhost:4000/api/v1/fake?nb={nb}&period=10s&count=100&org=test'`
3. Verify checks appear in list: `curl -H "Authorization: Bearer $TOKEN" 'http://localhost:4000/api/v1/orgs/test/checks'`
4. Delete them: `curl -X DELETE 'http://localhost:4000/api/v1/test/checks/bulk?slug=http-{nb}&count=100&org=test'`
5. Run tests: `cd back && go test ./internal/handlers/testapi/ -run TestBulk -v`
