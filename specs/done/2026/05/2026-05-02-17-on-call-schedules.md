# On-call schedules

## Context

SolidPing today notifies a fixed set of channels per check (via
`check_connections`) when an incident opens, ongoing, or resolves. There is no
notion of "the person on call right now". For team rotations, operators have
to maintain rotations in Google Sheets and copy-paste channel webhooks each
week — not a serious option for production use.

This spec adds first-class on-call schedules. They are *resolvers*: a schedule
plus a point in time resolves to one user (the "currently on call"). They are
*not* notification channels themselves — the way schedules drive paging is via
escalation policies (see `2026-05-02-19-escalation-policies.md`), which can target
a schedule and at evaluation time resolve it to the current on-call user.

This separation matters: schedules are about *who*, escalation policies are
about *when and through which medium*. Conflating them — as some monitoring
tools do — produces a confusing UI where the same concept appears in two
places.

## Scope

In scope:
- Single-layer rotations (the 80% case): one rotation, list of users, fixed
  rotation period (daily / weekly), explicit handoff time, explicit timezone.
- Time-bounded user overrides ("Alice is on PTO 5/4–5/8, Bob covers").
- Resolver API: "who is on call for this schedule at time T".
- A "current and next" preview API for the dashboard.
- Visual schedule view: who's on now, who's on next, calendar strip showing
  the next 14 days.
- Per-user "my on-call" dashboard widget: when am I next on call?
- Public iCal feed per schedule (single secret URL, opt-in) so individuals can
  subscribe in Google Calendar / Apple Calendar.

Out of scope (deliberate v1 cuts; revisit once the simple model is in
production):
- Multi-layer schedules (primary, secondary, tertiary). One layer covers most
  small-team cases; layers belong in escalation policies for v1.
- Time-of-day restrictions ("only weekdays 9–17"). Common requirement but
  meaningfully more complex (interaction with timezones, partial-day rotations,
  gap handling). Defer.
- Custom rotation periods other than daily/weekly. "Every 4 days" is rare;
  "every 2 weeks" can be done by listing the same user twice in a weekly
  rotation as a workaround until v2.
- Auto-skip-on-PTO (calendar integration). Manual overrides cover this.

## Data model

New tables. Match the conventions in `server/internal/db/models/` (UID PK,
`organization_uid`, `created_at`, `updated_at`, `deleted_at`).

### `on_call_schedules`

| Column | Type | Notes |
|---|---|---|
| `uid` | varchar(36) PK | UUID |
| `organization_uid` | FK | |
| `slug` | text | URL-friendly id, unique per org |
| `name` | text | Display name |
| `description` | text nullable | |
| `timezone` | text | IANA zone, e.g. `Europe/Paris` |
| `rotation_type` | enum | `daily` \| `weekly` |
| `handoff_time` | text | `HH:MM` in the schedule's timezone (e.g. `09:00`) |
| `handoff_weekday` | int nullable | 0–6 (Mon=0). Required for `weekly`, ignored for `daily`. |
| `start_at` | timestamp | When the rotation began (epoch for the rotation cycle) |
| `ical_secret` | text nullable | Random secret enabling unauthenticated iCal access. NULL = feed disabled. |
| `created_at` / `updated_at` / `deleted_at` | | |

`(organization_uid, slug)` unique.

### `on_call_schedule_users`

The ordered list of users participating in the rotation.

| Column | Type | Notes |
|---|---|---|
| `uid` | PK | |
| `schedule_uid` | FK | |
| `user_uid` | FK | |
| `position` | int | Rotation order, 0-based |
| `created_at` / `updated_at` | | |

`(schedule_uid, position)` unique. `(schedule_uid, user_uid)` unique (a user
appears at most once per schedule; for "every other week", make a different
schedule).

### `on_call_schedule_overrides`

Time-bounded replacements.

| Column | Type | Notes |
|---|---|---|
| `uid` | PK | |
| `schedule_uid` | FK | |
| `user_uid` | FK | The replacement (who is on call during the override) |
| `start_at` | timestamp | Inclusive |
| `end_at` | timestamp | Exclusive |
| `reason` | text nullable | |
| `created_by_uid` | FK to user | |
| `created_at` | | |

Index `(schedule_uid, start_at, end_at)` for resolver lookups.

## Resolver

New service `server/internal/handlers/oncallschedules/service.go`. Core
function:

```go
// Resolve returns the user on call for the given schedule at time t.
func (s *Service) Resolve(ctx context.Context, scheduleUID string, t time.Time) (*models.User, error)
```

Algorithm:

1. Load schedule and its overrides.
2. If any override covers `t` (`start_at <= t < end_at`), return that user.
   If multiple overrides overlap (operator error, but possible), pick the
   *most recently created*. Document this; do not throw.
3. Otherwise compute the rotation index:
   - Convert `t` to the schedule's timezone.
   - Find the rotation period boundary on or before `t` whose handoff time matches:
     - Daily: most recent occurrence of `handoff_time` <= `t`.
     - Weekly: most recent occurrence of `handoff_weekday` at `handoff_time` <= `t`.
   - `period_index = floor(durations between start_at and that boundary, in rotation periods)`.
   - `user_position = period_index % len(users)`.
4. Return the user at that position.

Edge cases worth getting right:
- DST transitions: do the math in the IANA zone, not UTC, so a "9am Monday"
  handoff stays at 9am local across DST. Use Go's `time.LoadLocation` and
  arithmetic on `time.Time` in that zone.
- Empty user list: return `ErrScheduleHasNoUsers`. The service must reject
  resolution attempts gracefully — `Resolve` is called from notification paths
  where panics would be very visible.
- Schedule before `start_at`: return `ErrScheduleNotYetActive`.
- A user in the rotation has been deleted (soft-deleted): skip them and use
  the next position. Surface this in the schedule UI as a warning so admins
  fix it. Do *not* fall through to "no one is on call" — the rotation still
  has slots filled.

### Test

`service_test.go` table-driven tests with concrete scenarios:
- Weekly rotation, 3 users, Monday 09:00 Europe/Paris handoff. Compute who's on
  for: a Sunday at 23:59, the Monday 08:59, the Monday 09:01, six months out.
- Daily rotation, 2 users.
- DST transition: rotate Sunday 09:00 across the spring-forward weekend.
  Handoff on Sunday 09:00 local — must not skip or duplicate.
- Override covers a rotation handoff.
- Two overlapping overrides → most recent wins.
- Empty user list → error.

These are the kind of tests where bugs hide. Don't skimp.

## API

`/api/v1/orgs/$org/on-call-schedules`

- `GET ` — list schedules with `currentlyOnCall: {userUid, name, email}` field
  on each, computed via the resolver.
- `POST ` — create. Body:
  ```json
  {
    "slug": "platform-eu",
    "name": "Platform EU",
    "timezone": "Europe/Paris",
    "rotationType": "weekly",
    "handoffTime": "09:00",
    "handoffWeekday": 0,
    "startAt": "2026-05-04T09:00:00+02:00",
    "userUids": ["<uid1>", "<uid2>", "<uid3>"]
  }
  ```
- `GET /$slug` — full schedule with `users[]`, `overrides[]`, `currentlyOnCall`,
  and `nextHandoff: {at, fromUserUid, toUserUid}`.
- `PATCH /$slug` — partial update of schedule fields and/or `userUids`.
  Replacing `userUids` rewrites positions; the resolver continues to work
  correctly across the change (no migration needed — `start_at` is fixed).
- `DELETE /$slug` — soft-delete.
- `GET /$slug/preview?from=...&days=14` — returns the upcoming on-call ranges:
  ```json
  {
    "data": [
      {"userUid": "u1", "from": "2026-05-04T09:00:00+02:00", "to": "2026-05-11T09:00:00+02:00"},
      {"userUid": "u2", "from": "2026-05-11T09:00:00+02:00", "to": "2026-05-18T09:00:00+02:00"},
      ...
    ]
  }
  ```
  This is what the dashboard renders as a calendar strip, and what's used to
  power the iCal feed.

### Overrides

`/api/v1/orgs/$org/on-call-schedules/$slug/overrides`

- `GET ` — list (with optional `?from=&until=`).
- `POST ` — create. Body: `{userUid, startAt, endAt, reason?}`.
- `DELETE /$uid` — remove.

(No PATCH — just delete and re-create. Overrides are short-lived; mutation is
rare.)

### iCal feed

`GET /api/v1/on-call-schedules/$secret/feed.ics`

Note the path: it's *not* under `/orgs/$org/...` because there's no auth. The
secret embeds enough information (or is looked up against `ical_secret`) to
identify the schedule. Response is `text/calendar`. Each calendar event covers
one rotation slot for the next 12 months and the previous 1 month. Generate on
demand; cache at the HTTP layer with `Cache-Control: max-age=900` (15 min) —
slots for the next year don't change minute-to-minute.

To enable: `POST /$slug/ical-feed/enable` (returns the URL with the new secret).
To disable: `POST /$slug/ical-feed/disable` (clears the secret).
To rotate: `POST /$slug/ical-feed/rotate`.

## Frontend (dash0)

### Routes

- `/orgs/$org/on-call` — list of schedules. Each card shows: name, currently on
  call (avatar + name), next handoff in N hours, link to detail.
- `/orgs/$org/on-call/$slug` — schedule detail.
  - Header: name, timezone, rotation type, edit/delete.
  - Currently-on-call panel.
  - 14-day calendar strip (use the `/preview` endpoint). Each segment is a
    user's slot. Color-code by user. Overrides shown as patches with a
    distinct pattern.
  - Users-in-rotation list (drag to reorder → sets `position`).
  - Overrides table.
  - iCal feed section (disabled by default; "Enable" reveals the URL).
- `/orgs/$org/on-call/new` — wizard form. Pre-fill timezone from the
  organization's default if one is set, else from the browser.

### "My on-call" widget

On the user's dashboard home (or as a profile-page card if no home page exists
yet — verify), a list of schedules the user is part of, with:

- "On call now" if currently active.
- "Next: <duration> from now (<weekday> <time> <tz>)" otherwise.

This is what makes schedules feel real to the people on them.

### i18n

`oncall` namespace for all strings. Match dashboard convention.

## Resolver-from-elsewhere

The escalation-policies spec needs to call `Resolve(scheduleUID, time.Now())`
at fan-out time. Expose a thin internal interface from the on-call package so
the escalation service depends on the abstraction, not the model:

```go
// In server/internal/handlers/oncallschedules/resolver.go
type Resolver interface {
    Resolve(ctx context.Context, scheduleUID string, t time.Time) (*models.User, error)
}
```

Bind in the service registry. This makes the on-call package independently
testable and lets the escalation service mock it.

## Verification

1. Migration applied; tables present.
2. Create a 3-user weekly schedule with Monday 09:00 Europe/Paris handoff and
   `start_at = next Monday 09:00`. Confirm `currentlyOnCall` is `null` until
   that point, then user 0, then user 1 a week later, etc.
3. Add an override: `userUid = user2`, covering tomorrow. Confirm
   `currentlyOnCall` switches at the override's `start_at` and reverts at
   `end_at`.
4. Rotate users via PATCH. Confirm `position`s update and resolver continues
   correctly.
5. Soft-delete a user in the rotation. Resolver skips them, UI shows a
   warning on the schedule.
6. Enable iCal feed. Subscribe in Google Calendar via the URL. Verify the next
   12 weeks of slots appear with the right user names. Disable feed —
   subscribed calendar shows 410 / empty.
7. Frontend: drag-to-reorder. Calendar strip updates without flicker.

Tests:
- All resolver edge cases listed above.
- iCal generator: timezone correctness, count of generated events, feed
  validates against `vcalendar` parsers (use a Go iCal lib that round-trips
  cleanly — search the existing deps before adding one).
- API: CRUD on schedules, overrides, list filtering.

## Risks / unknowns to flag before coding

- **Timezone library**: Go's stdlib handles IANA zones fine, but DST-correct
  arithmetic ("add one week, keeping local time at 09:00") is not idiomatic.
  Write `nextHandoff(t time.Time, schedule)` carefully and unit-test it
  against DST transitions in two zones with opposite hemisphere DST
  (Europe/Paris and Pacific/Auckland) before trusting it.
- **iCal correctness**: `text/calendar` is strict. Bad CRLF or unfolded long
  lines silently break in Google Calendar but pass naive validation. Use a
  proven lib; don't hand-roll.
- **Naming collision with the existing `EscalatedAt` field on incidents**:
  the on-call work introduces "escalation policy steps" elsewhere. Pick
  vocabulary in this spec that *doesn't* overlap — call them "rotation
  slots" not "rotation steps", "handoff" not "transition" — to keep the
  glossary clean.
- **No multi-layer in v1**: spell this out in the create form's help text so
  users don't go looking for it. "Need primary + secondary on-call? Use
  escalation policies — schedules represent one rotation."
- **Default timezone**: Organizations don't currently have a `timezone`
  field. Either add one in this spec or default to the user's browser
  zone in the UI and require explicit selection. Prefer the latter for v1
  to avoid scope creep — note the org-level setting as future work.
