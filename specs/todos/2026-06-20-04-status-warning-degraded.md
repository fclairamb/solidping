# First-class `StatusWarning` (degraded-but-up) check status

## Context

The SSL graduated-expiry spec ([`2026-06-20-01`](2026-06-20-01-ssl-graduated-expiry-and-chain-reporting.md))
and the Docker restart-loop spec ([`2026-06-20-02`](2026-06-20-02-docker-restart-loop-detection.md))
both need to express **"unhealthy but technically up"** — a cert expiring soon, a container
that's running but flapping. The status enum has no such value today, so both specs originally
worked around it with output flags. **Decision taken: add a first-class warning status now**, so
the degraded state is real in the UI, availability, and incident logic — and the other two specs
build on it instead of faking it.

There are **three** distinct status concepts in the code; only one needs a new *value*, the other
two need an aligned addition:

1. **`checkerdef.Status`** (what a checker returns, per execution) —
   `Created=1, Running=2, Up=3, Down=4, Timeout=5, Error=6`
   ([`checkerdef/types.go:12-18`](../../server/internal/checkers/checkerdef/types.go),
   `String()` at `:31-46`, string consts at `:21-28`). **No predicate helpers** (no IsUp/IsDown).
2. **`models.ResultStatus`** (the persisted `results.status`) — mirrors the above
   ([`db/models/result.go:12-25`](../../server/internal/db/models/result.go),
   `StatusToString()` at `:36-53`).
3. **`models.CheckStatus`** (the *visible* current status on the `checks` row) —
   `Created=1, Up=3, Down=4, Validating=5, **Degraded=7**`
   ([`db/models/check.go:14-28`](../../server/internal/db/models/check.go), `String()` at `:34-47`).
   **`CheckStatusDegraded = 7` already exists** — the visible degraded slot was reserved ahead of
   time. This is the big de-risker.

### What's already in our favour

- **`checks.status` has no DB CHECK constraint** (`pg 001:326` is just
  `smallint not null default 0`; the comment at `pg 001:360` is stale — it predates `Degraded`).
  So the visible degraded status needs **no migration**.
- **The frontend already half-handles `"warning"` / `"degraded"`:**
  - dash0 status badge already maps `"warning"` to the amber variant
    ([`status-badge.tsx:29`](../../web/dash0/src/components/shared/status-badge.tsx)).
  - status0 public page already maps `"warning"`/`"degraded"` to colour, badge, and label
    ([`status-page-view.tsx:22-67`](../../web/status0/src/components/shared/status-page-view.tsx)).
  - `statuspages` backend already serialises `CheckStatusDegraded`
    ([`statuspages/service.go:1412-1426`](../../server/internal/handlers/statuspages/service.go)).

### What's NOT in our favour

- **`results.status` HAS a CHECK constraint that must be widened** — and it's currently *wrong*:
  `check (status in (0, 1, 2, 3, 4, 5))` in **both** dialects
  ([`pg 001:440`](../../server/internal/db/postgres/migrations/001_v0_1_0.up.sql),
  [`sqlite 001:340`](../../server/internal/db/sqlite/migrations/001_v0_1_0.up.sql)). Note it
  **excludes `Error = 6`** even though that's a live enum value — a latent bug to fix in the same
  change (see "Migration").
- **Incidents have no severity/kind** — the table is essentially binary "check went down → open
  incident, recovered → resolve" ([`incidents table 001:512-540`](../../server/internal/db/postgres/migrations/001_v0_1_0.up.sql),
  fields: `state`, `failure_count`, `paging_suppressed`, `resolution_type` — **no `severity`**).
  So "a warning incident vs an outage incident" cannot be expressed without either a new field or
  reusing `paging_suppressed`.

## My honest opinion

You picked the harder-but-correct option, and it's more tractable than it looks because the
*visible* slot (`CheckStatusDegraded = 7`) and most of the frontend are already there. The work is
really: (a) add the value to the two execution-level enums, (b) widen one DB constraint, (c) make
**two product decisions**, and (d) close the handful of frontend/integration switch gaps the
existing code missed. The two decisions are what actually matter:

**Decision A — Availability: warning counts as UP (recommended).** A cert expiring in 20 days or a
container that restarted twice is *reachable and serving*. Counting it against uptime would crater
SLAs for non-outages and make "availability" meaningless. So a `StatusWarning` raw result should
increment `successCount` for `availability_pct`
([`job_aggregation.go:747`](../../server/internal/jobs/jobtypes/job_aggregation.go)). Degradation
is surfaced through the *status*, not by lying about uptime.

**Decision B — Incidents: warning is NEUTRAL in v1 (recommended).** Opening a full incident for a
warning would page people for non-outages (incidents have no severity to soften them). So in v1 a
warning result **neither opens nor resolves** an incident — it only drives the *visible* status to
`Degraded` and the streak. Concretely, the classifier at
[`incidents/service.go:163-168`](../../server/internal/handlers/incidents/service.go) becomes
**three-way**: success (Up) → may resolve; failure (Down/Timeout/Error) → may open; warning →
neither. **Critical ordering caveat:** today the function early-returns `nil` when a result is
neither success nor failure (`:168`); we must make sure the **visible-status update to `Degraded`
still happens** for warning results and isn't skipped by that early return. A warning arriving
during an open outage must **not** resolve it (warning ≠ success).

**On notifications:** because v1 keeps warning incident-neutral, there is **no automatic
notification** on entering the warning state — degradation is visible in the dashboard/status
page, and you still get paged when it eventually becomes a real `Down` (e.g. the cert actually
expires → handshake fails → Down → incident). If we want "warn me early" alerts, the clean
follow-up is a *non-paging* incident using the existing `paging_suppressed` flag (or a new
`severity`), scoped out below. I'd ship v1 incident-neutral and add warning-notifications as a
deliberate next step rather than smuggling paging semantics in now.

## Goals

- Add `StatusWarning = 7` to `checkerdef.Status` and `ResultStatusWarning = 7` to
  `models.ResultStatus`, mapping a warning *result* to the already-reserved visible
  `CheckStatusDegraded = 7`.
- Widen the `results.status` CHECK constraint to accept `7` (and fix the missing `6`) in both
  dialects.
- Availability: warning counts as up (Decision A).
- Incidents: warning is neutral — no open, no resolve — but the visible status becomes `Degraded`
  (Decision B), without breaking outage open/resolve for the real failure statuses.
- Close every remaining status switch/map that doesn't yet handle warning (backend + frontend).
- No change to checker behaviour except that checkers *may now return* `StatusWarning`.

## Out of scope

- Wiring SSL ([`2026-06-20-01`](2026-06-20-01-ssl-graduated-expiry-and-chain-reporting.md)) and
  restart-loop ([`2026-06-20-02`](2026-06-20-02-docker-restart-loop-detection.md)) to actually
  *emit* `StatusWarning` — those specs own that; this one just makes the status exist and behave.
- **Warning notifications** (paging-suppressed/“degraded-severity” incidents, or a dedicated
  degraded notification path). Deliberate follow-up; v1 is incident-neutral.
- An incident `severity` column. (Mentioned as the cleaner long-term home for warning alerts.)

## Design

### Backend enums
- `checkerdef/types.go`: `StatusWarning Status = 7`, `statusStrWarning = "warning"`, add the
  `String()` case (`:31-46`).
- `db/models/result.go`: `ResultStatusWarning ResultStatus = 7`, add `StatusToString()` case
  (`:36-53`, returns `"WARNING"`).
- Mapping result→visible: wherever a raw result status is turned into the check's visible
  `CheckStatus`, map `ResultStatusWarning → CheckStatusDegraded` (the `pickStatus`-style logic
  around [`incidents/service.go:317-328`](../../server/internal/handlers/incidents/service.go)).

### Availability (Decision A)
- [`job_aggregation.go:747`](../../server/internal/jobs/jobtypes/job_aggregation.go): count
  `ResultStatusUp` **or** `ResultStatusWarning` toward `successCount`; the downstream
  `availability_pct` / rollup math follows automatically.

### Incidents (Decision B)
- [`incidents/service.go:163-168`](../../server/internal/handlers/incidents/service.go): make the
  classification three-way (success / warning-neutral / failure). Mirror at the streak/relapse
  sites (`:275-285`). Ensure the **visible status + streak update runs for warning** and is not
  short-circuited by the neutral early-return.

### Integrations
- [`slack/mention_commands.go:428-435`](../../server/internal/integrations/slack/mention_commands.go):
  add a `ResultStatusWarning` case (render as a distinct "warning"/"degraded" string, not "up"
  or "down").

### Frontend gaps (the spots the existing partial support missed)
- status0 [`availability-bar.tsx:10-19`](../../web/status0/src/components/shared/availability-bar.tsx):
  add `case "warning"` → amber (mirror the existing `"degraded"` case).
- status0 [`response-time-chart.tsx:33-43`](../../web/status0/src/components/shared/response-time-chart.tsx):
  add `"warning"` colour.
- dash0 [`status-dashboard.tsx:46-59`](../../web/dash0/src/components/shared/status-dashboard.tsx):
  add `case "warning"` → `"warning"` UI status.
- dash0 [`response-time-chart.tsx`](../../web/dash0/src/components/checks/response-time-chart.tsx):
  decide warning = neutral (don't mark as a failure point on the chart).
- Already covered (verify only): dash0 `status-badge.tsx:29`, status0 `status-page-view.tsx:22-67`.

### Migration
Per the consolidated-per-release convention (`server/CLAUDE.md`), and since only
`001_v0_1_0` exists despite many shipped features (e.g. `integrations`, `status_page_subscriber`
tables live in `001`) — i.e. `v0.1.0` is still being assembled in place — **amend the
`results.status` CHECK constraint in `001_v0_1_0.up.sql` for both dialects**:
`check (status in (0, 1, 2, 3, 4, 5, 6, 7))` — adding `7` (warning) **and** the missing `6`
(error). Confirm with the maintainer whether to amend `001` in place or cut a `002_v0_X_0`
release migration. `checks.status` needs **no** change (no constraint).

## Open questions / decisions for the user

1. **Availability** — confirm warning counts as **up** (recommended). The alternative (warning
   counts against uptime) makes degraded states tank SLAs.
2. **Incidents** — confirm warning is **neutral** in v1 (recommended). Alternatives: warning
   opens a `paging_suppressed` incident (notifies, doesn't page) now, or stays neutral and we add
   that later.
3. **Constraint fix** — OK to also add the currently-missing `6` (error) to the `results.status`
   constraint while widening it? (Strongly recommended; it looks like a latent bug.)
4. **Migration strategy** — amend `001` in place (matches current practice) or cut `002`?

## Verification

- **Unit/integration (table-driven, `testify/require`, `t.Parallel()`, SQLite + Postgres via
  testcontainers):**
  - A `StatusWarning` raw result **persists** (constraint accepts 7) — and an `Error` result
    persists too (regression test for the previously-missing 6).
  - Aggregation: a bucket of Up+Warning yields `availability_pct == 100` (Decision A).
  - Incident-neutral: a warning result with **no** open incident → no incident created; with an
    **open** outage incident → incident stays open (not resolved); the **visible** check status
    becomes `Degraded` and the streak updates in both cases.
  - A subsequent `Up` after warning resolves nothing-extra and clears `Degraded` → `Up`.
  - `Status.String()` / `StatusToString()` round-trip `"warning"` / `"WARNING"`.
- **Frontend (`make test-dash`):** a check/resource in warning renders amber (badge, availability
  bar, dashboard, public status page) and is labelled degraded, not down.
- **Manual:** temporarily make a checker return `StatusWarning`; confirm the check shows degraded
  in dash0 and on a public status page, uptime stays 100%, and no incident/page fires.
- `make lint` / `make test` / `make test-dash` pass.

## Files referenced

- `server/internal/checkers/checkerdef/types.go` — `StatusWarning` value + `String()`
- `server/internal/db/models/result.go` — `ResultStatusWarning` + `StatusToString()`
- `server/internal/db/models/check.go` — `CheckStatusDegraded = 7` (already exists; map to it)
- `server/internal/jobs/jobtypes/job_aggregation.go` — availability counts warning as up (`:747`)
- `server/internal/handlers/incidents/service.go` — three-way classify; visible-status ordering
- `server/internal/handlers/statuspages/service.go` — already serialises Degraded (verify)
- `server/internal/integrations/slack/mention_commands.go` — warning display case (`:428-435`)
- `server/internal/db/{postgres,sqlite}/migrations/001_v0_1_0.up.sql` — widen results.status CHECK
- `web/dash0/src/components/shared/{status-badge,status-dashboard}.tsx`,
  `web/status0/src/components/shared/{status-page-view,availability-bar,response-time-chart}.tsx`
  — warning rendering gaps
