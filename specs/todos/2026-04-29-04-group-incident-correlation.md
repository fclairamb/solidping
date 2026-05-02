# Group-Based Incident Correlation

## Goal

When a shared dependency fails (one DB, one network segment, one upstream provider), several checks fail at the same time and SolidPing currently produces one incident per check, sending one notification per channel per check. With nine notification senders and a dozen affected checks, an outage becomes a 100-message alert storm. This spec collapses simultaneous failures inside a `check_group` into a **single group incident** with a per-member timeline, so each notification channel fires once per group event instead of once per check.

The deliverable is a state machine and storage change inside `internal/handlers/incidents/`. No new check type. No new notification channel. No new UI screen — the existing incident list, detail page, and Slack/Discord/email templates render group incidents transparently with member detail.

## Why now

- The product has shipped 30 check types and 9 notification channels. The dominant remaining user pain is alert volume during a real outage, not missing protocols or channels.
- Adding more notification channels (Telegram/Teams/PagerDuty) before this would amplify the spam on more platforms.
- The infrastructure is already there: `check_groups` exists with a flat `Check.CheckGroupUID *string` membership; the incident state machine in `incidents/service.go` is the single integration point; `IsCheckInActiveMaintenance` is the prior art for the same kind of "should this check participate?" gate.
- BetterStack ships this as "automatic incident merging" and uses it as a sales bullet. No self-hosted competitor has it.

## Scope

In scope:
- A new optional `check_group_uid` column on `incidents`.
- A new join table `incident_member_checks` tracking which checks are participating in a group incident and their per-check failure/recovery state.
- Routing logic in `incidents.Service.ProcessCheckResult` that attaches grouped checks to the active group incident instead of creating a per-check incident.
- Group-level resolution rule: the group incident resolves when **every** member has individually met its `recovery_threshold` (adaptive resolution still applies per-member; group resolution is the AND of all members).
- Group-level escalation: the group incident's `escalated_at` fires the first time any member's individual escalation threshold is reached.
- Group-level acknowledgment: one ack covers the whole incident (existing behavior, just at the group key).
- Notification fan-out: union the connections of every currently-failing member, dedup by connection UID, dispatch one notification job per (connection, event-type).
- API surface: list/get incident endpoints return `checkGroupUid`, `members[]`, and per-member per-event timeline. Existing per-check incident shape is unchanged when `checkGroupUid` is null.
- Frontend: incident list and detail render group incidents (title, member list, member-level mini-timeline). No new pages.

Out of scope:
- Changing the `check_groups` model itself (still flat, still single-membership).
- Per-region group incidents. Today incidents aren't split per region; the group incident inherits that.
- Group-level threshold overrides on `check_groups` (the per-member thresholds keep deciding when each member joins or leaves; the group has no thresholds of its own in v1).
- Quorum-based resolution (e.g. "resolve when 80% of members are healthy"). v1 uses strict AND.
- Multi-group membership (the model only allows one).

---

## Pre-decisions for the spec's open questions

`specs/ideas/2026-03-21-group-incidents.md` left four open questions. v1 nails them down:

| Question | v1 answer | Why |
|----------|-----------|-----|
| Resolve when all checks recover, or quorum? | **All checks must recover.** | Strict AND is the obvious mental model and matches the "single shared dependency" use case. Quorum can land later if a real workload demands it. |
| Membership churn during an active incident? | **Joining**: a check moved into the group while the group incident is active and the check is currently failing → joins the incident as a new member. **Leaving**: a check moved out of the group stays attached to the existing incident (`incident_member_checks` keeps the row); after the incident resolves, the check goes back to per-check behavior. | Lossless — a removal during an outage is rare and shouldn't drop the audit trail. |
| Title format? | `"<Group name> — N/M checks down"` where N is the count of currently-failing members and M is the total members of the group at incident-create time. Member slugs go in the body, not the title. | Stable, scannable in Slack channel summaries. |
| Multi-group membership? | **Moot** — `Check.CheckGroupUID` is a single optional pointer. A check is in 0 or 1 group. | The model already enforces it. |

---

## 1. Database schema

### Migration: `003_group_incidents.up.sql`

```sql
-- Tag incidents with the group they belong to (NULL = traditional per-check incident).
ALTER TABLE incidents
  ADD COLUMN check_group_uid VARCHAR(36) NULL
    REFERENCES check_groups(uid) ON DELETE SET NULL;

CREATE INDEX idx_incidents_active_by_group
  ON incidents (check_group_uid, state)
  WHERE check_group_uid IS NOT NULL AND deleted_at IS NULL;

-- Per-member state inside a group incident.
CREATE TABLE incident_member_checks (
  incident_uid       VARCHAR(36) NOT NULL REFERENCES incidents(uid) ON DELETE CASCADE,
  check_uid          VARCHAR(36) NOT NULL REFERENCES checks(uid) ON DELETE CASCADE,
  joined_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
  first_failure_at   TIMESTAMPTZ NOT NULL,
  last_failure_at    TIMESTAMPTZ NOT NULL,
  last_recovery_at   TIMESTAMPTZ NULL,
  failure_count      INT NOT NULL DEFAULT 1,
  currently_failing  BOOLEAN NOT NULL DEFAULT TRUE,
  PRIMARY KEY (incident_uid, check_uid)
);

CREATE INDEX idx_incident_member_checks_check
  ON incident_member_checks (check_uid)
  WHERE currently_failing = TRUE;
```

The mirrored SQLite migration uses the same shape — `BOOLEAN` is fine, `TIMESTAMPTZ` becomes `DATETIME`, and partial indexes are supported on both backends.

### Down migration

```sql
DROP TABLE incident_member_checks;
DROP INDEX idx_incidents_active_by_group;
ALTER TABLE incidents DROP COLUMN check_group_uid;
```

### Model changes

```go
// internal/db/models/incident.go
type Incident struct {
    // ...existing fields...
    CheckGroupUID *string `bun:"check_group_uid"`
}

// new file: internal/db/models/incident_member.go
type IncidentMemberCheck struct {
    IncidentUID      string     `bun:"incident_uid,pk"`
    CheckUID         string     `bun:"check_uid,pk"`
    JoinedAt         time.Time  `bun:"joined_at,notnull,default:current_timestamp"`
    FirstFailureAt   time.Time  `bun:"first_failure_at,notnull"`
    LastFailureAt    time.Time  `bun:"last_failure_at,notnull"`
    LastRecoveryAt   *time.Time `bun:"last_recovery_at"`
    FailureCount     int        `bun:"failure_count,notnull,default:1"`
    CurrentlyFailing bool       `bun:"currently_failing,notnull,default:true"`
}

type IncidentMemberUpdate struct {
    LastFailureAt    *time.Time
    LastRecoveryAt   *time.Time
    FailureCount     *int
    CurrentlyFailing *bool
}
```

### `db.Service` additions

```go
// internal/db/service.go
FindActiveIncidentByGroupUID(ctx context.Context, groupUID string) (*models.Incident, error)
FindRecentlyResolvedIncidentByGroupUID(ctx context.Context, groupUID string, since time.Time) (*models.Incident, error)
ListIncidentMemberChecks(ctx context.Context, incidentUID string) ([]*models.IncidentMemberCheck, error)
GetIncidentMemberCheck(ctx context.Context, incidentUID, checkUID string) (*models.IncidentMemberCheck, error)
UpsertIncidentMemberCheck(ctx context.Context, member *models.IncidentMemberCheck) error
UpdateIncidentMemberCheck(ctx context.Context, incidentUID, checkUID string, update *models.IncidentMemberUpdate) error
CountFailingIncidentMembers(ctx context.Context, incidentUID string) (int, error)
```

The PG implementation is straightforward; the SQLite implementation has matching `ON CONFLICT … DO UPDATE` for `UpsertIncidentMemberCheck`.

The existing `FindActiveIncidentByCheckUID` keeps its current semantics — it returns the incident a single check is participating in, whether per-check or via a group. The query becomes `WHERE state = active AND (check_uid = $1 OR uid IN (SELECT incident_uid FROM incident_member_checks WHERE check_uid = $1 AND currently_failing = TRUE))`. This means `ProcessCheckResult` can call it unchanged on the success path and discover the group incident the check is attached to.

---

## 2. State machine

The integration point is `incidents.Service.ProcessCheckResult` in `internal/handlers/incidents/service.go`. Today it (a) updates check status/streak, (b) finds the active per-check incident, (c) handles failure or success. The change replaces step (b) and the failure-creation branch.

### Failure path

```text
ProcessCheckResult(check, result)  // result.Status ∈ {DOWN, TIMEOUT, ERROR}
└── update check.Status / streak (unchanged)
└── if check.IsInActiveMaintenance: return (unchanged)
└── findIncidentForCheck(check):
    if check.CheckGroupUID != nil:
        return FindActiveIncidentByGroupUID(*check.CheckGroupUID)
    else:
        return FindActiveIncidentByCheckUID(check.UID)

└── if incident == nil and streak >= IncidentThreshold:
        if check.CheckGroupUID != nil:
            createOrReopenGroupIncident(check, result)
        else:
            createOrReopenIncident(check, result)            // existing
└── if incident != nil:
        if incident.CheckGroupUID != nil:
            handleGroupFailure(check, result, incident)
        else:
            handleFailure(check, result, incident)           // existing
```

`createOrReopenGroupIncident`:
1. Try to reopen a recently-resolved group incident (cooldown rules unchanged, but key on `CheckGroupUID`).
2. If reopened: insert/upsert this check's `incident_member_checks` row with `currently_failing=true`, `failure_count=1`, `first_failure_at = result.PeriodStart`. Increment `incident.RelapseCount` and `incident.FailureCount` (failure_count on a group incident = total member-failure events, not member count — see "Counters" below).
3. Otherwise create a new incident with `CheckUID = check.UID` (trigger), `CheckGroupUID = group.UID`, title = `formatGroupTitle(group, 1, totalMembers)`, then insert the first member row.

`handleGroupFailure`:
1. Look up `incident_member_checks` row for this check.
   - Missing → this is a new member joining an active group incident. Insert with `currently_failing=true`, `failure_count=1`, `joined_at=now`. Increment incident's `failure_count` and rebuild title.
   - Present and `currently_failing=true` → just bump `failure_count++`, set `last_failure_at = result.PeriodStart`, increment incident's `failure_count`.
   - Present and `currently_failing=false` → this member had recovered and is failing again. Set `currently_failing=true`, bump `failure_count++`, increment incident's `failure_count`. **This is a per-member relapse but does not touch `incident.RelapseCount`** (relapse is a property of the group incident's resolved→active transition, not a per-member flap inside the incident).
2. If the member's individual `escalation_threshold` is met (cumulative failure_count of this member ≥ check.EscalationThreshold) and the incident isn't escalated yet → set `incident.EscalatedAt = now` and emit `EventTypeIncidentEscalated`.

### Success path

```text
ProcessCheckResult(check, result)  // result.Status == UP
└── update check.Status / streak (unchanged)
└── incident, _ := FindActiveIncidentByCheckUID(check.UID)   // already finds group incidents (see schema note)
└── if incident == nil: return
└── if incident.CheckGroupUID != nil:
        handleGroupSuccess(check, result, incident)
    else:
        handleSuccess(check, result, incident)               // existing
```

`handleGroupSuccess`:
1. Look up `incident_member_checks` row for this check.
   - Missing → no-op (the check belonged to the group but was never failing in this incident).
   - Present and `currently_failing=false` → no-op.
   - Present and `currently_failing=true` → check whether `check.StatusStreak >= effectiveRecoveryThreshold(check, incident)`. If yes, mark this member recovered: `currently_failing=false`, `last_recovery_at = result.PeriodStart`. Otherwise no-op (still recovering).
2. After flipping a member to recovered, call `CountFailingIncidentMembers`. If 0 → resolve the group incident exactly like the per-check `resolveIncident` (set state, emit `EventTypeIncidentResolved`). If > 0 → just rebuild the title to reflect the new failing count.

### Counters

| Field on group incident | Meaning |
|-------------------------|---------|
| `failure_count` | Sum of every member's `failure_count` — total number of failed probes across all members during this incident. Used by escalation. |
| `relapse_count` | Number of times the group incident has gone from `resolved` → `active` (inherits per-incident semantics). Per-member intra-incident flapping does not bump this. |
| `escalated_at` | Set the first time any member crosses its individual `escalation_threshold`. Never reset within an incident. |
| `acknowledged_at`, `acknowledged_by` | Set once at the group level; cleared on reopen (existing behavior). |

### Maintenance windows

`IsCheckInActiveMaintenance(checkUID)` is called per check at the very top of `ProcessCheckResult` and returns early. Group incidents need no extra logic: a member in maintenance never reaches the routing code, so it neither joins nor blocks a group incident's resolution. If every member of a group is in maintenance, the group incident never opens. If only some members are in maintenance, the group incident represents the non-maintained subset accurately.

---

## 3. Notifications

Today, `queueNotifications(ctx, orgUID, checkUID, incidentUID, eventType)` calls `ListConnectionsForCheck(checkUID)` and creates one notification job per enabled connection. For group incidents, the connection set is the **union of every currently-failing member's connections, deduplicated by connection UID**.

```go
func (s *Service) queueGroupNotifications(
    ctx context.Context, orgUID, incidentUID string, eventType models.EventType,
) {
    members, err := s.db.ListIncidentMemberChecks(ctx, incidentUID)
    if err != nil { /* warn, return */ }

    seen := make(map[string]bool)
    for _, m := range members {
        if !m.CurrentlyFailing && eventType != models.EventTypeIncidentResolved {
            continue   // recovered members don't bring their channels into mid-incident events
        }
        conns, err := s.db.ListConnectionsForCheck(ctx, m.CheckUID)
        if err != nil { continue }
        for _, c := range conns {
            if !c.Enabled || seen[c.UID] { continue }
            seen[c.UID] = true
            s.enqueueNotificationJob(ctx, orgUID, c.UID, incidentUID, eventType)
        }
    }
}
```

`emitEvent` dispatches to either `queueGroupNotifications` or the existing `queueNotifications` based on `incident.CheckGroupUID`. Notification senders themselves do not change — the existing payload-build logic already loads the incident by UID; the templates simply read the new fields (see §4) and render a member list when present.

The four event types fired for a group incident match per-check:
- `EventTypeIncidentCreated` — first member crosses threshold.
- `EventTypeIncidentEscalated` — first member crosses its individual escalation threshold.
- `EventTypeIncidentReopened` — group incident transitions resolved → active during cooldown.
- `EventTypeIncidentResolved` — last failing member recovers.

Per-member transitions (a member joining mid-incident, a member recovering while others still fail, a member relapsing within an incident) **do not fire notifications**. They are recorded in the `events` table with new event subtypes for the timeline UI:

```go
const (
    EventTypeIncidentMemberFailing   = "incident.member.failing"
    EventTypeIncidentMemberRecovered = "incident.member.recovered"
    EventTypeIncidentMemberRelapsed  = "incident.member.relapsed"
)
```

Use `payload.check_uid`, `payload.check_slug`, `payload.failure_count`, and `payload.member_count_failing` to drive the timeline.

---

## 4. API surface

### Incident response

`IncidentResponse` (in `incidents/service.go`) gains:

```go
type IncidentResponse struct {
    // ...existing fields...
    CheckGroupUID  *string                  `json:"checkGroupUid,omitempty"`
    CheckGroupSlug *string                  `json:"checkGroupSlug,omitempty"`
    Members        []IncidentMemberResponse `json:"members,omitempty"`
}

type IncidentMemberResponse struct {
    CheckUID         string     `json:"checkUid"`
    CheckSlug        *string    `json:"checkSlug,omitempty"`
    CheckName        *string    `json:"checkName,omitempty"`
    JoinedAt         time.Time  `json:"joinedAt"`
    FirstFailureAt   time.Time  `json:"firstFailureAt"`
    LastFailureAt    time.Time  `json:"lastFailureAt"`
    LastRecoveryAt   *time.Time `json:"lastRecoveryAt,omitempty"`
    FailureCount     int        `json:"failureCount"`
    CurrentlyFailing bool       `json:"currentlyFailing"`
}
```

`Members` is populated only when `CheckGroupUID != nil`. `CheckUID` on a group incident is the trigger check (the one that opened it); clients that today display a single check still render correctly, and `Members` is the new richer field.

### Endpoints

- `GET /api/v1/orgs/$org/incidents` — unchanged path. Adds the new fields above. Add a `checkGroupUid` query parameter for filtering.
- `GET /api/v1/orgs/$org/incidents/$uid` — unchanged path. Returns `Members` when applicable.
- `GET /api/v1/orgs/$org/incidents/$uid/events` — already returns timeline; now naturally includes `incident.member.*` events.
- `POST /api/v1/orgs/$org/incidents/$uid/acknowledge` — unchanged. One ack, group level.

No new endpoints. No breaking changes to per-check incidents.

### Listing semantics

The `ListIncidents` query is unchanged — the existing filter on check UIDs continues to return per-check incidents and any group incidents whose **trigger** check matches. To list incidents that *contain* a check, the frontend uses the new join: `WHERE check_uid = $1 OR uid IN (SELECT incident_uid FROM incident_member_checks WHERE check_uid = $1)`. Expose this via a new `?memberCheckUid=` filter to keep the existing `?checkUid=` semantic stable.

---

## 5. Frontend

`web/dash0`:
- Incident list row: when `checkGroupUid` is set, show the group name and `N/M` badge instead of the single check slug. Title fallback unchanged.
- Incident detail page: when `members` is non-empty, render a "Members" section with one row per member (slug, status badge, failure count, last failure / last recovery). Reuse the existing event timeline component to mix `incident.*` and `incident.member.*` events.
- Check detail page: in the incidents tab, show group incidents the check participated in (use the new `?memberCheckUid=` filter).

`web/status0` (public status page):
- A check belonging to an active group incident shows the same "down" pill as before, but the incident link points to the group incident page.

No new components and no new routes — all changes are extensions to existing pages.

---

## 6. CheckUpdate side effects

`checks.Service.UpdateCheck` already validates and applies `CheckGroupUID`. Add two side effects that mirror existing patterns:

1. **Group changed while check is failing**:
   - `oldGroup → newGroup`: if the check is currently a member of an active group incident on `oldGroup`, leave its row in place (the audit trail wins). If `newGroup` has an active group incident and `check.Status == down` with `streak >= IncidentThreshold`, attach as a new member.
   - `oldGroup → nil`: leave the row in place; the check returns to per-check behavior on the next probe.
   - `nil → newGroup`: if the check has its own active per-check incident, leave that incident open until it resolves naturally; the check joins `newGroup`'s next group incident (or starts one) on its next failure. We do **not** convert a per-check incident into a group incident retroactively — that would rewrite history. The next failure decides the right shape.

2. **Check disabled** while a member of a group incident: mark the member row `currently_failing=false`, `last_recovery_at=now`. If this leaves zero failing members, resolve the group incident.

Both cases are best handled by a small helper in `incidents.Service` (`OnCheckGroupChanged`, `OnCheckDisabled`) called from `checks.Service.UpdateCheck` after the DB write.

---

## 7. Configuration / defaults

No new system parameters. No new per-check fields. Group incidents are on by default for any check that has `check_group_uid` set; they're off (per-check behavior) for any check without a group. To opt out of group correlation for a single check that lives in a group, the existing path is to remove it from the group — that's intentional, the group is the unit of correlation.

---

## 8. Edge cases

| Case | Behavior |
|------|----------|
| Group with one enabled check fails | A group incident is opened with one member. Functionally identical to a per-check incident for notification purposes. Overhead is one row in `incident_member_checks`. Acceptable. |
| All members of a group are simultaneously in maintenance | No group incident opens; standard per-check maintenance gating applies. |
| Group has 100 checks, 80 fail simultaneously | One group incident, 80 member rows, one notification per connection. Title: `"<group> — 80/100 checks down"`. |
| A failing member's check is deleted | `ON DELETE CASCADE` on `incident_member_checks.check_uid` removes the row; if zero failing members remain, the incident resolves on the next probe of any other member (the trigger is the next success that lowers the count to zero) — to handle the "all members deleted" edge cleanly, `OnCheckDeleted` re-evaluates `CountFailingIncidentMembers` and resolves immediately if zero. |
| The trigger check is deleted while incident still active | `incidents.check_uid` references `checks(uid)`; today this is `ON DELETE CASCADE`, which would delete the incident. **Change required**: alter that FK to `ON DELETE SET NULL` for group incidents, or equivalently add an explicit migration that nulls `check_uid` before deletion. v1: switch the FK on `incidents.check_uid` to `ON DELETE SET NULL` so historical group incidents survive trigger-check deletion. |
| Group is deleted while incident is active | `incidents.check_group_uid` is `ON DELETE SET NULL` (see migration). The incident stays active and resolves normally; the title falls back to the trigger check's name once the group is gone. |
| Check moved into a group with an active group incident, check is currently UP | Nothing happens. The check joins the group on its next failure (if any). |
| Check moved out of a group with an active group incident, check is currently DOWN and a member | Member row stays. The group incident's resolution still requires this member to recover (per pre-decision). Operationally rare; the audit trail wins over surprise behavior. |
| Member relapses inside an incident | Member's `currently_failing` flips back to true, `failure_count++`, no group-level relapse, no notification. Recorded as `incident.member.relapsed` event. |
| Group incident resolves, then a member fails again within cooldown | Standard reopen path: group incident reopens with `relapse_count++`, that member is its first member in the new round. |

---

## 9. Tests

New file: `internal/handlers/incidents/service_group_test.go`.

Required cases:
- Group with two checks: one fails → group incident opens with one member; second fails → second member added; first recovers → still one failing member; second recovers → group incident resolves.
- Group escalation: single member crosses `escalation_threshold` → `escalated_at` set, `EventTypeIncidentEscalated` fired exactly once.
- Notification dedup: two members share a connection → exactly one notification job per event.
- Maintenance window on one of two failing members: only the other member opens the group incident.
- Reopen within cooldown: group incident closes, first member fails again → reopens with `relapse_count=1`.
- Member relapse inside the incident: no notification, `incident.member.relapsed` event.
- Trigger check deletion: incident `check_uid` becomes NULL, list/get endpoints still serve it.
- Group deletion: incident `check_group_uid` becomes NULL, incident still resolves.
- API: `GET /incidents/$uid` includes `members[]` when group, omits when per-check.
- API: `?memberCheckUid=X` filter returns group incidents that contain X.
- Migration: existing per-check incident rows survive the migration with `check_group_uid = NULL` and continue to behave as before.

Existing per-check incident tests must continue to pass without modification — that's the regression bar.

---

## 10. Manual verification

```bash
TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"org":"default","email":"admin@solidping.com","password":"solidpass"}' \
  'http://localhost:4000/api/v1/auth/login' | jq -r '.accessToken')

# 1. Create a group and two checks pointing at a guaranteed-bad target.
GROUP_UID=$(curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"infra-eu","slug":"infra-eu"}' \
  'http://localhost:4000/api/v1/orgs/default/check-groups' | jq -r '.uid')

for slug in api db; do
  curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
    -d "{\"slug\":\"$slug\",\"type\":\"http\",\"config\":{\"url\":\"http://127.0.0.1:1\"},\"period\":\"5s\",\"checkGroupUid\":\"$GROUP_UID\",\"incidentThreshold\":1}" \
    'http://localhost:4000/api/v1/orgs/default/checks' > /dev/null
done

# 2. Wait ~15s for the workers to run both checks and the group incident to open.
sleep 20

# 3. Verify a single group incident exists with two members.
curl -s -H "Authorization: Bearer $TOKEN" \
  "http://localhost:4000/api/v1/orgs/default/incidents?checkGroupUid=$GROUP_UID" \
  | jq '.data[] | {uid, title, members: (.members | length)}'

# 4. Flip one check to a healthy URL; verify the group incident remains active with one failing member.
curl -s -X PATCH -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"config":{"url":"https://example.com"}}' \
  'http://localhost:4000/api/v1/orgs/default/checks/api'

sleep 20
curl -s -H "Authorization: Bearer $TOKEN" \
  "http://localhost:4000/api/v1/orgs/default/incidents?checkGroupUid=$GROUP_UID" \
  | jq '.data[0] | {state, members: [.members[] | {checkSlug, currentlyFailing}]}'

# 5. Fix the second check; verify the group incident resolves.
curl -s -X PATCH -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"config":{"url":"https://example.com"}}' \
  'http://localhost:4000/api/v1/orgs/default/checks/db'

sleep 20
curl -s -H "Authorization: Bearer $TOKEN" \
  "http://localhost:4000/api/v1/orgs/default/incidents?checkGroupUid=$GROUP_UID" \
  | jq '.data[0] | {state, resolvedAt}'
```

---

## 11. Implementation order

1. Migrations + model + db.Service additions + unit tests on the queries.
2. `incidents.Service` failure path (create + reopen + member upsert), then success path (member recovery + group resolution), with table-driven tests on the state machine.
3. Notification dedup in `queueGroupNotifications`, with a test that covers the union behavior.
4. API response shape extension and filter additions; OpenAPI regen.
5. `checks.Service.UpdateCheck` side effects (`OnCheckGroupChanged`, `OnCheckDisabled`, `OnCheckDeleted`).
6. Frontend rendering pass on incident list + detail.
7. End-to-end manual run from §10. Smoke test against `dev-test` mode.

Stop conditions during build:
- Any per-check incident regression test fails → fix before continuing.
- Notification dedup test produces duplicates → block.
- Migration is not idempotent (re-running the down + up cycle leaves diff) → block.

**Status**: Todo | **Created**: 2026-04-29

## Implementation Plan

Following the order in §11. Each numbered step is a commit:

1. **Migrations** (postgres + sqlite) `004_group_incidents.{up,down}.sql`: `incidents.check_group_uid` column with FK `ON DELETE SET NULL`; new `incident_member_checks` table; partial indexes.
2. **Models**: add `Incident.CheckGroupUID *string`, new `IncidentMemberCheck` + `IncidentMemberUpdate` in `internal/db/models/`.
3. **db.Service additions**: `FindActiveIncidentByGroupUID`, `FindRecentlyResolvedIncidentByGroupUID`, `ListIncidentMemberChecks`, `GetIncidentMemberCheck`, `UpsertIncidentMemberCheck`, `UpdateIncidentMemberCheck`, `CountFailingIncidentMembers`. Implement on both postgres and sqlite. Update `FindActiveIncidentByCheckUID` to also return group incidents containing the check.
4. **State machine — failure path** in `incidents.Service`: `createOrReopenGroupIncident`, `handleGroupFailure`. Routing in `ProcessCheckResult` based on `check.CheckGroupUID`.
5. **State machine — success path**: `handleGroupSuccess`, group resolution when `CountFailingIncidentMembers == 0`.
6. **Notification dedup**: `queueGroupNotifications` building the union of failing members' connections; `emitEvent` chooses based on `incident.CheckGroupUID`.
7. **API**: `IncidentResponse` gains `CheckGroupUID`, `CheckGroupSlug`, `Members[]`. Add `?checkGroupUid=` and `?memberCheckUid=` query filters. Wire member loading into `Get`/`List` handlers.
8. **CheckUpdate side effects**: `OnCheckGroupChanged`, `OnCheckDisabled`, `OnCheckDeleted` helpers in `incidents.Service`, called from `checks.Service.UpdateCheck` / `DeleteCheck`.
9. **Frontend**: incident list shows `<group> — N/M`; detail page renders members section + member-level events; check detail incidents tab uses `?memberCheckUid=`.
10. **Tests**: new `service_group_test.go` covering the table from §9. Per-check incident tests must keep passing.
11. **QA + archive**.

Notes on safety:
- All changes are additive — new column nullable, new table separate. Existing per-check incidents (where `CheckGroupUID IS NULL`) must keep behaving identically. Regression-test bar.
- `FindActiveIncidentByCheckUID` semantics widen: it now also returns the group incident a check participates in. Callers see the returned `incident.CheckGroupUID` and route accordingly. Existing per-check tests pass because non-grouped checks still return the per-check incident.

