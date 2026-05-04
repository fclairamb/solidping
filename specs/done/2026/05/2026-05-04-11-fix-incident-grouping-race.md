# Incident grouping: prevent duplicate group incidents on concurrent failures

## Context

The group incident correlation work shipped in `specs/done/2026/04/2026-04-29-04-group-incident-correlation.md` (commit `feat: group incident correlation (spec 04 backend v1) (#31)`). Routing, member tracking, and AND-recovery semantics are all implemented in `server/internal/handlers/incidents/service.go`.

The user reports that incident grouping "doesn't seem to work correctly". The most likely cause based on code inspection: **a race on group-incident creation**.

`Service.handleGroupFailure` → `createOrReopenGroupIncident` → `s.createGroupIncident` (`server/internal/handlers/incidents/service.go:608-627`):

```go
incident := models.NewIncident(check.OrganizationUID, check.UID, result.PeriodStart, title)
incident.CheckGroupUID = check.CheckGroupUID
if err := s.db.CreateIncident(ctx, incident); err != nil {
    return fmt.Errorf("failed to create group incident: %w", err)
}
```

There's no uniqueness guard. When two checks in the same group fail in the same scheduler tick:
1. Both call `routeCheckResult` concurrently.
2. Both call `FindActiveIncidentByGroupUID` → both get `sql.ErrNoRows`.
3. Both call `CreateIncident` → both succeed.

Result: two parallel active group incidents for the same group. Each holds a partial member list, each resolves independently, and notifications fire twice. From the user's perspective, "grouping doesn't work" — the second check that fails doesn't appear to join the first one's incident.

A secondary concern flagged during exploration: confirm `FindActiveIncidentByGroupUID` is implemented for both SQLite and Postgres backends. If only SQLite has it, the Postgres path silently no-ops the lookup, exacerbating the race.

## Scope

**In scope:**
- Add a partial unique index (or unique constraint) on active group incidents:
  - SQLite: `CREATE UNIQUE INDEX uq_active_group_incident ON incidents(organization_uid, check_group_uid) WHERE state='active' AND check_group_uid IS NOT NULL;`
  - Postgres: same syntax (Postgres supports partial indexes natively).
- Wrap the `FindActiveIncidentByGroupUID` + `CreateIncident` flow in a transaction; on unique-violation from the index, re-fetch the existing incident and fall through to "join as a new member".
- Verify (and add if missing) `FindActiveIncidentByGroupUID` in both DB backends.
- A focused stress test that fires two concurrent failures in the same group and asserts exactly one active group incident with two members.
- Audit the recovery path (`CountFailingIncidentMembers`, `service.go:761-768`) for any assumption that breaks under the merged-into-one model.

**Out of scope:**
- Multi-region quorum, weighted member criticality (explicitly out of the original spec).
- A general-purpose "outbox" or job queue for incident events.

## Approach

### 1. DB migration

New migration pair under `server/internal/db/sqlite/migrations/` and `server/internal/db/postgres/migrations/`:

```sql
-- up
CREATE UNIQUE INDEX IF NOT EXISTS uq_active_group_incident
  ON incidents(organization_uid, check_group_uid)
  WHERE state='active' AND check_group_uid IS NOT NULL;
-- down
DROP INDEX IF EXISTS uq_active_group_incident;
```

Note: the existing `idx_incidents_active_by_group` is non-unique (per the original spec); leave it for query performance. The new index enforces invariant.

### 2. Service logic

`server/internal/handlers/incidents/service.go` — refactor `createOrReopenGroupIncident` to be transactional and idempotent:

```go
return s.db.WithTx(ctx, func(tx Tx) error {
    existing, err := tx.FindActiveIncidentByGroupUID(ctx, check.OrganizationUID, *check.CheckGroupUID)
    if err == nil && existing != nil {
        return s.attachMemberToGroupIncident(ctx, tx, existing, check, result)
    }
    incident := models.NewIncident(...)
    incident.CheckGroupUID = check.CheckGroupUID
    if err := tx.CreateIncident(ctx, incident); err != nil {
        if errors.Is(err, ErrUniqueViolation) {
            // Race-loser: someone else just created it. Re-read and attach.
            existing, err := tx.FindActiveIncidentByGroupUID(ctx, ...)
            if err != nil { return err }
            return s.attachMemberToGroupIncident(ctx, tx, existing, check, result)
        }
        return err
    }
    return s.attachMemberToGroupIncident(ctx, tx, incident, check, result)
})
```

`ErrUniqueViolation` needs to be a typed sentinel mapped from each backend's native unique-violation error code (SQLite `2067`, Postgres `23505`). If a generic mapper doesn't exist, add one in `server/internal/db/`.

### 3. Backend parity

Audit `server/internal/db/postgres/incidents.go` (or wherever incident DB methods live) for:
- `FindActiveIncidentByGroupUID` — present and correct?
- `CountFailingIncidentMembers` — present and correct?

If either is missing or stubbed, implement against the same SQL the SQLite path uses.

### 4. Tests

`server/internal/handlers/incidents/service_test.go`:

```go
func TestGroupIncident_ConcurrentFailures_OneIncident(t *testing.T) {
    // Setup: 2 checks in same group, both about to fail.
    var wg sync.WaitGroup
    for _, c := range checks {
        wg.Add(1)
        go func(c *models.Check) {
            defer wg.Done()
            svc.ProcessCheckResult(ctx, c, downResult)
        }(c)
    }
    wg.Wait()
    incidents := db.ListActiveIncidents(ctx, orgUID)
    require.Len(t, incidents, 1)
    members := db.ListIncidentMembers(ctx, incidents[0].UID)
    require.Len(t, members, 2)
}
```

Run with `-race`. Repeat 100x via `t.Parallel()` or a loop to actually exercise the race.

### 5. Manual diagnosis aid

Add a one-line warning log at the unique-violation branch ("group incident race resolved, joined existing UID=…"). This makes future investigations cheap.

## Verification

1. `go test -race ./server/internal/handlers/incidents/...` passes the new test 100x.
2. On a dev environment, fail two checks in the same group within the same second; assert exactly one active group incident in `incidents` table.
3. Notifications fire once for the group, not twice.
4. Recovery: after both checks recover, the single group incident transitions to `resolved`.
