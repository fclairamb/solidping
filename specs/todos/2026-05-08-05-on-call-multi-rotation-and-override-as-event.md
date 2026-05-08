# On-call schedules: multiple rotations per schedule + overrides as events

## Context

SolidPing's on-call model today is **one rotation per schedule**:

- `OnCallSchedule` has a single `rotationType` (daily / weekly), a
  single `handoffTime`, a single `handoffWeekday` (for weekly), and
  a single ordered `users[]` array. ([`on_call_schedule.go:23`](../../server/internal/db/models/on_call_schedule.go))
- Time-bounded substitutions live in a separate `OnCallScheduleOverride`
  resource with its own table, CRUD endpoints, and dashboard editor.

Both competitors take a different shape:

- **BetterStack** ([`betterstack/alerting.md §On-call calendars`](../../docs/competitors/betterstack/alerting.md#on-call-calendars)):
  one calendar contains N `on_call_rotation` blocks. Each rotation has
  its own `users[]`, `rotation_length`, `rotation_interval`, start /
  end timestamps. PTO overrides are just events on the calendar with
  `override: true` — no separate class.
- **Hyperping** ([`hyperping/alerting.md §On-call schedules`](../../docs/competitors/hyperping/alerting.md#on-call-schedules)):
  schedule contains N rotations, each with its own timezone, users,
  handoff time, and optional time-restrictions.

The N-rotations-per-schedule shape is what enables **follow-the-sun**
without forcing customers to make three separate schedules and string
them together with escalation policy time-branching. The
override-as-event pattern saves one resource class and one API surface.

## Goal

Two coordinated changes:

1. **N rotations per schedule.** A schedule contains zero or more
   rotation blocks; each rotation has its own users, type
   (daily/weekly/custom-interval), timezone, handoff time, and start /
   end window. The schedule itself is the named container; rotations
   are the rotation logic.
2. **Override-as-event.** Drop the standalone `OnCallScheduleOverride`
   resource. Replace with a single `OnCallScheduleEvent` table whose
   rows can be either *regular events* (computed from the rotation,
   read-only) or *override events* (manual, `isOverride = true`,
   takes precedence within its `[startsAt, endsAt]` window).

## Approach

### Data model

Replace `OnCallSchedule.rotation*` scalar fields and
`OnCallSchedule.users[]` with a child table:

```sql
CREATE TABLE on_call_rotations (
    uid VARCHAR(36) PRIMARY KEY,
    schedule_uid VARCHAR(36) NOT NULL REFERENCES on_call_schedules(uid),
    name TEXT NOT NULL,
    timezone TEXT NOT NULL,
    rotation_type TEXT NOT NULL,         -- daily | weekly | hourly_interval | weekly_interval
    rotation_interval_hours INTEGER,     -- non-null for hourly/weekly_interval rotations
    handoff_time TEXT NOT NULL,          -- HH:MM in the rotation's timezone
    handoff_weekday INTEGER,             -- 0-6 for weekly; null for daily/interval
    start_at TIMESTAMP NOT NULL,         -- the rotation's epoch
    end_at TIMESTAMP NULL,               -- optional sunset
    position INTEGER NOT NULL,           -- ordering within the schedule
    created_at, updated_at, deleted_at
);

CREATE TABLE on_call_rotation_users (
    uid VARCHAR(36) PRIMARY KEY,
    rotation_uid VARCHAR(36) NOT NULL,
    user_uid VARCHAR(36) NOT NULL,
    position INTEGER NOT NULL,
    UNIQUE (rotation_uid, position)
);
```

The `OnCallSchedule` table loses the rotation-detail columns; it keeps
`uid`, `slug`, `name`, `description`, `organization_uid`, and
timestamps. The schedule is now just a named container.

Replace the override table with an events table:

```sql
CREATE TABLE on_call_schedule_events (
    uid VARCHAR(36) PRIMARY KEY,
    schedule_uid VARCHAR(36) NOT NULL,
    user_uid VARCHAR(36) NOT NULL,
    starts_at TIMESTAMP NOT NULL,
    ends_at TIMESTAMP NOT NULL,
    is_override BOOLEAN NOT NULL DEFAULT false,
    reason TEXT NULL,
    created_at, updated_at, deleted_at
);
```

When `isOverride = false`, the row is a *materialized* event derived
from the rotation logic. We don't insert these eagerly; the resolver
generates them on demand from the rotation rules. Override events
(`isOverride = true`) are real rows.

The "what override events apply now" question becomes: query rows
where `schedule_uid = ?` and `starts_at <= now < ends_at` and
`is_override = true`. The "who is on call now" resolver merges
override events on top of the computed rotation schedule.

### Migration from current model

Each existing `OnCallSchedule` row becomes a schedule plus exactly
one rotation row:

```sql
INSERT INTO on_call_rotations (uid, schedule_uid, name, timezone,
                                rotation_type, handoff_time, handoff_weekday,
                                start_at, position, created_at, updated_at)
SELECT
    /* generated uid */, uid AS schedule_uid, 'Default rotation',
    timezone, rotation_type, handoff_time, handoff_weekday,
    start_at, 0, created_at, updated_at
FROM on_call_schedules;
```

Existing override rows migrate to the new events table with
`is_override = true`:

```sql
INSERT INTO on_call_schedule_events (..., is_override)
SELECT ..., true FROM on_call_schedule_overrides;

DROP TABLE on_call_schedule_overrides;
```

After migration, every customer's view of "who is on call" is
unchanged because their single rotation is preserved. They can now
add additional rotations through the new UI.

### Resolution at fire time

The "who is on call right now" resolver now walks all rotations in
the schedule and picks the active one based on the current timestamp
falling within `[start_at, end_at)`. For overlapping rotations
(e.g. follow-the-sun where multiple geos are technically active at
the same UTC moment), pick the rotation whose `position` is lowest —
deterministic, simple. Operators model "primary EU + secondary US"
by giving EU position 0 and US position 1.

Override events are checked last: if any override row covers
`now`, its `user_uid` wins over whatever the rotation produced.

### Wire shape

```json
{
  "uid": "...",
  "slug": "platform",
  "name": "Platform team",
  "description": "...",
  "rotations": [
    {
      "uid": "...",
      "name": "EU primary",
      "timezone": "Europe/Paris",
      "rotationType": "weekly",
      "handoffTime": "09:00",
      "handoffWeekday": 1,
      "startAt": "2026-01-06T08:00:00Z",
      "users": ["alice-uid", "bob-uid"]
    },
    {
      "uid": "...",
      "name": "US primary",
      "timezone": "America/Los_Angeles",
      "rotationType": "weekly",
      "handoffTime": "09:00",
      "handoffWeekday": 1,
      "startAt": "2026-01-06T17:00:00Z",
      "users": ["carol-uid", "dave-uid"]
    }
  ],
  "events": [
    {
      "uid": "...",
      "userUid": "carol-uid",
      "startsAt": "2026-05-15T00:00:00Z",
      "endsAt": "2026-05-22T00:00:00Z",
      "isOverride": true,
      "reason": "PTO coverage"
    }
  ]
}
```

The single-rotation customer continues to see one rotation in
`rotations[]`, with `events: []` until they create one.

### API surface

- `GET /api/v1/orgs/:org/on-call-schedules` — list schedules.
- `GET /api/v1/orgs/:org/on-call-schedules/:slug` — read with full
  rotations and events.
- `POST /api/v1/orgs/:org/on-call-schedules` — create with optional
  initial rotation array.
- `PATCH /api/v1/orgs/:org/on-call-schedules/:slug` — top-level
  schedule fields (name, description).
- `POST /api/v1/orgs/:org/on-call-schedules/:slug/rotations` —
  add a rotation.
- `PATCH /api/v1/orgs/:org/on-call-schedules/:slug/rotations/:uid` —
  edit one rotation.
- `DELETE /api/v1/orgs/:org/on-call-schedules/:slug/rotations/:uid` —
  remove one rotation.
- `POST /api/v1/orgs/:org/on-call-schedules/:slug/events` — add an
  override event (clients may only post `isOverride = true`; regular
  events are derived).
- `DELETE /api/v1/orgs/:org/on-call-schedules/:slug/events/:uid` —
  remove an override.
- `GET /api/v1/orgs/:org/on-call-schedules/:slug/preview` — the
  next-N-days preview, which now merges all rotations + overrides at
  once.

The legacy `POST /api/v1/orgs/:org/on-call-schedules/:slug/overrides`
path is kept as an alias for one release; it forwards to the events
endpoint with `isOverride = true`.

## Files affected

### Backend

- `server/internal/db/migrations/NNN_oncall_multi_rotation.{up,down}.sql`
  — schema rewrite, data migration, drop `on_call_schedule_overrides`.
- `server/internal/db/models/on_call_schedule.go` — strip rotation
  fields off the schedule; add `OnCallRotation` and
  `OnCallRotationUser` types; rename `OnCallScheduleOverride` →
  `OnCallScheduleEvent` and gain `IsOverride` field.
- `server/internal/handlers/oncallschedules/service.go` — add
  rotation CRUD methods, rewrite preview/resolver to walk rotations,
  add event CRUD.
- `server/internal/handlers/oncallschedules/handler.go` — new route
  registrations.
- `server/internal/db/{sqlite,postgres}/on_call*.go` — plumbing for
  the new tables.
- `server/internal/jobs/jobtypes/job_escalation_step.go` — the
  schedule-target resolver consults the new resolver instead of the
  schedule's old fields.

### Frontend

- `web/dash0/src/api/hooks.ts` — extend `OnCallSchedule` type with
  `rotations[]` and `events[]`. Drop the standalone
  `useOnCallScheduleOverrides` hook (or alias it to event-as-override
  mutations).
- `web/dash0/src/components/oncall/on-call-schedule-form.tsx` (the
  user's spec-04 WIP) — extend to manage N rotations as a tabbed or
  list form. Each rotation gets its own sub-form with the existing
  rotation-type / handoff / users fields.
- New `web/dash0/src/routes/orgs/$org/on-call.$slug.events.tsx` (or a
  section on the detail page) for managing override events directly.
- The existing override editor route gets folded into events.
- `web/dash0/src/locales/{en,fr,de,es}/oncall.json` — new keys for
  rotation labels (rotation name, "Add rotation", "Sunset"), drop
  override-specific keys, add event-as-override labels.

### MCP

- `server/internal/mcp/tools_oncall.go` (if it exists; otherwise
  add) — `list_on_call_schedules`, `add_rotation`, `add_override_event`
  tools. The deprecated overrides-tool aliases the new event-tool
  for one release.

### Docs

- `docs/api-specification.md` — replace the on-call section with the
  new shape; document the legacy alias.
- `docs/features/notifications-and-escalation.md` — update the
  "On-call schedules" section to describe N rotations and the
  override-as-event semantics.

## Out of scope

- **Rotation time-restrictions** ("only weekdays 9-17"). BetterStack
  intentionally doesn't ship this — they say "use multiple
  rotations + escalation policy time-branching". We follow that
  decision; time-restrictions become a future spec only if customers
  ask.
- **Per-user holiday-mode flag.** BetterStack has it; useful but
  shape-orthogonal to this spec. Separate.
- **Drag-drop calendar UI.** Worth doing but the form-based editor in
  this spec is the v1 surface. Drag-drop is a follow-up frontend spec.
- **`concurrent_shifts`** (N people on at once) from Hyperping. Not
  shipping in v1; the rotation always picks one user. Multi-shift
  needs duplicate-paging deduplication that's its own design problem.
- **Localized rotation names.** English only.
- **Auto-suggesting overlap warnings** when the user creates two
  rotations with overlapping windows. Future polish.

## Verification

1. `make build-backend lint-back test` clean. Existing tests for
   single-rotation behavior still pass — the resolver returns the
   same answer for a schedule with one rotation that it returned
   before the migration.
2. New test: schedule with two rotations (EU + US), query "who is
   on call at 14:00 UTC on Tuesday" → returns the EU on-call.
   Query at 22:00 UTC → returns the US on-call.
3. Overlapping-rotation tiebreak: two rotations both active at the
   same instant → the lower-position one wins.
4. Override event: post an override covering "all of next week";
   query during that window → returns the override user, not the
   rotation user. Outside the window: rotation user.
5. Migration smoke: a fixture DB with three single-rotation
   schedules and one override per. After migration, `rotations[]`
   has three rows (one per schedule), `events[]` has three rows
   (one per old override, all `isOverride = true`).
6. Legacy alias: `POST .../overrides` still works for one release;
   audit-log entry shows the alias was used.
7. Manual: build a follow-the-sun schedule via the dashboard with
   three rotations (EU/US/AS), verify the dashboard preview shows
   handoffs at the right local times.

## Implementation Plan

1. Migration scripts (sqlite + postgres) + manual smoke load.
2. New model types (`OnCallRotation`, `OnCallScheduleEvent`).
3. Service-layer rewrite: rotation CRUD, override-event CRUD,
   resolver that walks rotations + events.
4. HTTP handler: new route registrations + legacy alias for
   `/overrides`.
5. Job runtime: escalation step resolver consults new
   resolver.
6. Backend tests: unit + integration.
7. Frontend: extend schedule form to manage N rotations, drop the
   standalone override editor in favor of the events surface.
8. MCP tool updates.
9. Docs.
10. Completeness audit, archive, merge.

This spec depends on the user's spec-04 WIP (oncall edit page)
landing first — the new multi-rotation form replaces single-rotation
form, but the editor *route* added in spec 04 is the foundation.
Sequencing: spec 04 ships → this spec rebases on the editor route →
extends the form.
