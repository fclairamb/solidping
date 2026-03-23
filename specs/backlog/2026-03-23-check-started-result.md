# Check "Initial" Result — Neutral Start Marker

## Overview

When a check is created, the system already inserts a `ResultStatusInitial` (0) result (displayed as `"unknown"`). This result is a **start marker** — it defines when monitoring began. It must be completely **neutral with respect to status**: it does not count as a failure, does not trigger an incident, and does not cause a status transition.

This spec formalizes the semantics of the `initial` result and ensures they are consistently enforced across the entire pipeline.

## Semantics of the `initial` result

The `initial` result is **transparent to status**. It does not change anything — it only marks a point in time.

| Property | Behavior |
|----------|----------|
| **Status effect** | None. Does not change the check's current status. |
| **If no prior status exists** | The check status remains `unknown` (0). The effective displayed status is determined by the **next** real result. |
| **If a prior status exists** (e.g., re-enabled check) | The check status is unchanged — it keeps whatever status it had before. |
| **Incident processing** | Skipped entirely. Not a success, not a failure. |
| **Aggregation / statistics** | Excluded from `totalChecks`, `successfulChecks`, and `availabilityPct`. |
| **Status streak** | Does not reset or increment the streak counter. |
| **Visual representation** | Rendered in **gray** across all UIs: timeline, graphs, result lists, status page. |
| **`last_for_status`** | Tracked normally (its own `last_for_status=true` for the `initial` status). |

## When an `initial` result is inserted

The `initial` result is inserted **by the API layer** (not the worker) in these situations:

1. **Check creation** — Already implemented in `CreateCheck()`. The result is inserted with output `"Check created"`.
2. **Check re-enabled** — When a check is re-enabled via PATCH (`enabled: true` when previously disabled), an `initial` result should be inserted to mark the new monitoring epoch.
3. **Check configuration changed** — When the check's target or type is modified (e.g., URL, host, type changed), an `initial` result should be inserted since the previous baseline is no longer meaningful.

The worker requires **no changes**. It continues to execute probes and report `UP`/`DOWN`/`TIMEOUT`/`ERROR` as usual.

## Current state and gaps

### Already correct
- `CreateCheck()` inserts an `initial` result at creation time.
- `incidents/service.go` skips `initial` results (returns early).
- Results API maps `"unknown": {0}`.

### To verify / fix
- **Aggregation**: Confirm `job_aggregation.go` excludes `initial` (0) from `processRawResult`. If not, add it alongside `running` (5).
- **Check status update**: Confirm that `UpdateCheckStatus` is never called with an `initial` result. The `initial` result should be invisible to the status/streak machinery.
- **Frontend**: Ensure `initial` / `unknown` is rendered in gray everywhere (timeline, graphs, result lists, status page).
- **Re-enable path**: Insert an `initial` result when a check transitions from `enabled: false` to `enabled: true`.
- **Config change path**: Insert an `initial` result when a check's target-related fields (`url`, `host`, `type`) are modified.

## Edge Cases

| Scenario | Behavior |
|----------|----------|
| Check created, first probe is DOWN | `initial` result exists. First real result is DOWN → check status transitions from `unknown` to `down`, streak = 1. The `initial` result did not count as a failure — incident threshold starts from this first DOWN, not before. |
| Check disabled then re-enabled | Status preserved from before disable. New `initial` result inserted. Worker resumes probing normally. |
| Check URL changed while status is UP | Status stays UP. New `initial` result inserted. Next probe result processed normally. |
| Heartbeat check | `initial` result is inserted at creation. External heartbeat pings are real signals and processed normally from the start. |

## Verification

### Manual testing
```bash
TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"org":"default","email":"admin@solidping.com","password":"solidpass"}' \
  'http://localhost:4000/api/v1/auth/login' | jq -r '.accessToken')

# 1. Create a check — verify initial result exists
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"Initial Test","type":"http","url":"https://example.com","period":"30s"}' \
  'http://localhost:4000/api/v1/orgs/default/checks' | jq '.'

curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:4000/api/v1/orgs/default/results?checkSlug=initial-test&limit=5' | jq '.data[] | {status, periodStart, output}'

# 2. Verify check status is "unknown" before first real probe
# 3. After first probe, verify status changed to UP or DOWN (not before)
# 4. Verify initial result is gray in the dashboard
```

### Automated tests
- `initial` result exists after check creation
- `initial` result does not change check status
- `initial` result does not increment status streak
- `initial` result is excluded from incident processing
- `initial` result is excluded from aggregation
- Re-enabled check gets a new `initial` result
- Check with modified URL gets a new `initial` result

**Status**: Backlog | **Created**: 2026-03-23
