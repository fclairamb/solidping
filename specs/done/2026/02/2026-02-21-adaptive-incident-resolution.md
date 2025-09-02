# Adaptive Incident Resolution

## Context

When a check alternates between UP and DOWN (flapping), the current incident system creates and resolves incidents in rapid succession. With `recovery_threshold=3` (default), a check that briefly succeeds 3 times then fails again generates a new incident each cycle. This causes:

- **Notification spam**: "created" and "resolved" notifications in rapid succession
- **Polluted incident history**: many short incidents instead of one representing the instability period
- **Alert fatigue**: users learn to ignore notifications

The current `recovery_threshold` is static and doesn't adapt to how unstable the check is. We need a mechanism that:
1. Groups flapping into a single incident (via reopening)
2. Requires increasing proof of stability before resolving (adaptive threshold)

Design decisions:
- **Reopen, don't re-create** -- a failure shortly after resolution reopens the same incident
- **Linear threshold increase** -- effective recovery threshold grows by 1 per relapse, capped by `max_adaptive_increase`
- **On by default, configurable per check** -- sensible defaults (multiplier=5, cap=5) with per-check override; set both to 0 for pre-adaptive behavior
- **Count-based, not time-based** -- consistent with existing streak model
- **Clear acknowledgment on reopen** -- the situation has changed, team needs to re-acknowledge
- **Preserve escalation on reopen** -- problem is worse if it comes back

---

## 1. Database Schema

### Migration files
- `back/internal/db/postgres/migrations/YYYYMMDD000002_adaptive_resolution.up.sql`
- `back/internal/db/postgres/migrations/YYYYMMDD000002_adaptive_resolution.down.sql`
- `back/internal/db/sqlite/migrations/YYYYMMDD000002_adaptive_resolution.up.sql`
- `back/internal/db/sqlite/migrations/YYYYMMDD000002_adaptive_resolution.down.sql`

### Changes to `incidents` table

```sql
ALTER TABLE incidents ADD COLUMN relapse_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE incidents ADD COLUMN last_reopened_at TIMESTAMPTZ;

-- Index for the cooldown lookup query
CREATE INDEX idx_incidents_check_resolved ON incidents (check_uid, resolved_at DESC)
    WHERE state = 2 AND deleted_at IS NULL;
```

| Column | Type | Notes |
|--------|------|-------|
| relapse_count | INTEGER | NOT NULL DEFAULT 0, incremented each time incident is reopened |
| last_reopened_at | TIMESTAMPTZ | nullable, set when incident is reopened |

### Changes to `checks` table

```sql
ALTER TABLE checks ADD COLUMN reopen_cooldown_multiplier INTEGER;
ALTER TABLE checks ADD COLUMN max_adaptive_increase INTEGER;
```

| Column | Type | Notes |
|--------|------|-------|
| reopen_cooldown_multiplier | INTEGER | nullable. Multiplier for the check period to compute the reopen cooldown window. NULL = use default (5). Set to 0 to disable incident reopening. |
| max_adaptive_increase | INTEGER | nullable. Maximum additional consecutive successes required per relapse. NULL = use default (5). Set to 0 to disable adaptive thresholds. |

---

## 2. Backend Models

### Modify `back/internal/db/models/incident.go`

Add to `Incident` struct:
- `RelapseCount int` (default 0)
- `LastReopenedAt *time.Time`

Add to `IncidentUpdate` struct:
- `RelapseCount *int`
- `LastReopenedAt *time.Time`
- `ClearResolvedAt bool` -- to set `resolved_at = NULL` on reopen
- `ClearAcknowledgedAt bool` -- to clear acknowledgment on reopen
- `ClearAcknowledgedBy bool` -- to clear acknowledgment on reopen

### Modify `back/internal/db/models/check.go`

Add to `Check` struct:
- `ReopenCooldownMultiplier *int` -- nullable, NULL = use default (5)
- `MaxAdaptiveIncrease *int` -- nullable, NULL = use default (5)

### New event type in `back/internal/db/models/event.go`

```go
EventTypeIncidentReopened EventType = "incident.reopened"
```

---

## 3. db.Service Interface

### New method

```
FindRecentlyResolvedIncidentByCheckUID(ctx, checkUID string, since time.Time) (*Incident, error)
```

Returns the most recently resolved incident for the check where `resolved_at >= since`. Returns `sql.ErrNoRows` if none found.

Implement in both `postgres.go` and `sqlite.go`.

### Modify `UpdateIncident`

Handle the `Clear*` boolean fields to set columns to NULL:
```go
if update.ClearResolvedAt {
    query = query.Set("resolved_at = NULL")
}
if update.ClearAcknowledgedAt {
    query = query.Set("acknowledged_at = NULL")
}
if update.ClearAcknowledgedBy {
    query = query.Set("acknowledged_by = NULL")
}
```

---

## 4. Core Logic

All changes in `back/internal/handlers/incidents/service.go`.

### Cooldown calculation

```go
const defaultCooldownMultiplier = 5

func calculateCooldown(check *models.Check) time.Duration {
    multiplier := defaultCooldownMultiplier
    if check.ReopenCooldownMultiplier != nil {
        multiplier = *check.ReopenCooldownMultiplier
    }
    if multiplier == 0 {
        return 0 // Reopening disabled for this check
    }

    period := time.Duration(check.Period)
    cooldown := time.Duration(multiplier) * period

    const minCooldown = 2 * time.Minute
    const maxCooldown = 30 * time.Minute

    if cooldown < minCooldown { cooldown = minCooldown }
    if cooldown > maxCooldown { cooldown = maxCooldown }
    return cooldown
}
```

| Check Period | Cooldown (multiplier=5) |
|-------------|----------|
| 15s | 2min (floor) |
| 30s | 2min 30s |
| 1min | 5min |
| 5min | 25min |
| 10min | 30min (ceiling) |

When `reopen_cooldown_multiplier = 0`, reopening is disabled entirely — every failure past the incident threshold creates a new incident (pre-adaptive behavior).

### Modified `handleFailure`

When no active incident exists and failure threshold is met:

1. If `reopen_cooldown_multiplier = 0` → skip reopen, create new incident
2. Calculate cooldown window from check period × multiplier
3. Query for a recently-resolved incident within the cooldown
4. **Guards** -- do NOT reopen if:
   - The incident was manually resolved (has `acknowledged_by` or resolved via user action)
   - The check was modified after the incident was resolved (`check.UpdatedAt > incident.ResolvedAt`)
5. If a recent incident passes guards → **reopen** it
6. Otherwise → create a new incident (existing behavior)

### `reopenIncident` method

```go
func (s *Service) reopenIncident(ctx, check, result, incident) error {
    // Set state back to ACTIVE
    // Clear resolved_at (NULL)
    // Increment relapse_count
    // Set last_reopened_at = now
    // Increment failure_count
    // Clear acknowledged_at and acknowledged_by
    // Emit "incident.reopened" event with {check_uid, check_slug, relapse_count, result_uid}
}
```

### Adaptive recovery threshold

In `handleSuccess`, replace the static threshold check:

```go
const defaultMaxAdaptiveIncrease = 5

func effectiveRecoveryThreshold(check *models.Check, incident *models.Incident) int {
    maxIncrease := defaultMaxAdaptiveIncrease
    if check.MaxAdaptiveIncrease != nil {
        maxIncrease = *check.MaxAdaptiveIncrease
    }

    increase := incident.RelapseCount
    if increase > maxIncrease {
        increase = maxIncrease
    }
    return check.RecoveryThreshold + increase
}
```

Progression with `recovery_threshold=3`, `max_adaptive_increase=5` (default):

| Relapses | Effective Threshold | Successes needed |
|----------|-------------------|-----------------|
| 0 | 3 | 3 consecutive |
| 1 | 4 | 4 consecutive |
| 2 | 5 | 5 consecutive |
| 3 | 6 | 6 consecutive |
| 5+ | 8 (capped) | 8 consecutive |

When `max_adaptive_increase = 0`, the recovery threshold stays fixed at `recovery_threshold` regardless of relapses (pre-adaptive behavior).

---

## 5. Notifications

### Trigger notifications for `incident.reopened`

In `emitEvent`, add `EventTypeIncidentReopened` to the notification trigger list alongside `incident.created`, `incident.resolved`, and `incident.escalated`.

### Slack notification

- Post a thread reply: ":warning: Incident reopened (relapse #N). Now requires M consecutive successes to resolve."
- Update the original Slack message back to "active" state (red color, remove "resolved" text)
- The "Acknowledge" button becomes active again (since acknowledgment was cleared)

### Webhook notification

Add `relapseCount` and `effectiveRecoveryThreshold` to the webhook payload for `incident.reopened` events.

---

## 6. API Changes

### `CheckResponse` -- add fields

```json
{
  "reopenCooldownMultiplier": 5,
  "maxAdaptiveIncrease": 5
}
```

Both fields are nullable in the response. `null` means "using default". The check creation/update (POST/PATCH) endpoints accept these fields.

### `IncidentResponse` -- add fields

```json
{
  "relapseCount": 2,
  "lastReopenedAt": "2026-02-21T10:15:00Z"
}
```

### New event type in timeline

```json
{
  "eventType": "incident.reopened",
  "payload": {
    "relapse_count": 2,
    "result_uid": "...",
    "effective_recovery_threshold": 5
  }
}
```

---

## 7. Frontend (dash0)

### API types (`apps/dash0/src/api/hooks.ts`)
- Add `relapseCount?: number` and `lastReopenedAt?: string` to `IncidentDetail`
- Add `reopenCooldownMultiplier?: number | null` and `maxAdaptiveIncrease?: number | null` to `Check`

### Check edit page
- Add "Adaptive Resolution" section in advanced settings
- `reopenCooldownMultiplier`: number input with placeholder "5 (default)". Help text: "How many check periods to wait before considering a resolved incident as truly closed. Set to 0 to disable incident reopening."
- `maxAdaptiveIncrease`: number input with placeholder "5 (default)". Help text: "Maximum extra consecutive successes required per relapse to resolve an incident. Set to 0 to disable adaptive thresholds."

### Incident detail page (`apps/dash0/src/routes/orgs/$org/incidents.$incidentUid.tsx`)
- Show "Reopened (N times)" badge next to the state badge when `relapseCount > 0`
- Show "Reopened" event in the timeline
- Show effective recovery threshold in the details card

### Incident list page (`apps/dash0/src/routes/orgs/$org/incidents.index.tsx`)
- Show small relapse count badge on incidents with `relapseCount > 0`

---

## 8. Implementation Order

1. **Database migration** -- new columns + index (PostgreSQL + SQLite)
2. **Models** -- Incident, IncidentUpdate, EventType
3. **DB layer** -- FindRecentlyResolvedIncidentByCheckUID + UpdateIncident NULL-clearing
4. **Core service logic** -- cooldown, reopen, adaptive threshold
5. **Notification handlers** -- Slack, webhook for reopened event
6. **API response** -- new fields in IncidentResponse
7. **Frontend** -- hooks types, incident detail, incident list
8. **Tests** -- unit + integration

---

## 9. Key Files

| File | Change |
|------|--------|
| `back/internal/db/postgres/migrations/..._adaptive_resolution.up.sql` | **New** -- migration |
| `back/internal/db/sqlite/migrations/..._adaptive_resolution.up.sql` | **New** -- migration |
| `back/internal/db/models/check.go` | Add ReopenCooldownMultiplier, MaxAdaptiveIncrease |
| `back/internal/db/models/incident.go` | Add RelapseCount, LastReopenedAt, Clear* fields |
| `back/internal/db/models/event.go` | Add EventTypeIncidentReopened |
| `back/internal/db/service.go` | Add FindRecentlyResolvedIncidentByCheckUID |
| `back/internal/db/postgres/postgres.go` | Implement new query + NULL-clearing |
| `back/internal/db/sqlite/sqlite.go` | Same |
| `back/internal/handlers/incidents/service.go` | Core: reopen logic, cooldown, adaptive threshold |
| `back/internal/notifications/slack.go` | Reopened message builder |
| `back/internal/notifications/webhook.go` | Add relapseCount to payload |
| `apps/dash0/src/api/hooks.ts` | Add new fields to IncidentDetail |
| `apps/dash0/src/routes/orgs/$org/incidents.$incidentUid.tsx` | Reopened badge + timeline |
| `apps/dash0/src/routes/orgs/$org/incidents.index.tsx` | Relapse badge |

---

## 10. Test Scenarios

1. **Basic reopen**: fail → incident → recover → resolve → fail within cooldown → incident reopened (not new)
2. **Cooldown expiry**: same, but fail after cooldown → new incident (relapse_count=0)
3. **Adaptive threshold**: after 2 relapses, need recovery_threshold+2 consecutive successes
4. **Threshold cap**: after 10 relapses, effective threshold capped at recovery_threshold+5
5. **Manual resolution**: manually resolved incident is NOT reopened
6. **Acknowledgment cleared**: acknowledged → resolved → reopened → acknowledgment is NULL
7. **Escalation preserved**: escalated → resolved → reopened → escalated_at preserved
8. **Check modified guard**: check updated after resolution → new incident, not reopen
9. **Notification**: Slack thread gets "reopened" message, original message updated to active
10. **Reopen disabled**: check with `reopen_cooldown_multiplier=0` → always creates new incident, never reopens
11. **Adaptive disabled**: check with `max_adaptive_increase=0` → recovery threshold stays static
12. **Custom multiplier**: check with `reopen_cooldown_multiplier=10` → longer cooldown window
13. **Default behavior**: check with both fields NULL → uses defaults (multiplier=5, cap=5)

---

## 11. Verification

### Backend
```bash
make build && make test && make lint
```

### Manual testing
```bash
SP_DB_RESET=true make dev-backend

TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"org":"default","email":"admin@solidping.com","password":"solidpass"}' \
  'http://localhost:4000/api/v1/auth/login' | jq -r '.accessToken')

# List incidents -- verify relapseCount field is present
curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:4000/api/v1/orgs/default/incidents' | jq '.data[0].relapseCount'
```

---

**Status**: Ready for Implementation | **Created**: 2026-02-21
