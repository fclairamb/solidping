---
model: opus
effort: high
---

# `showAvailability: false` is silently stored as `true` — a `default:` bun tag makes every zero value unwritable on create

## Problem

`POST /api/v1/orgs/:org/status-pages` with `{"showAvailability": false}` returns a page
that says `showAvailability: true`, and the database row is `true`. The value the caller
sent never reaches Postgres or SQLite.

The cause is in the model tag, not the handler. The handler chain is correct end to end:
`applyCreateFields` sets `page.ShowAvailability = *req.ShowAvailability`
([service.go:1042](server/internal/handlers/statuspages/service.go:1042)) and
`CreateStatusPage` inserts the struct
([postgres.go:4740](server/internal/db/postgres/postgres.go:4740),
[sqlite.go:4689](server/internal/db/sqlite/sqlite.go:4689)). bun then throws the value
away:

```go
// bun@v1.2.18 query_insert.go:449
func (q InsertQuery) marshalsToDefault(f *schema.Field, v reflect.Value) bool {
	return (f.IsPtr && f.HasNilValue(v)) ||
		(f.HasZeroValue(v) && (f.NullZero || f.SQLDefault != ""))
}
```

A field whose tag declares `default:` and whose Go value is the zero value is emitted as
the literal `DEFAULT` in the `VALUES` clause (`query_insert.go:356-362`), so the DDL
default wins. `ShowAvailability bool` with `bun:"show_availability,notnull,default:true"`
([status_page.go:86](server/internal/db/models/status_page.go:86)) therefore cannot be
created as `false` by any code path.

This is exactly the trap already documented on `StatusPage.AutoPublishDelaySeconds`
([status_page.go:97-107](server/internal/db/models/status_page.go:97)), whose tag
deliberately carries **no** `default:` clause for this reason. The documentation exists;
the audit that would have found the sibling fields never happened.

Only CREATE is affected — the UPDATE paths build explicit `Set(...)` clauses and are fine.
A workaround comment recording the bug already sits in the test fixture
([page_availability_test.go:152-163](server/internal/handlers/statuspages/page_availability_test.go:152)),
which flips the flag through `UpdateStatusPage` because create cannot express it.

### The rule, and how wide it goes

A `default:` tag is only harmful when **the DDL default differs from Go's zero value**.
`default:false`, `default:0` and `default:''` are inert (the zero value and the default
agree) — misleading, but not bugs. Every tag where they disagree is an unwritable zero:

On `models.StatusPage` ([status_page.go](server/internal/db/models/status_page.go)):

| Field | Tag | Zero value is |
|---|---|---|
| `Enabled` | `default:true` | a page created disabled — currently impossible |
| `ShowAvailability` | `default:true` | the reported bug |
| `ShowResponseTime` | `default:true` | same, unreported |
| `HistoryDays` | `default:90` | `0` (unreachable, but the enum is the source of truth) |
| `HistoryPeriod` | `default:'90d'` | `""` — never legal, so inert in practice |
| `Visibility` / `AutoResolve` / `CustomDomainState` | non-empty string defaults | `""` — never legal |
| `IsDefault`, `AutoPublish`, `CustomDomainFailures`, `CustomDomainSuccesses` | `default:false` / `default:0` | inert (agree with zero) |

The same class of tag exists on other models, and at least two look like live bugs:

- `Check.FlappingWindowSeconds` `default:21600`, `Check.FlapBackoffFactor` `default:2`,
  `Check.MaxRecoveryMultiplier` `default:8`
  ([check.go:141-143](server/internal/db/models/check.go:141)) — the comment immediately
  above them states that `FlapBackoffFactor == 1` or `FlappingWindowSeconds == 0`
  reproduces constant-recovery behavior. `FlappingWindowSeconds: 0` on create silently
  becomes 21600, so flapping cannot be turned off at creation time.
- `Check.EscalationThreshold` `default:3`
  ([check.go:118](server/internal/db/models/check.go:118)) — `0` on create becomes 3.
- `Integration.Enabled` `default:true`
  ([integration.go:108](server/internal/db/models/integration.go:108)) — an integration
  cannot be created disabled.
- `UserNotificationRoute.Enabled` `default:true`
  ([user_contact.go:103](server/internal/db/models/user_contact.go:103)) — same shape.
- `IncidentCheck.CurrentlyFailing` `default:true`, `Incident.FailureCount` `default:1`,
  `Incident.State` `default:1` ([incident.go](server/internal/db/models/incident.go)) —
  these are written by internal code that always sets a non-zero value, so likely inert,
  but they need a decision recorded rather than an assumption.
- `created_at` / `updated_at` `default:current_timestamp` are **fine and should stay**: a
  zero `time.Time` falling back to the DDL default is the desired behavior.

## Proposal

1. **Fix the reported bug.** Drop the `default:` clause from the bun tags on
   `StatusPage.Enabled`, `ShowAvailability` and `ShowResponseTime`. The DDL default is
   untouched and still applies to rows inserted outside the application (migrations, an
   upgraded installation's existing rows) — that is the whole reason dropping the tag is
   safe, and it is what `AutoPublishDelaySeconds` already does. Carry a short comment on
   each, pointing at the `AutoPublishDelaySeconds` note rather than repeating it.

   `NewStatusPage` already sets all three to `true`
   ([status_page.go:196-199](server/internal/db/models/status_page.go:196)), so a create
   request that omits the field still lands on `true` — the Go constructor, not the DDL,
   becomes the single source of the default. Verify that no other create path builds a
   `StatusPage` without going through `NewStatusPage`; if one exists, it would start
   writing `false` where it previously inherited `true`, and that is the one regression
   this change can cause.

2. **Audit the rest of the models.** Walk every `default:` tag in
   `server/internal/db/models/` and classify each as: *harmful* (DDL default differs from
   the Go zero value and the zero value is legal user input) → drop the clause; *inert*
   (default agrees with the zero value) → drop it too, since it is a loaded gun for the
   next person to change the default; or *deliberate* (`current_timestamp` on
   `created_at`/`updated_at`) → keep, with a comment saying why. The candidates above are
   a starting list, not the finished audit — the grep is
   `grep -rn 'default:' server/internal/db/models/`.

   Where a field's zero value is legal, the create path must be able to express it; where
   a zero value is not legal, validation should reject it rather than being silently
   rewritten by the database.

3. **Add a guard that fails on reintroduction.** A reflection test over the registered
   models that walks every struct field's bun tag and fails when a non-`time.Time` field
   declares a `default:` whose value differs from that field's Go zero value. This is the
   part that makes the fix durable — the `AutoPublishDelaySeconds` comment was already
   correct and still did not stop the same bug being written three fields above it. An
   explicit, commented allowlist covers any field the audit decides to keep.

4. **Round-trip tests on the create path, per field.** For each flag fixed in step 1, a
   test that creates via `svc.CreateStatusPage` with the zero value and reads the row
   back with `svc.db.GetStatusPageBySlug`, asserting the zero value survived — plus the
   positive control that omitting the field still yields the non-zero default. Run them
   against both dialects, since `DEFAULT`-placeholder handling is a bun/driver feature
   flag and Postgres and SQLite reach it by different paths. Cover the non-status-page
   fields the audit reclassifies in step 2 the same way.

5. **Remove the workaround.** Delete the comment and the `UpdateStatusPage` detour in
   `buildAggregatePage`
   ([page_availability_test.go:152-164](server/internal/handlers/statuspages/page_availability_test.go:152))
   and let the fixture pass `ShowAvailability: &showAvailability` to
   `CreateStatusPageRequest` directly. The test then exercises the create path it was
   always meant to.

### Open question

Whether `HistoryDays` should also lose its `default:90`. `history_days` is a deprecated
back-compat column driven by `HistoryPeriod`, and `0` is never a value a caller sends, so
the tag is inert today. Dropping it is consistent; keeping it costs nothing. Decide once
and record the reason in the tag comment either way.

## Implementation Plan

### Findings that shape the plan

- **Blast radius of dropping a `default:` clause is exactly one thing.** `SQLDefault`
  is read by bun in three places: the INSERT `DEFAULT`-placeholder path
  (`query_insert.go:358,447`), `CreateTable` DDL generation
  (`query_table_create.go:191`), and the `migrate/sqlschema` inspector. This repo
  generates **no** DDL from models — every table comes from hand-written SQL in
  `internal/db/{postgres,sqlite}/migrations/`, and the single `NewCreateTable()` call
  (`internal/db/migrationguard/migrationguard.go:327`) targets `checksumRow`, which lives
  outside `internal/db/models/`. So the DDL defaults are untouched by this change.
- **There are no CHECK constraints** in either dialect's migrations. An enum column
  that loses its `default:` tag and gains no Go-side default would be silently written as
  `''` rather than erroring — so every tag dropped here must have a Go-side default.
- **Only two production paths create a `StatusPage`**: `service.go:1270` via
  `models.NewStatusPage`, and the test-mode seed literal at
  `test/testdata/testdata.go:319`, which already sets `Enabled`, `ShowAvailability` and
  `ShowResponseTime` explicitly. No regression from step 1.

### Audit result (`grep -rn 'default:' server/internal/db/models/`)

71 non-timestamp `default:` clauses, 94 `default:current_timestamp` clauses.

| Class | Count | Action |
|---|---|---|
| *harmful* — default differs from the Go zero value and the zero value is legal user input | 10 | drop the clause |
| *inert* — `default:0` (26), `default:false` (14), `default:''` (1); the default agrees with the zero value | 41 | drop the clause (loaded gun for the next person to change the default) |
| *non-zero default, zero never legal* — enum/counter columns whose Go constructor already supplies the value | 20 | drop the clause; the constructor is the single source of the default |
| *deliberate* — `default:current_timestamp` on a `time.Time` | 94 | **keep**, with the guard asserting the field really is a `time.Time` |

The 10 harmful ones: `StatusPage.Enabled` / `.ShowAvailability` / `.ShowResponseTime`,
`Check.EscalationThreshold` / `.FlappingWindowSeconds` / `.FlapBackoffFactor` /
`.MaxRecoveryMultiplier`, `Integration.Enabled`, `UserNotificationRoute.Enabled`,
`IncidentMemberCheck.CurrentlyFailing`.

### Steps

1. **Model tags.** Drop every non-`current_timestamp` `default:` clause in
   `server/internal/db/models/`. Add the one missing Go-side default this exposes —
   `StatusPage.CustomDomainState` is not set by `NewStatusPage` — and complete the
   test-mode seed literal in `test/testdata/testdata.go` (`HistoryPeriod`, `AutoResolve`,
   `CustomDomainState`). Comment the three status-page flags and the flapping trio,
   pointing at the existing `AutoPublishDelaySeconds` note rather than repeating it.
2. **`HistoryDays`** (the spec's open question): drop the `default:90` too, and record
   the reason in the tag comment — `history_days` is deprecated back-compat driven by
   `HistoryPeriod`, `NewStatusPage` sets 90, and leaving one non-zero default behind would
   mean carrying an allowlist entry forever for a column nobody writes zero to.
3. **Guard test** (`internal/db/models/default_tag_guard_test.go`). Rather than reflection
   over a hand-maintained registry — which a newly added model silently escapes — parse
   the package source with `go/ast` and walk every struct field's `bun` tag. Fails when a
   field declares a `default:` whose literal differs from that field's Go zero value, and
   when a `default:current_timestamp` sits on a field that is not a `time.Time`. Explicit,
   commented (and currently empty) allowlist.
4. **Round-trip tests, both dialects.** `TestZeroValuesSurviveCreate_SQLite` /
   `_Postgres` in `internal/db/{sqlite,postgres}` cover the status-page flags, the check
   flapping/escalation fields and `Integration.Enabled` — zero value in, zero value out —
   each with the positive control that a constructor-built row still yields the non-zero
   default. Plus a service-level test in `internal/handlers/statuspages` driving the real
   API path (`CreateStatusPageRequest{ShowAvailability: ptr(false)}`).
5. **Remove the workaround** in `page_availability_test.go:152-164`: the fixture passes
   `ShowAvailability` to `CreateStatusPageRequest` directly.
6. **Gate**: `make build-backend lint-back test` from the repo root.
