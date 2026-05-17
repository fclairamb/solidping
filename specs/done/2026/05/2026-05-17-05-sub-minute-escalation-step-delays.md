# Sub-minute escalation step delays

## Context

Escalation policy steps store their delay as `DelayMinutes int`
([server/internal/db/models/escalation_policy.go:72](server/internal/db/models/escalation_policy.go)).
The scheduler converts this directly to a duration in the escalation-step job:
`step.DelayMinutes * time.Minute`
([server/internal/jobs/jobtypes/job_escalation_step.go:506](server/internal/jobs/jobtypes/job_escalation_step.go)).

This means:
- The minimum representable delay between steps is 0 (fire immediately) or 60 seconds.
- There is no way to express "fire step 1 thirty seconds after step 0."
- Repeat-after intervals (`RepeatAfterMinutes`) have the same constraint.

For production use, minute-granularity is perfectly fine — escalation ladder steps are
typically measured in minutes or hours. But it makes automated end-to-end tests
([2026-05-17-04-fast-loop-e2e-integration-tests.md](2026-05-17-04-fast-loop-e2e-integration-tests.md))
unable to verify multi-step or repeat-cycle behaviour in a few seconds: the only fast
value is 0 (immediate), so all steps collapse to the same instant and you cannot assert
ordering or timing between them.

This spec changes the unit of delay from minutes to seconds across the model, API, engine,
and dashboard. It is a **breaking API change** (`delayMinutes` → `delaySeconds`,
`repeatAfterMinutes` → `repeatAfterSeconds`). This is acceptable because no production
data exists yet and the schema is under active development (confirmed at planning time,
2026-05-17).

## Goal

- Escalation policy step delays are stored and accepted in seconds.
- The minimum meaningful delay becomes 1 second (0 = fire immediately, unchanged).
- Repeat-after is also expressed in seconds.
- The dashboard input remains user-friendly (shows minutes/seconds as needed, accepts
  seconds under the hood).
- Existing rows (all dev/test data) are migrated by multiplying their `delay_minutes`
  value by 60.

### Honest opinion (recorded at planning time)

An alternative is to keep `delay_minutes` and add a separate `delay_seconds` override
column. This preserves API backwards-compatibility but adds permanent complexity: two
columns with precedence rules, two form fields, migration complexity. Given there is no
production data to protect, a clean rename is strictly better. If we ever need
backward-compat for external API consumers, that is the moment to introduce an API version
layer — not now.

Another alternative: add a `delay_ms` column for maximum precision. Millisecond granularity
has no practical use in escalation ladders and makes the UI harder to reason about. Seconds
is the right unit.

## Non-goals

- Changing the representation of check `Period` (that is already in `time.Duration` / seconds).
- UI widgets that let users enter hours/minutes/seconds — a plain seconds input or a
  simple "minutes" label updated to reflect seconds is sufficient. Nice formatting (`2m 30s`) can come later.
- Replay/backfill of historical escalation job timing — no production incidents exist.

## Design

### Schema migration

Next migration index is **024** (023 is claimed by
[2026-05-17-02-incident-notifications-audit-table.md](2026-05-17-02-incident-notifications-audit-table.md)).

**`server/internal/db/postgres/migrations/024_escalation_step_delay_seconds.up.sql`**

```sql
-- escalation_policy_steps
ALTER TABLE escalation_policy_steps
    ADD COLUMN delay_seconds integer NOT NULL DEFAULT 0;

UPDATE escalation_policy_steps
    SET delay_seconds = delay_minutes * 60;

ALTER TABLE escalation_policy_steps
    DROP COLUMN delay_minutes;

-- escalation_policies (repeat_after_minutes)
ALTER TABLE escalation_policies
    ADD COLUMN repeat_after_seconds integer;

UPDATE escalation_policies
    SET repeat_after_seconds = repeat_after_minutes * 60
    WHERE repeat_after_minutes IS NOT NULL;

ALTER TABLE escalation_policies
    DROP COLUMN repeat_after_minutes;
```

**`server/internal/db/postgres/migrations/024_escalation_step_delay_seconds.down.sql`**

```sql
ALTER TABLE escalation_policy_steps
    ADD COLUMN delay_minutes integer NOT NULL DEFAULT 0;

UPDATE escalation_policy_steps
    SET delay_minutes = delay_seconds / 60;

ALTER TABLE escalation_policy_steps
    DROP COLUMN delay_seconds;

ALTER TABLE escalation_policies
    ADD COLUMN repeat_after_minutes integer;

UPDATE escalation_policies
    SET repeat_after_minutes = repeat_after_seconds / 60
    WHERE repeat_after_seconds IS NOT NULL;

ALTER TABLE escalation_policies
    DROP COLUMN repeat_after_seconds;
```

SQLite equivalents follow the same logic (SQLite does not support `DROP COLUMN` in older
versions; use `CREATE TABLE ... AS SELECT` with column rename pattern already used by the
project's existing SQLite migration history).

### Model

`server/internal/db/models/escalation_policy.go`

```go
// Before
DelayMinutes int `bun:"delay_minutes,notnull"`

// After
DelaySeconds int `bun:"delay_seconds,notnull"`
```

```go
// Before (on EscalationPolicy)
RepeatAfterMinutes *int `bun:"repeat_after_minutes"`

// After
RepeatAfterSeconds *int `bun:"repeat_after_seconds"`
```

Also update `NewEscalationPolicyStep(policyUID string, position, delaySeconds int)` and
`NewEscalationPolicy` constructors.

### Validation

`server/internal/handlers/escalationpolicies/service.go` (currently at L27, L361-362)

```go
// Before
if step.DelayMinutes < 0 {
    return ErrDelayNegative
}

// After
const maxStepDelaySeconds = 86400 // 24h
if step.DelaySeconds < 0 {
    return ErrDelayNegative
}
if step.DelaySeconds > maxStepDelaySeconds {
    return ErrDelayTooLarge
}
```

Same update for `RepeatAfterSeconds` validation (current `RepeatAfterMinutes`).

### Escalation job engine

`server/internal/jobs/jobtypes/job_escalation_step.go` (currently L506)

```go
// Before
startAt.Add(time.Duration(step.DelayMinutes) * time.Minute)

// After
startAt.Add(time.Duration(step.DelaySeconds) * time.Second)
```

Same for `RepeatAfterSeconds` → `time.Second` in `scheduleNextCycle`.

### REST API

The JSON field names change in the handler input/output.

`server/internal/handlers/escalationpolicies/handler.go` (request/response structs):

```go
// Before
DelayMinutes int `json:"delayMinutes"`

// After
DelaySeconds int `json:"delaySeconds"`
```

```go
// Before
RepeatAfterMinutes *int `json:"repeatAfterMinutes,omitempty"`

// After
RepeatAfterSeconds *int `json:"repeatAfterSeconds,omitempty"`
```

Update `docs/api-specification.md` field names and regenerate the TypeScript/Go client
with `make generate`.

### Dashboard

`web/dash0/src/components/oncall/on-call-schedule-form.tsx` is not directly affected (that
is the on-call schedule, not escalation policy). The affected component is:

`web/dash0/src/routes/orgs/$org/escalation-policies.$slug.tsx` — the step delay input.

Update the field binding from `delayMinutes` to `delaySeconds`. The label can stay as
"Delay" for now; consider displaying a human-readable helper (`"120s = 2 minutes"`). If
the existing input is a plain number field, no structural change is needed — just update
the field name and consider setting the step value in seconds when pre-populating the form.

Also update the generated TypeScript API client types (via `make generate`).

### Existing integration test fixtures

`server/test/integration/escalation_policies_test.go` — the helper
`createEscalationPolicyViaAPI` currently sends `delayMinutes: 0`; change to
`delaySeconds: 0`. The `patchPolicySteps` helper likewise.

### Hardcoded sample data

`server/test/testdata.go` — if it seeds any escalation policy steps with `DelayMinutes`,
update to `DelaySeconds`.

`server/internal/app/server.go:1659` — `InitializeTestData` path — check for any
hardcoded step creation.

## Files to change

### New files

- `server/internal/db/postgres/migrations/024_escalation_step_delay_seconds.up.sql`
- `server/internal/db/postgres/migrations/024_escalation_step_delay_seconds.down.sql`
- `server/internal/db/sqlite/migrations/024_escalation_step_delay_seconds.up.sql`
- `server/internal/db/sqlite/migrations/024_escalation_step_delay_seconds.down.sql`

### Modified files

- `server/internal/db/models/escalation_policy.go` — `DelayMinutes` → `DelaySeconds`,
  `RepeatAfterMinutes` → `RepeatAfterSeconds`, constructors.
- `server/internal/handlers/escalationpolicies/service.go` — validation constants and
  field references.
- `server/internal/handlers/escalationpolicies/handler.go` — request/response struct JSON
  tags.
- `server/internal/jobs/jobtypes/job_escalation_step.go` — scheduling arithmetic (L506
  and `scheduleNextCycle`).
- `server/test/integration/escalation_policies_test.go` — fixture helper JSON fields.
- `server/test/testdata.go` — any step seeds.
- `web/dash0/src/routes/orgs/$org/escalation-policies.$slug.tsx` — form field binding and
  label.
- `docs/api-specification.md` — field rename.
- Generated: `server/pkg/client/` and `web/dash0/src/generated/` — via `make generate`.

## Verification

```bash
# Build and lint
make build && make lint

# Unit and integration tests
make test

# Check migration applies cleanly
make migrate

# Dashboard smoke-test
make test-dash

# Manual: create a policy with a 5-second step delay, trigger a heartbeat failure,
# confirm the escalation step job fires within ~6s (requires Docker + Postgres).
make test-scenario
```

Specifically verify that the escalation_policies Playwright tests still pass after the
`delayMinutes` → `delaySeconds` field rename on the API.

## Implementation Plan

1. Write migration files (Postgres + SQLite) for both up and down.
2. Update the Bun model (`DelaySeconds`, `RepeatAfterSeconds`).
3. Update validation in `escalationpolicies/service.go`; add `ErrDelayTooLarge` sentinel.
4. Update the escalation step job scheduling arithmetic.
5. Update handler request/response JSON tags.
6. Update `docs/api-specification.md`; run `make generate` to regenerate client code.
7. Update dashboard form component field name.
8. Update test fixtures and seed data.
9. `make build && make lint && make test && make test-dash`.
10. Verify `make test-scenario` — the repeat-cycle test in spec 04 should now un-skip.
