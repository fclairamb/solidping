# First-class `StatusWarning` (degraded-but-up) check status

> **Code-verified 2026-06-20.** Every file:line and design choice below was checked against the
> current tree. Corrections folded in from the validation pass (see **Validation log** at the end);
> the most consequential were: `checkerdef.Status` has **no** `Created`; the aggregation rollup
> tie-break (`calculateDominantStatus`) ranks by raw status number and `Warning=7` would mask
> outages; the frontend wire string is **split** between `"warning"` and `"degraded"`; and the
> incident classifier early-returns *before* the visible-status update.

## Context

The SSL graduated-expiry spec ([`2026-06-20-01`](2026-06-20-01-ssl-graduated-expiry-and-chain-reporting.md))
and the Docker restart-loop spec ([`2026-06-20-02`](2026-06-20-02-docker-restart-loop-detection.md))
both need to express **"unhealthy but technically up"** — a cert expiring soon, a container
that's running but flapping. **Decision taken: add a first-class warning/degraded status now**, so
the state is real in the UI, availability, and incident logic, and the other two specs build on it.

There are **three** distinct status concepts; they do **not** share the same set of values, which
matters:

1. **`checkerdef.Status`** (what a checker returns, per execution) —
   `Running=2, Up=3, Down=4, Timeout=5, Error=6` (**no `Created`** — starts at `Running=2`)
   ([`checkerdef/types.go:12-18`](../../server/internal/checkers/checkerdef/types.go),
   `String()` at `:31-46` → lowercase `"up"`/`"down"`/…, string consts at `:21-28`).
   **No predicate/severity helpers** (no `IsUp`/`IsFailure`/`Severity`).
2. **`models.ResultStatus`** (the persisted `results.status`) —
   `Created=1, Running=2, Up=3, Down=4, Timeout=5, Error=6`
   ([`db/models/result.go:12-25`](../../server/internal/db/models/result.go),
   `StatusToString()` at `:36-53` → UPPERCASE `"UP"`/`"DOWN"`/…).
3. **`models.CheckStatus`** (the *visible* current status on the `checks` row) —
   `Created=1, Up=3, Down=4, Validating=5, **Degraded=7**`
   ([`db/models/check.go:14-28`](../../server/internal/db/models/check.go), `String()` at `:33-48`
   → lowercase, **`Degraded` → `"degraded"`**).
   - **`CheckStatusDegraded = 7` already exists** but is commented *"reserved for future use"*
     (`check.go:26`) — defined and `String()`-mapped, not yet produced anywhere. The big de-risker.
   - **`CheckStatusValidating = 5`** is the precedent to copy: a **display-only** status that
     "never triggers notifications, never gates the incident state machine" (`check.go:21-25`).
     Our `Degraded` mapping should follow exactly this pattern.

### What's already in our favour

- **`checks.status` has no DB CHECK constraint** (`pg 001:326` is `smallint not null default 0`;
  the comment at `pg 001:360` is stale — it predates `Degraded`). The visible degraded status
  needs **no migration**, and `CheckStatusValidating=5`/`Degraded=7` already round-trip.
- **Some of the frontend already renders a degraded/amber state** — but inconsistently (below).

### What's NOT in our favour

- **`results.status` HAS a CHECK constraint to widen — and it's currently wrong.**
  `check (status in (0, 1, 2, 3, 4, 5))` in **both** dialects
  ([`pg 001:440`](../../server/internal/db/postgres/migrations/001_v0_1_0.up.sql),
  [`sqlite 001:340`](../../server/internal/db/sqlite/migrations/001_v0_1_0.up.sql)). It **excludes
  `Error = 6`** despite that being a live value — a latent bug to fix in the same change.
- **Aggregation severity ranking breaks with `7`.** The rolled-up row status is
  `calculateDominantStatus(statusCounts)` — most-frequent wins, **ties broken by higher status
  number** (`job_aggregation.go:891`, used at `:347`, written to the row at `:936`). `Warning=7`
  is numerically the **highest**, so on a count tie a bucket with both `Down(4)` and `Warning(7)`
  rolls up to **Warning**, masking the outage. (`maxStatus` at `:744-745` has the same numeric
  assumption but is computed-and-unused for the row status.) Numeric order no longer encodes
  severity — needs an explicit severity rank.
- **The frontend wire string is split between `"warning"` and `"degraded"`** for the same state
  (see Design → Frontend). There is no single status→style mapping; each component rolls its own.
- **Incidents have no severity/kind** — the table is binary "down → open, recover → resolve"
  ([`incidents 001:512-540`](../../server/internal/db/postgres/migrations/001_v0_1_0.up.sql):
  `state`, `failure_count`, `paging_suppressed`, `resolution_type` — **no `severity`**). A
  "warning incident" can't be expressed without a new field or reusing `paging_suppressed`.

## My honest opinion

You picked the harder-but-correct option, and the *visible* slot (`CheckStatusDegraded`) plus the
`CheckStatusValidating` precedent make it tractable. The work is: (a) add the value to the two
execution-level enums, (b) widen one DB constraint, (c) make **four decisions**, (d) fix the
rollup severity ranking, and (e) unify the frontend status rendering. The decisions:

**Decision A — Availability: warning counts as UP (recommended).** A cert 20 days out, or a
container that restarted twice, is *reachable and serving*. Counting it against uptime craters
SLAs for non-outages. So a warning raw result increments `successCount` for `availability_pct`
([`job_aggregation.go:747`](../../server/internal/jobs/jobtypes/job_aggregation.go)).

**Decision B — Incidents: warning is NEUTRAL in v1 (recommended).** Incidents have no severity to
soften, so a warning must **not** open or resolve one — it only drives the visible status to
`Degraded`, exactly like `CheckStatusValidating` is display-only today. **Ordering caveat (real):**
in `ProcessCheckResult` the classifier `isSuccess`/`isFailure`
([`incidents/service.go:163-166`](../../server/internal/handlers/incidents/service.go))
**early-returns `nil` at `:168-170`** *before* the visible-status update at `:181-194`
(`deriveCheckStatus` → `UpdateCheckStatusAndClocks`). A warning that is neither success nor failure
would hit that early return and **never become visible**. So warning needs a *third* path that
bypasses the early return, sets `Degraded` via `pickStatus` (`:313-328`), is **clock-neutral** in
`deriveIncidentClocks` (`:213-245`, arms neither the failure nor the recovery clock), and defines
the up↔degraded streak edges in `deriveStreakAndChange`/`deriveCheckStatus` (`:272-306`). A warning
arriving during an open outage must **not** resolve it.

**Decision C — One wire string: `"degraded"` (recommended).** The visible status already
stringifies to `"degraded"` (`check.go:44`) and the public status page speaks `"degraded"`. The
constant can stay named `StatusWarning` (it names the *trigger*), but it should **stringify to
`"degraded"`** at every layer so the whole UI speaks one word — mirroring how `CheckStatusDegraded`
→ `"degraded"` already. (Alternative: name the new constants `*Degraded` for full top-to-bottom
alignment — cleaner, slightly more churn.) Treat any lingering `"warning"` string as a synonym.

**Decision D — Severity ranking, not numeric order.** Add a small `Severity()`/rank helper (none
exist today) so "which status is worse" is explicit, and fix the dominant-status tie-break so
`Down/Timeout/Error` outrank `Warning`.

**On notifications:** because v1 is incident-neutral there is **no automatic alert** on entering
warning — it's visible only, and you still get paged when it degrades to a real `Down`. The clean
follow-up for "warn me early" is a `paging_suppressed` incident (the column already exists) or an
incident `severity`; scoped out below.

## Goals

- Add `StatusWarning = 7` to `checkerdef.Status` and `ResultStatusWarning = 7` to
  `models.ResultStatus`, both stringifying to **`"degraded"`**, mapping a warning *result* to the
  already-reserved visible `CheckStatusDegraded = 7`.
- Widen the `results.status` CHECK constraint to accept `7` (and fix the missing `6`) in both
  dialects.
- Availability: warning counts as up (A).
- Incidents: warning is neutral — no open, no resolve — but visible status becomes `Degraded` (B),
  via a third path that survives the success/failure early-return.
- Fix the aggregation rollup so `Warning` never outranks a real failure (D).
- Unify the frontend: one status→style mapping; every status surface renders `degraded` amber (C).
- No checker-behaviour change except that checkers *may now return* `StatusWarning`.

## Out of scope

- Wiring SSL ([`2026-06-20-01`](2026-06-20-01-ssl-graduated-expiry-and-chain-reporting.md)) and
  restart-loop ([`2026-06-20-02`](2026-06-20-02-docker-restart-loop-detection.md)) to *emit*
  `StatusWarning` — those specs own that.
- **Warning notifications** (a `paging_suppressed`/degraded-severity incident, or a dedicated
  notification path) and an incident `severity` column — deliberate follow-up; v1 is neutral.

## Design

### Backend enums + wire string (Decision C)
- `checkerdef/types.go`: `StatusWarning Status = 7`; add a `String()` case returning the shared
  degraded label.
- `db/models/result.go`: `ResultStatusWarning ResultStatus = 7`; `StatusToString()` case →
  `"DEGRADED"` (UPPERCASE, matching that function's convention).
- Map result→visible: `ResultStatusWarning → CheckStatusDegraded` in the visible-status path
  (`pickStatus`, [`incidents/service.go:313-328`](../../server/internal/handlers/incidents/service.go)).
- Confirm the lowercase wire string `"degraded"` (not `"warning"`) is what reaches the frontend
  for both check-level and result-level status, so the UI has one vocabulary.

### Severity helper + availability (Decisions A, D)
- Add `func (s Status) Severity() int` (and/or `IsFailure()`) in `checkerdef` so severity is
  explicit, with `Down/Timeout/Error` ranked **above** `Warning`.
- [`job_aggregation.go:747`](../../server/internal/jobs/jobtypes/job_aggregation.go): count
  `Up` **or** `Warning` toward `successCount`.
- [`calculateDominantStatus` (`:881-898`)](../../server/internal/jobs/jobtypes/job_aggregation.go):
  replace the numeric tie-break (`status > dominantStatus`, `:891`) with the severity rank so a
  `Down`/`Warning` count tie resolves to `Down`. (`maxStatus` at `:744-745`/`:807-808` is unused
  for the row status — leave or remove, but don't let it reintroduce numeric severity.)

### Incidents (Decision B)
- [`incidents/service.go`](../../server/internal/handlers/incidents/service.go): introduce a third
  `isWarning` branch. It must **not** early-return at `:168-170`; instead run the visible-status
  update (`deriveCheckStatus`/`UpdateCheckStatusAndClocks`, `:181-194`) with `pickStatus` returning
  `CheckStatusDegraded`, keep `deriveIncidentClocks` (`:213-245`) clock-neutral for warning, and
  define up↔degraded streak/`statusChangedAt` edges in `deriveStreakAndChange`/`deriveCheckStatus`
  (`:272-306`). **Do not** call `routeCheckResultWithIncident` open/resolve logic for warning.
  Model the whole thing on the existing display-only `CheckStatusValidating`.

### Integrations
- [`slack/mention_commands.go` `statusIntToString` (~`:422-437`)](../../server/internal/integrations/slack/mention_commands.go):
  warning currently falls to `"unknown"` (default). Add a `ResultStatusWarning` case returning a
  distinct "degraded" string (add the constant alongside `statusUp`/`statusDown`).

### Frontend — unify, don't patch (Decision C)
The status→style logic is duplicated across ~7 components with an inconsistent vocabulary.
**Recommended:** introduce one shared `statusStyle(status)` util per app (color, badge variant,
label key) keyed on the canonical `"degraded"` (treating `"warning"` as a synonym), and route
every surface through it. Current state per file (verified):

| File | Today | Needs |
|---|---|---|
| dash0 `status-badge.tsx:29-34` | handles `"warning"`→validating label; **not** `"degraded"` | handle `"degraded"`; real degraded label |
| dash0 `status-dashboard.tsx:46-59` (`getCheckStatus`) | `"timeout"`→`"warning"`; no `"warning"`/`"degraded"` **input** | map degraded input → amber |
| dash0 `response-time-chart.tsx` (`:420-429`,`:642-645`) | non-neutral → red | treat degraded as neutral, not red |
| dash0 `status-timeline.tsx:44-50` | handles `"warning"`(yellow); **not** `"degraded"` | handle `"degraded"` |
| dash0 `check-summary-cards.tsx:58-62` | `isDown`=down/error/timeout; no degraded | degraded ≠ down |
| status0 `status-page-view.tsx:22-81` | **both** `"warning"`+`"degraded"` ✓ | verify only |
| status0 `availability-bar.tsx:9-20` | handles `"degraded"`; **not** `"warning"` | covered if string is `"degraded"`; add synonym |
| status0 `response-time-chart.tsx:33-44` | neither | add degraded color |

### Migration
Per the consolidated-per-release convention (`server/CLAUDE.md`), and since only `001_v0_1_0`
exists despite many shipped features (`integrations`, `status_page_subscriber` tables live in
`001`) — v0.1.0 is still being assembled in place — **amend the `results.status` CHECK constraint
in `001_v0_1_0.up.sql` for both dialects**: `check (status in (0, 1, 2, 3, 4, 5, 6, 7))` — adding
`7` (degraded) **and** the missing `6` (error). Confirm whether to amend `001` in place or cut a
`002_v0_X_0` release migration. `checks.status` needs **no** change (no constraint).

## Open questions / decisions for the user

1. **Availability** — warning counts as **up**? (recommended; alternative tanks SLAs.)
2. **Incidents** — warning **neutral** in v1? (recommended; alternative = `paging_suppressed`
   incident now, or add `severity`.)
3. **Wire string / naming** — stringify to **`"degraded"`** everywhere (recommended)? And keep the
   constant named `StatusWarning`, or rename to `StatusDegraded` for full alignment?
4. **Constraint fix** — also add the missing `6` (error) while widening? (Strongly recommended.)
5. **Migration strategy** — amend `001` in place (matches current practice) or cut `002`?
6. **Frontend** — invest in one shared `statusStyle()` util (recommended) or patch each switch?

## Verification

- **Unit/integration (table-driven, `testify/require`, `t.Parallel()`, SQLite + Postgres):**
  - A `StatusWarning` raw result **persists** (constraint accepts 7); an `Error` result persists
    too (regression for the previously-missing 6).
  - Aggregation availability: a bucket of `Up`+`Warning` → `availability_pct == 100` (A).
  - **Aggregation severity (D):** a bucket with equal counts of `Down` and `Warning` rolls up to
    `Down`, not `Warning` (guards the tie-break fix).
  - **Incident-neutral (B):** warning with no open incident → none created; with an open outage →
    stays open (not resolved); visible status → `Degraded` and streak updates in both cases; a
    subsequent `Up` clears `Degraded` → `Up`.
  - `Status.String()` / `StatusToString()` emit the canonical `"degraded"` / `"DEGRADED"`.
  - `Severity()` ranks `Down/Timeout/Error` above `Warning`.
- **Frontend (`make test-dash`):** a check whose **visible status is `"degraded"`** renders amber
  in the dash0 badge, dashboard, timeline, and summary cards (this catches the `status-badge.tsx`
  `"degraded"` gap), and on the public status page; never shown as "down".
- **Manual:** temporarily make a checker return `StatusWarning`; confirm degraded amber in dash0
  and the public page, uptime stays 100%, no incident/page fires.
- `make lint` / `make test` / `make test-dash` pass.

## Files referenced

- `server/internal/checkers/checkerdef/types.go` — `StatusWarning` value, `String()`, `Severity()`
- `server/internal/db/models/result.go` — `ResultStatusWarning` + `StatusToString()`
- `server/internal/db/models/check.go` — `CheckStatusDegraded=7` (exists; map to it), `Validating` precedent
- `server/internal/jobs/jobtypes/job_aggregation.go` — `successCount` (`:747`), `calculateDominantStatus` tie-break (`:881-898`)
- `server/internal/handlers/incidents/service.go` — three-way classify, early-return ordering, `pickStatus`/`deriveCheckStatus`/`deriveIncidentClocks`
- `server/internal/handlers/statuspages/service.go` — already serialises Degraded (verify)
- `server/internal/integrations/slack/mention_commands.go` — `statusIntToString` degraded case
- `server/internal/db/{postgres,sqlite}/migrations/001_v0_1_0.up.sql` — widen results.status CHECK (add 6 + 7)
- dash0: `status-badge.tsx`, `status-dashboard.tsx`, `status-timeline.tsx`, `check-summary-cards.tsx`, `checks/response-time-chart.tsx`
- status0: `status-page-view.tsx` (done), `availability-bar.tsx`, `response-time-chart.tsx`

## Validation log (2026-06-20 review)

Corrections made after verifying against the tree:
- **`checkerdef.Status` has no `Created`** (starts `Running=2`). Earlier draft wrongly listed
  `Created=1` for it; that value exists only in `ResultStatus`/`CheckStatus`.
- **`CheckStatusDegraded=7`** confirmed present and `String()`→`"degraded"`, comment "reserved for
  future use"; **`CheckStatusValidating=5`** identified as the display-only/incident-neutral
  precedent to copy.
- **New required change: aggregation tie-break.** `calculateDominantStatus` ranks ties by numeric
  status, so `Warning=7` would mask outages — added Decision D + a `Severity()` helper.
- **Incident ordering** pinned to real functions: the `:168-170` early-return precedes the
  visible-status update, so warning needs a third path (was vaguely stated before).
- **Frontend reality:** the `"warning"`/`"degraded"` vocabulary is split across components; the
  earlier "`status-badge.tsx` already covered" claim was wrong (it handles `"warning"`, not the
  `"degraded"` the backend emits). Added the per-file table, two missed files
  (`status-timeline.tsx`, `check-summary-cards.tsx`), and Decision C (one wire string + shared util).
- **Slack** function is `statusIntToString`; warning currently → `"unknown"`.
