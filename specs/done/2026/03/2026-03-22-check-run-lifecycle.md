# Check Run Lifecycle

## Overview

Introduce a new `running` result status (`status=5`) that enables two capabilities:

1. **Passive checks (heartbeat)**: Two-phase reporting where external systems report "process started" before reporting the final outcome (up/down/error). Useful for cron jobs, deployments, batch processes.
2. **Active checks**: The t=0 marker already exists (`ResultStatusInitial=0` inserted at check creation in `db/postgres/postgres.go:892`), so no additional work is needed.

## Motivation

Heartbeat checks currently only support a single signal: "I'm alive." This works for long-running services but fails for **batch processes** (cron jobs, deployments, ETL pipelines) where you need to know:
- Did the process **start**?
- Did it **complete successfully** or **fail**?
- If it started but never completed, it's **stale** (crashed/hung).

Without run lifecycle tracking, a cron job that crashes mid-execution looks identical to one that never started.

---

## 1. New Result Status: `running`

**Constant**: `ResultStatusRunning ResultStatus = 5` / `StatusRunning Status = 5`
**String**: `"RUNNING"` / `"running"`

### Files to modify

**`back/internal/db/models/result.go`**:
```go
ResultStatusRunning ResultStatus = 5
```
Update `StatusToString` to return `"RUNNING"` for status 5.

**`back/internal/checkers/checkerdef/types.go`**:
```go
StatusRunning Status = 5
```
Update `String()` to return `"running"` for status 5.

### Non-data statuses: `initial` (0) and `running` (5)

Both `initial` and `running` are **non-data statuses** — they represent lifecycle markers, not measurement outcomes. They share the same treatment everywhere:

| Aspect | Behavior | Changes needed |
|--------|----------|----------------|
| **Visual** | Rendered in **gray** in tracking timeline and graphs | Frontend color mapping |
| **Incidents** | Ignored — not success, not failure | None (`incidents/service.go:55` already returns nil) |
| **Check status** | Does not change check status | None (only UP/DOWN affect it) |
| **Statistics** | **Completely excluded** — not counted in `totalChecks`, `successfulChecks`, or `availabilityPct` | Filter in `processRawResult` |
| **Results API filter** | `"running"` maps to `{5}`, `"unknown"` maps to `{0}` | Add `"running"` to `mapStatusStringsToInts` |
| **LastForStatus** | Tracks its own `last_for_status=true` per status | None (automatic) |

These statuses must **never** be counted as down. They are invisible to statistics — as if they don't exist for availability/uptime calculations.

---

## 2. Heartbeat API Enhancement

### Current endpoint
```
POST /api/v1/heartbeat/{org}/{identifier}?token=<token>
```
Always records `status=UP`.

### Enhanced endpoint
```
POST /api/v1/heartbeat/{org}/{identifier}?token=<token>&status=running
POST /api/v1/heartbeat/{org}/{identifier}?token=<token>&status=up       (default)
POST /api/v1/heartbeat/{org}/{identifier}?token=<token>&status=down
POST /api/v1/heartbeat/{org}/{identifier}?token=<token>&status=error
```

When `status` is omitted, defaults to `up` (fully backward compatible).

### Optional: JSON body for context
```
POST /api/v1/heartbeat/{org}/{identifier}?token=<token>&status=running
Content-Type: application/json
{"message": "Deploying v2.3.1"}
```

The body populates the result's `output` field. If no body is sent, the output defaults to a descriptive message based on status.

### Default output messages
| Status | Default output message |
|--------|----------------------|
| `running` | `"Run started"` |
| `up` | `"Heartbeat received"` (unchanged) |
| `down` | `"Heartbeat reported failure"` |
| `error` | `"Heartbeat reported error"` |

### Files to modify

**`back/internal/handlers/heartbeat/handler.go`**:
- Parse `status` query parameter (default: `"up"`)
- Parse optional JSON body for `message` field
- Pass both to service

**`back/internal/handlers/heartbeat/service.go`**:
- Add `status` and `output` parameters to `ReceiveHeartbeat`
- Map string status to `ResultStatus` constant
- Validate allowed statuses: `running`, `up`, `down`, `error`
- New error: `ErrInvalidStatus`
- Skip incident processing for `running` status (optimization, though incidents already ignore it)

---

## 3. Stale Run Detection

When a process reports `running` but never completes, the heartbeat worker must detect this.

### Current behavior (`back/internal/checkworker/worker.go:593-648`)
The worker checks if the last result was `StatusUp` and recent. If not → `StatusDown`.

### Enhanced behavior
Add a check for `StatusRunning`:
- If last result is `StatusRunning` **and** within `period * 2` → keep status as `running` (process still has time)
- If last result is `StatusRunning` **and** exceeds `period * 2` → mark `StatusTimeout` with output `"Run started but never completed"`

The multiplier (default 2x) could later be configurable via `config.runTimeout`, but for v1 we hardcode `period * 2`.

### Updated logic pseudocode
```
lastStatus = getLastResult()

if lastStatus == UP and recent:
    → UP
elif lastStatus == RUNNING and within 2*period:
    → RUNNING (still in progress)
elif lastStatus == RUNNING and past 2*period:
    → TIMEOUT ("Run started but never completed")
else:
    → DOWN ("Heartbeat overdue" or "No heartbeat received")
```

---

## 4. Aggregation Exclusion

**`back/internal/jobs/jobtypes/job_aggregation.go`**:

In `processRawResult` (line 641), skip results with `status=5` (running) and `status=0` (initial):
```go
// Skip non-terminal statuses from aggregation
if result.Status != nil && (*result.Status == int(models.ResultStatusRunning) || *result.Status == int(models.ResultStatusInitial)) {
    return
}
```

Both `initial` and `running` are non-data statuses. They must be completely invisible to statistics — not counted as up, not counted as down, not counted at all. Including them would inflate `totalChecks` and skew `availabilityPct`.

---

## 5. Results API Filter Update

**`back/internal/handlers/results/service.go`** `mapStatusStringsToInts`:

Add `"running": {5}` to the status map so the API supports `?status=running` filtering.

Updated mapping:
```go
statusMap := map[string][]int{
    "up":      {1},
    "down":    {2, 3, 4}, // down, timeout, error — NOT running
    "unknown": {0},       // initial
    "running": {5},       // new
}
```

`running` is its own category — it must NOT be grouped under `"down"`.

---

## 6. Implementation Order

1. Add `ResultStatusRunning=5` and `StatusRunning=5` constants + string mappings
2. Update `mapStatusStringsToInts` in results service
3. Enhance heartbeat handler to accept `status` query parameter + optional body
4. Enhance heartbeat service to support all status values
5. Update `executeHeartbeatJob` worker for stale run detection
6. Filter running/initial from aggregation
7. Tests for each change

## 7. Verification

### Manual testing
```bash
# 1. Create a heartbeat check
TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"org":"default","email":"admin@solidping.com","password":"solidpass"}' \
  'http://localhost:4000/api/v1/auth/login' | jq -r '.accessToken')

curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"My Cron Job","type":"heartbeat","period":"5m"}' \
  'http://localhost:4000/api/v1/orgs/default/checks' | jq '.'

# 2. Get the heartbeat token from the check config
HB_TOKEN=$(curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:4000/api/v1/orgs/default/checks/heartbeat-my-cron-job' | jq -r '.config.token')

# 3. Report "running"
curl -s -X POST "http://localhost:4000/api/v1/heartbeat/default/heartbeat-my-cron-job?token=$HB_TOKEN&status=running"

# 4. Check results show running status
curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:4000/api/v1/orgs/default/results?checkUid=heartbeat-my-cron-job&status=running' | jq '.'

# 5. Report completion
curl -s -X POST "http://localhost:4000/api/v1/heartbeat/default/heartbeat-my-cron-job?token=$HB_TOKEN&status=up"

# 6. Report failure
curl -s -X POST "http://localhost:4000/api/v1/heartbeat/default/heartbeat-my-cron-job?token=$HB_TOKEN&status=down"
```

### Automated tests
- `make gotest` — all existing tests must pass
- New tests in `heartbeat/service_test.go` for status parameter variations
- New tests in `checkworker/worker_test.go` for stale run detection

**Status**: In Progress | **Created**: 2026-03-22

---

## Implementation Plan

### Step 1: Add `running` status constants
- `back/internal/db/models/result.go`: Add `ResultStatusRunning = 5`, update `StatusToString`
- `back/internal/checkers/checkerdef/types.go`: Add `StatusRunning = 5`, update `String()`

### Step 2: Update results API filter
- `back/internal/handlers/results/service.go`: Add `"running": {5}` to `mapStatusStringsToInts`

### Step 3: Enhance heartbeat handler and service
- `back/internal/handlers/heartbeat/handler.go`: Parse `status` query param + optional JSON body
- `back/internal/handlers/heartbeat/service.go`: Accept status param, map to ResultStatus, validate, add `ErrInvalidStatus`

### Step 4: Stale run detection in heartbeat worker
- `back/internal/checkworker/worker.go`: Update `executeHeartbeatJob` to handle `StatusRunning`

### Step 5: Aggregation exclusion
- `back/internal/jobs/jobtypes/job_aggregation.go`: Skip `initial` and `running` in `processRawResult`

### Step 6: Tests
- Heartbeat service tests for all status values
- Worker tests for stale run detection
- Aggregation tests for exclusion
- Results API filter test for `running`
