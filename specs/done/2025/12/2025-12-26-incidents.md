# Incidents Feature

## Overview

Incidents group consecutive check failures to prevent alert fatigue. An incident is created on first failure, updated on subsequent failures, and resolved when the check recovers.

**Key Concepts**:
- Auto-created after `incident_threshold` consecutive failures, auto-resolved after `recovery_threshold` consecutive successes
- Notifications triggered by incident state changes, not individual failures
- States: `active` → `resolved`

## State Machine

### Check Status (on `checks` table)
```
                    Result Success              Result Failure
                         │                           │
    ┌───────────┐        │        ┌──────────┐       │       ┌────────────┐
    │ UNKNOWN=0 │ ──────►├───────►│   UP=1   │◄──────┼──────►│  DOWN=2    │
    └───────────┘        │        └──────────┘       │       └────────────┘
         │               │             ▲             │             ▲
         └───────────────┘             └─────────────┴─────────────┘
```
Status values: `0=unknown`, `1=up`, `2=down`, `3=degraded` (reserved)
- `status_streak` increments on consecutive same-status results, resets to 1 on change
- `status_changed_at` updates when status transitions

### Incident State (on `incidents` table)
```
  Failures >= incident_threshold                      Successes >= recovery_threshold
               │                                                     │
               ▼                                                     ▼
        ┌────────────┐                                        ┌───────────┐
        │  ACTIVE=1  │ ──────────────────────────────────────►│RESOLVED=2 │
        └────────────┘                                        └───────────┘
```
State values: `1=active`, `2=resolved`

**Notification Triggers**: `incident.created` (ACTIVE), `incident.escalated` (after `escalation_threshold`), `incident.resolved` (RESOLVED)

## Database Schema

All SQL migrations consolidated below:

```sql
-- =============================================================================
-- CHECKS TABLE (new columns for incident tracking)
-- =============================================================================
-- These columns should be added to the existing checks table definition:
--
--   incident_threshold INTEGER NOT NULL DEFAULT 1,
--       -- Number of consecutive failures required to create an incident (default: 1 = immediate)
--   escalation_threshold INTEGER NOT NULL DEFAULT 3,
--       -- Number of failures in an incident before triggering escalation notification
--   recovery_threshold INTEGER NOT NULL DEFAULT 1,
--       -- Number of consecutive successes required to resolve an incident (default: 1 = immediate)
--   status SMALLINT NOT NULL DEFAULT 0,
--       -- Current health status: 0=unknown, 1=up, 2=down, 3=degraded
--   status_streak INTEGER NOT NULL DEFAULT 0,
--       -- Count of consecutive results in the current status
--   status_changed_at TIMESTAMPTZ,
--       -- Timestamp when the status last changed

-- =============================================================================
-- INCIDENTS TABLE
-- =============================================================================
CREATE TABLE incidents (
    uid UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_uid UUID NOT NULL REFERENCES organizations(uid) ON DELETE CASCADE,
    check_uid UUID NOT NULL REFERENCES checks(uid) ON DELETE CASCADE,
        -- Checks are always soft-deleted (deleted_at timestamp), never hard-deleted from API.
        -- Application logic blocks soft-deletion while active incidents exist.
    state SMALLINT NOT NULL DEFAULT 1,
        -- Incident state: 1=active, 2=resolved
    started_at TIMESTAMPTZ NOT NULL,
        -- Timestamp of the check result that triggered incident creation
    resolved_at TIMESTAMPTZ,
        -- Timestamp of the check result that triggered resolution
    escalated_at TIMESTAMPTZ,
        -- Timestamp when escalation was triggered (NULL = not yet escalated, only one escalation per incident)
    acknowledged_at TIMESTAMPTZ,
    acknowledged_by UUID REFERENCES users(uid),
    failure_count INTEGER NOT NULL DEFAULT 1,
    title TEXT,
    description TEXT,                          -- Future
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Incidents indexes
CREATE INDEX idx_incidents_org_check_state ON incidents(org_uid, check_uid, state)
    WHERE state = 1; -- active incidents only
CREATE INDEX idx_incidents_org_started ON incidents(org_uid, started_at DESC);
CREATE INDEX idx_incidents_org_state_started ON incidents(org_uid, state, started_at DESC);

-- =============================================================================
-- EVENTS TABLE
-- =============================================================================
CREATE TABLE events (
    uid UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_uid UUID NOT NULL REFERENCES organizations(uid) ON DELETE CASCADE,
    incident_uid UUID REFERENCES incidents(uid) ON DELETE CASCADE,
    check_uid UUID REFERENCES checks(uid) ON DELETE CASCADE,
    job_uid UUID, -- references jobs(uid)
    event_type VARCHAR(50) NOT NULL,
    actor_type VARCHAR(20) NOT NULL CHECK (actor_type IN ('system', 'user')),
    actor_uid UUID REFERENCES users(uid),
    payload JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Events indexes
CREATE INDEX idx_events_org_created ON events(org_uid, created_at DESC);
CREATE INDEX idx_events_org_incident_created ON events(org_uid, incident_uid, created_at)
    WHERE incident_uid IS NOT NULL;
CREATE INDEX idx_events_check_created ON events(check_uid, created_at DESC)
    WHERE check_uid IS NOT NULL;
CREATE INDEX idx_events_type_created ON events(event_type, created_at DESC);
CREATE INDEX idx_events_actor ON events(actor_uid, created_at DESC)
    WHERE actor_uid IS NOT NULL;
```

## REST API

### Incidents

#### List Incidents
```
GET /api/v1/orgs/$org/incidents
```

**Query Parameters**:
| Param | Description |
|-------|-------------|
| `check_uid` | Filter by check UID/slug (comma-separated) |
| `state` | Filter: `active`, `resolved` (comma-separated) |
| `since` | ISO 8601 timestamp |
| `until` | ISO 8601 timestamp |
| `cursor` | Pagination cursor (UUID) |
| `size` | Results per page (default: 20, max: 100) |
| `with` | Include: `check_slug`, `check_name`, `timeline`, `metrics`, `events` |

**Response**:
```json
{
  "data": [{
    "uid": "550e8400-e29b-41d4-a716-446655440000",
    "checkUid": "660e8400-e29b-41d4-a716-446655440000",
    "state": "resolved",
    "startedAt": "2025-12-26T10:00:00Z",
    "resolvedAt": "2025-12-26T10:15:00Z",
    "failureCount": 5,
    "title": "my-api-check is down"
  }],
  "pagination": {"cursor": "550e8400-e29b-41d4-a716-446655440000", "size": 20}
}
```

#### Get Incident
```
GET /api/v1/orgs/$org/incidents/$incident_uid
```

Returns single incident with timeline by default.

#### Acknowledge Incident (Future)
```
POST /api/v1/orgs/$org/incidents/$incident_uid/acknowledge
Body: {"notes": "Investigating..."}
```

### Events

#### List Events
```
GET /api/v1/orgs/$org/events
GET /api/v1/orgs/$org/incidents/$incident_uid/events
GET /api/v1/orgs/$org/checks/$check_uid/events
```

**Query Parameters**: `event_type`, `check_uid`, `incident_uid`, `actor_type`, `since`, `until`, `cursor`, `size`

### Event Types

| Event Type | Trigger | Payload |
|------------|---------|---------|
| `check.created` | Check created | `{name, slug, type}` |
| `check.updated` | Check updated | `{changes: {...}}` |
| `check.deleted` | Check deleted | `{name, slug}` |
| `incident.created` | First failure | `{state, check_result_uid, failure_reason}` |
| `incident.escalated` | Threshold reached (once per incident) | `{failure_count, escalation_threshold}` |
| `incident.resolved` | Check succeeds | `{from_state, duration_seconds, total_failures}` |
| `notification.queued` | Notification scheduled | `{channel, recipient}` |
| `notification.sent` | Delivered | `{channel, recipient, message_id}` |
| `notification.failed` | Failed | `{channel, recipient, error}` |
| `incident.acknowledged` | User acks | `{notes}` |

## Business Logic

### Check Status Update

When a check result is recorded, update the check's status tracking:

```go
const (
    CheckStatusUnknown  = 0
    CheckStatusUp       = 1
    CheckStatusDown     = 2
    CheckStatusDegraded = 3 // reserved for future use
)

const (
    IncidentStateActive   = 1
    IncidentStateResolved = 2
)

func (s *Service) UpdateCheckStatus(ctx context.Context, check *Check, result *CheckResult) error {
    now := time.Now()
    newStatus := statusFromResult(result)

    if check.Status == newStatus {
        // Same status: increment streak
        check.StatusStreak++
    } else {
        // Status changed: reset streak and update timestamp
        check.Status = newStatus
        check.StatusStreak = 1
        check.StatusChangedAt = &now
    }

    return s.repo.UpdateCheckStatus(ctx, check)
}

func statusFromResult(result *CheckResult) int {
    if result.Success {
        return CheckStatusUp
    }
    return CheckStatusDown
}
```

### Incident Management

After updating check status, manage incidents based on thresholds:

```go
func (s *Service) ProcessCheckResult(ctx context.Context, check *Check, result *CheckResult) error {
    // 1. Update check status (streak counter)
    s.UpdateCheckStatus(ctx, check, result)

    // 2. Find active incident
    incident, _ := s.repo.FindActiveIncident(ctx, check.UID)

    // 3. Handle based on result and thresholds
    if !result.Success {
        // FAILURE
        if incident == nil {
            // Check if we've hit the incident threshold
            if check.StatusStreak >= check.IncidentThreshold {
                // Create new incident
                incident = s.CreateIncident(ctx, check, result)
                incident.StartedAt = result.CreatedAt // Use result timestamp
                s.EmitEvent("incident.created", incident)
            }
        } else {
            // Update existing incident
            incident.FailureCount++

            // Trigger escalation once when crossing the threshold
            if incident.EscalatedAt == nil &&
               incident.FailureCount >= check.EscalationThreshold {
                now := time.Now()
                incident.EscalatedAt = &now
                s.EmitEvent("incident.escalated", incident)
            }
            s.repo.UpdateIncident(ctx, incident)
        }
    } else {
        // SUCCESS
        if incident != nil {
            // Check if we've hit the recovery threshold
            if check.StatusStreak >= check.RecoveryThreshold {
                incident.State = IncidentStateResolved
                incident.ResolvedAt = result.CreatedAt // Use result timestamp
                s.repo.UpdateIncident(ctx, incident)
                s.EmitEvent("incident.resolved", incident)
            }
            // Note: If success but below recovery_threshold, incident remains active.
            // The check.StatusStreak tracks recovery progress. A new failure will
            // reset StatusStreak to 1 and continue incrementing incident.FailureCount.
        }
    }

    return nil
}
```

### Partial Recovery Behavior

When an incident exists and successes occur but don't meet `recovery_threshold`:

1. `check.StatusStreak` increments with each success
2. Incident remains in `active` state
3. If a failure occurs before reaching `recovery_threshold`:
   - `check.StatusStreak` resets to 1 (now tracking failures)
   - `check.Status` changes to `down`
   - `incident.FailureCount` increments
   - Recovery progress is lost; must start over with consecutive successes

This prevents premature resolution during flapping conditions.

### Threshold Behavior Examples

| Scenario | incident_threshold | escalation_threshold | recovery_threshold | Behavior |
|----------|-------------------|---------------------|-------------------|----------|
| Sensitive | 1 | 3 | 1 | Incident on first failure, escalate at 3, resolve on first success |
| Flap-resistant | 3 | 5 | 2 | Needs 3 failures to incident, escalate at 5, 2 successes to resolve |
| Conservative | 5 | 10 | 3 | Tolerates intermittent failures, late escalation, stable recovery |

**Title Generation**: `{check_slug} is down`

**Metrics** (calculated on-demand):
- Duration: `resolved_at - started_at`
- MTTR: Same as duration for resolved
- Avg failure duration: Average `durationMs` of failed results

## CLI Client

```bash
# Incidents
solidping client incidents list
solidping client incidents list --state active --check my-api
solidping client incidents get inc_123

# Events
solidping client events list
solidping client events list --type incident.created,incident.resolved
solidping client incidents events inc_123
solidping client checks events my-api-check
```

**Output**:
```
UID          CHECK            STATE     STARTED              FAILURES  DURATION
inc_abc123   my-api-check     resolved  2025-12-26 10:00:00  5         15m
inc_def456   db-health        active    2025-12-26 11:30:00  3         30m
```

**Timezone**: All timestamps are displayed in the local timezone, respecting the `TZ` environment variable.

## Implementation Steps

### Step 1: Database Schema

1.1. **Migration: Add columns to `checks` table**
- Add `incident_threshold INTEGER NOT NULL DEFAULT 1`
- Add `escalation_threshold INTEGER NOT NULL DEFAULT 3`
- Add `recovery_threshold INTEGER NOT NULL DEFAULT 1`
- Add `status SMALLINT NOT NULL DEFAULT 0`
- Add `status_streak INTEGER NOT NULL DEFAULT 0`
- Add `status_changed_at TIMESTAMPTZ`

1.2. **Migration: Create `incidents` table**
- Create table with all columns as specified in schema
- Create indexes: `idx_incidents_org_check_state`, `idx_incidents_org_started`, `idx_incidents_org_state_started`

1.3. **Migration: Create `events` table**
- Create table with all columns as specified in schema
- Create indexes: `idx_events_org_created`, `idx_events_org_incident_created`, `idx_events_check_created`, `idx_events_type_created`, `idx_events_actor`

1.4. **Update Check model**
- Add new fields to `Check` struct
- Update `CheckCreate`/`CheckUpdate` DTOs to include threshold fields
- Update check repository methods

### Step 2: Core Domain Logic

2.1. **Create Incident model and repository**
- Define `Incident` struct with all fields
- Define `IncidentState` constants (`Active=1`, `Resolved=2`)
- Implement `IncidentRepository` interface:
  - `Create(ctx, incident) error`
  - `Update(ctx, incident) error`
  - `FindByUID(ctx, orgUID, uid) (*Incident, error)`
  - `FindActiveByCheckUID(ctx, checkUID) (*Incident, error)`
  - `List(ctx, orgUID, filters) ([]Incident, error)`

2.2. **Create Event model and repository**
- Define `Event` struct with all fields
- Define `EventType` constants for all event types
- Implement `EventRepository` interface:
  - `Create(ctx, event) error`
  - `List(ctx, orgUID, filters) ([]Event, error)`

2.3. **Create Check Status constants**
- Define `CheckStatus` constants (`Unknown=0`, `Up=1`, `Down=2`, `Degraded=3`)

2.4. **Implement IncidentService**
- `ProcessCheckResult(ctx, check, result)` - main entry point after check execution
- `UpdateCheckStatus(ctx, check, result)` - update status/streak on check
- `createIncident(ctx, check, result)` - create new incident with title generation
- `resolveIncident(ctx, incident, result)` - resolve incident
- `emitEvent(ctx, eventType, incident, payload)` - record event

2.5. **Integrate with check execution flow**
- Call `IncidentService.ProcessCheckResult()` after each check result is saved
- Ensure transactional consistency

### Step 3: Check Deletion Protection

3.1. **Update CheckService.Delete()**
- Before soft-deleting, query for active incidents on the check
- If active incidents exist, return `CHECK_HAS_ACTIVE_INCIDENTS` error

### Step 4: REST API - Incidents

4.1. **Create incidents handler** (`back/internal/web/api/v1/incidents/`)
- `GET /api/v1/orgs/:org/incidents` - List incidents with filters
- `GET /api/v1/orgs/:org/incidents/:uid` - Get single incident

4.2. **Implement List Incidents endpoint**
- Parse query params: `check_uid`, `state`, `since`, `until`, `cursor`, `size`
- Support `with` param for includes: `check_slug`, `check_name`
- Return paginated response

4.3. **Implement Get Incident endpoint**
- Fetch incident by UID
- Return 404 with `INCIDENT_NOT_FOUND` if not found

4.4. **Register routes**
- Add incident routes to router

### Step 5: REST API - Events

5.1. **Create events handler** (`back/internal/web/api/v1/events/`)
- `GET /api/v1/orgs/:org/events` - List all events
- `GET /api/v1/orgs/:org/incidents/:uid/events` - List events for incident
- `GET /api/v1/orgs/:org/checks/:uid/events` - List events for check

5.2. **Implement List Events endpoint**
- Parse query params: `event_type`, `check_uid`, `incident_uid`, `actor_type`, `since`, `until`, `cursor`, `size`
- Return paginated response

5.3. **Register routes**
- Add event routes to router

### Step 6: Tests

6.1. **Unit tests for IncidentService**
- Test `ProcessCheckResult` with various scenarios:
  - First failure creates incident (when threshold=1)
  - Multiple failures before threshold (when threshold>1)
  - Failure count increments on existing incident
  - Escalation triggers once at threshold
  - Success resolves incident (when recovery_threshold=1)
  - Partial recovery (successes below threshold don't resolve)
  - Flapping behavior (success then failure resets streak)

6.2. **Repository integration tests**
- Test incident CRUD operations
- Test event creation and listing
- Test filters and pagination

6.3. **API integration tests**
- Test incidents list/get endpoints
- Test events list endpoints
- Test error responses

### Step 7: CLI Client

7.1. **Add `incidents` command group**
- `solidping client incidents list` - List incidents
- `solidping client incidents get <uid>` - Get incident details

7.2. **Add `events` command group**
- `solidping client events list` - List events

7.3. **Implement table output**
- Format timestamps using `TZ` environment variable
- Display duration in human-readable format

### Future Steps (Not in MVP)

- **Acknowledgment**: `POST /api/v1/orgs/:org/incidents/:uid/acknowledge`
- **Email notifications**: Trigger on `incident.created`, `incident.escalated`, `incident.resolved`
- **Webhook notifications**: POST to configured URLs on incident events

## Error Codes

- `INCIDENT_NOT_FOUND` - Incident UID not found
- `CHECK_NOT_FOUND` - Check does not exist
- `CHECK_HAS_ACTIVE_INCIDENTS` - Cannot delete check while active incidents exist
- `VALIDATION_ERROR` - Invalid parameters

## Open Questions

1. ~~**Thresholds**: Hardcode for v1, make configurable later~~ → Resolved: `incident_threshold`, `escalation_threshold`, and `recovery_threshold` are per-check configurable
2. **Flapping detection**: New incident after any success for v1 (use higher `recovery_threshold` to mitigate)
3. **Manual incidents**: Auto-only for v1
4. **Severity levels**: Not in v1

---

**Status**: Ready for Implementation | **Created**: 2025-12-26 | **Updated**: 2025-12-29
