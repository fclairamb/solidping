# First-class `Warning` (live) + `Degraded` (aggregated) statuses

> **Code-verified 2026-06-20; user decisions applied.** This is the *two-status* design the user
> chose (open-question 3): **`Warning`** is the live/per-execution state ("up, but something to
> report"), **`Degraded`** is its **aggregated** rollup representation ("over this window there was
> something to report"). All file:line refs below were checked against the tree. See **Decisions**
> and **Validation log** at the end.

## Concept

Two distinct states, deliberately separated by altitude:

| | **`Warning`** (value **8**, new) | **`Degraded`** (value **7**, already reserved) |
|---|---|---|
| Meaning | Live: target is up, but there's something to report (cert expiring, container flapping) | Aggregated: a rolled-up period contained warning(s) but no dominating failure |
| Produced by | a checker (`Execute` returns it) | the **aggregation job** only (never a checker) |
| Stored on | **raw** `results` rows; the **current** check status | **aggregated** `results` rows (hour/day/month) |
| Availability | counts as **up** | n/a (aggregated rows store `availability_pct` directly) |
| Incidents | **neutral** (no open/resolve) | never in the incident path (incidents see raw results only) |
| Wire string | `"warning"` | `"degraded"` |
| Colour | amber, label "Warning" | amber, label "Degraded" |

The bridge between them: **when the aggregation job rolls up a window containing raw `Warning`
results (and no dominating failure), the aggregated row's status is `Degraded`.** That is literally
"degraded when we aggregate."

## Context — the three status enums

They do **not** share one value set:

1. **`checkerdef.Status`** (checker returns, per execution) —
   `Running=2, Up=3, Down=4, Timeout=5, Error=6` (**no `Created`**)
   ([`checkerdef/types.go:12-18`](../../server/internal/checkers/checkerdef/types.go),
   `String()` `:31-46` → lowercase, string consts `:21-28`). No predicate/severity helpers.
   → **add `StatusWarning = 8`** (checkers never emit `Degraded`).
2. **`models.ResultStatus`** (persisted `results.status`) —
   `Created=1, Running=2, Up=3, Down=4, Timeout=5, Error=6`
   ([`db/models/result.go:12-25`](../../server/internal/db/models/result.go),
   `StatusToString()` `:36-53` → UPPERCASE). → **add `ResultStatusWarning = 8`** (raw rows) **and
   `ResultStatusDegraded = 7`** (aggregated rows).
3. **`models.CheckStatus`** (visible current status on `checks`) —
   `Created=1, Up=3, Down=4, Validating=5, **Degraded=7**`
   ([`db/models/check.go:14-28`](../../server/internal/db/models/check.go), `String()` `:33-48`,
   `Degraded`→`"degraded"`). → **add `CheckStatusWarning = 8`** for the current live status.
   `CheckStatusDegraded = 7` already exists (commented "reserved for future use", `check.go:26`)
   and is retained for rendering a check's **aggregated/summary** status.
   - **`CheckStatusValidating = 5`** is the precedent to copy for `Warning`: a **display-only**
     status that "never triggers notifications, never gates the incident state machine"
     (`check.go:21-25`).

### In our favour

- **`checks.status` has no DB CHECK constraint** (`pg 001:326`); `CheckStatusDegraded=7` already
  round-trips and `parseStatusFilter` already accepts `"degraded"`
  ([`checks/handler.go:45`](../../server/internal/handlers/checks/handler.go)). Adding
  `Warning=8`/`"warning"` to the current status needs **no migration**.
- The public status page already renders both `"warning"` and `"degraded"` amber
  ([`status0/status-page-view.tsx:22-81`](../../web/status0/src/components/shared/status-page-view.tsx)).

### Not in our favour

- **`results.status` CHECK constraint must widen — and is currently wrong.**
  `check (status in (0,1,2,3,4,5))` in both dialects
  ([`pg 001:440`](../../server/internal/db/postgres/migrations/001_v0_1_0.up.sql),
  [`sqlite 001:340`](../../server/internal/db/sqlite/migrations/001_v0_1_0.up.sql)) — it **excludes
  the live `Error=6`** already, and now needs `7` and `8` too.
- **Aggregation severity ranks by raw status number.** `calculateDominantStatus` picks
  most-frequent, **tie-break = higher status number**
  ([`job_aggregation.go:891`](../../server/internal/jobs/jobtypes/job_aggregation.go), used `:347`,
  written to the row `:936`). Numeric order will not encode severity once `7`/`8` exist — needs an
  explicit rank **and** the Warning→Degraded promotion (below). (`maxStatus` `:744-745` is
  computed-but-unused for the row status.)
- **Five status→string sites** must learn the new values (validation found more than first listed):
  `CheckStatus.String()` (check.go:33), `models.StatusToString()` (result.go:36),
  `results/service.go statusIntToString` (`:334`, currently collapses Down/Timeout/Error→`"down"`),
  the checks-service LastResult mapper (~`:1828`), and `slack statusIntToString` (`:422`).
- **Incidents have no severity** (`incidents 001:512-540`: `state`, `failure_count`,
  `paging_suppressed`, no `severity`) — fine, because `Warning` is neutral and `Degraded` never
  reaches the incident path.

## Design

### Enums + wire strings (two strings, both amber)
- `checkerdef/types.go`: `StatusWarning Status = 8`; `String()`→`"warning"`; add `statusStrWarning`.
- `db/models/result.go`: `ResultStatusWarning = 8`, `ResultStatusDegraded = 7`; `StatusToString()`
  → `"WARNING"` / `"DEGRADED"`.
- `db/models/check.go`: `CheckStatusWarning = 8`; `String()`→`"warning"`; keep `Degraded`→`"degraded"`.
- Wire-string mappers — add `8→"warning"` everywhere, and `7→"degraded"` where aggregated results
  are serialised:
  - `results/service.go statusIntToString` (`:334`): `case ResultStatusWarning: "warning"`,
    `case ResultStatusDegraded: "degraded"`. (Keep Down/Timeout/Error collapsed to `"down"`.)
  - checks-service LastResult mapper (~`:1820-1838`): add `Warning` → `"warning"`.
  - `slack/mention_commands.go statusIntToString` (`:422`): add `Warning` → a "warning" string.
  - `checks/handler.go parseStatusFilter` (`:30-45`): add `"warning"` alongside the existing
    `"degraded"` so both are filterable.
- Map raw `Warning` → current `CheckStatusWarning` in the visible-status path (`pickStatus`,
  [`incidents/service.go:313-328`](../../server/internal/handlers/incidents/service.go)).

### Availability (Decision A — counts as up)
- [`job_aggregation.go:747`](../../server/internal/jobs/jobtypes/job_aggregation.go): count
  `Up` **or** `Warning` toward `successCount`. An aggregated `Degraded` period can therefore be
  `availability_pct == 100`.

### Aggregation: Warning→Degraded + severity (Decision D)
- Add `func (s Status) Severity() int` in `checkerdef` (failures > Degraded > Up; Warning ranks as
  Up for availability but promotes to Degraded in rollups).
- Rework `calculateDominantStatus`/the rollup status selection (`:881-898`) so:
  1. a dominating hard failure (Down/Timeout/Error per existing rules) → that failure status;
  2. else if the window contains **any** raw `Warning` → **`Degraded`** (the promotion);
  3. else `Up`.
  Replace the numeric tie-break (`status > dominantStatus`, `:891`) with `Severity()` so a
  `Down`/anything tie resolves to the failure, never to `Warning`/`Degraded`.
  *(Promotion threshold "any warning" is the proposed default — see Decisions for the knob.)*

### Incidents (Decision B — Warning neutral; Degraded absent)
- [`incidents/service.go`](../../server/internal/handlers/incidents/service.go): add a third
  `isWarning` branch that must **not** early-return at `:168-170`; instead run the visible-status
  update (`:181-194`) with `pickStatus` → `CheckStatusWarning`, keep `deriveIncidentClocks`
  (`:213-245`) clock-neutral, define the up↔warning streak/`statusChangedAt` edges in
  `deriveStreakAndChange`/`deriveCheckStatus` (`:272-306`), and **never** open/resolve an incident.
  Model on the display-only `CheckStatusValidating`. `Degraded` is aggregate-only and never reaches
  this path (raw results are Up/Down/Timeout/Error/Warning).

### Frontend — one shared `statusStyle()` (Decision 6 — yes)
Introduce one `statusStyle(status)` util per app returning `{color, badgeVariant, labelKey}`,
handling **both** `"warning"` and `"degraded"` (both amber; labels "Warning" / "Degraded"), and
route every status surface through it. Live surfaces receive `"warning"` (current check status),
historical/aggregated surfaces receive `"degraded"` (aggregated result status). Verified per file:

| File | Surface | Today | Needs |
|---|---|---|---|
| dash0 `status-badge.tsx:29-34` | live | `"warning"`→**validating** label; no `"degraded"` | proper "Warning" label; handle `"degraded"` for summary |
| dash0 `status-dashboard.tsx:46-59` | live | `"timeout"`→`"warning"`; no `"warning"`/`"degraded"` input | route via util |
| dash0 `status-timeline.tsx:44-50` | historical | `"warning"` yellow; no `"degraded"` | handle `"degraded"` |
| dash0 `check-summary-cards.tsx:58-62` | live | `isDown`=down/error/timeout; neither | warning/degraded ≠ down |
| dash0 `checks/response-time-chart.tsx` (`:420-429`,`:642-645`) | raw points | non-neutral→red | warning/degraded = neutral, not red |
| status0 `status-page-view.tsx:22-81` | both | **both** handled ✓ | route via util (verify) |
| status0 `availability-bar.tsx:9-20` | historical | `"degraded"` ✓; no `"warning"` | covered (gets `"degraded"`); add synonym |
| status0 `response-time-chart.tsx:33-44` | both | neither | add warning + degraded |

### Migration (Decision 5 — amend in place)
Amend the `results.status` CHECK in **`001_v0_1_0.up.sql`** for **both** dialects:
`check (status in (0, 1, 2, 3, 4, 5, 6, 7, 8))` — adding `6` (the missing error, Decision 4), `7`
(degraded), `8` (warning). Only `001` exists despite shipped features (`integrations`,
`status_page_subscriber` live in `001`), so in-place is consistent. `checks.status` needs no change.

## Decisions (answered by the user 2026-06-20)

1. **Availability** → warning counts as **up**. ✅
2. **Incidents** → warning **neutral** in v1. ✅
3. **Status model** → **two statuses**: `Warning` (live "something to report") + `Degraded`
   (aggregated rollup). ✅ — this design is built around it.
4. **Constraint fix** → also add the missing `6` (error). ✅
5. **Migration** → **amend `001` in place** (both dialects). ✅
6. **Frontend** → one shared `statusStyle()` util. ✅

### Follow-on sub-decisions (introduced by the two-status model — defaults proposed)

- **a. Numeric values:** `Degraded = 7` (reuse the reserved slot), `Warning = 8` (new), across all
  three enums + the constraint. (Alternative would renumber the shipped `Degraded` — avoided.)
- **b. Rollup promotion rule:** **any** raw `Warning` in a non-failing window → `Degraded`
  (proposed). Alternatives: a threshold (≥N warnings or ≥X%), or most-frequent. "Any" matches
  "there was something to report in this window" but is the noisiest; easy to make a knob later.
- **c. Role of `CheckStatusDegraded=7` on the live `checks` row:** current status uses `Warning`;
  `Degraded` is reserved for rendering a check's **aggregated/summary** status (and stays a valid
  filter value). It is not produced by the live pipeline.

## Verification

- **Unit/integration (table-driven, `testify/require`, `t.Parallel()`, SQLite + Postgres):**
  - A raw `Warning` (8) and an `Error` (6) and an aggregated `Degraded` (7) all **persist**
    (constraint accepts 6/7/8 — the 6 case is a regression test).
  - Availability: a bucket of `Up`+`Warning` → `availability_pct == 100` (A) **and** the
    aggregated row status is **`Degraded`** (promotion rule b).
  - Severity (D): a bucket with `Down` + `Warning` rolls up to **`Down`**, never `Degraded`/`Warning`.
  - Incident-neutral (B): raw `Warning` with no open incident → none created; during an open
    outage → not resolved; current check status → `Warning`, streak updates; a later `Up` clears it.
  - Serialisation: `CheckStatus.String()`→`"warning"`/`"degraded"`; `statusIntToString` raw
    `Warning`→`"warning"`, aggregated `Degraded`→`"degraded"`; `StatusToString`→`"WARNING"`/`"DEGRADED"`.
  - `Severity()` ranks `Down/Timeout/Error` above `Degraded` above `Up`.
- **Frontend (`make test-dash`):** a check whose **current** status is `"warning"` renders amber
  "Warning" in the badge/dashboard/summary cards; an **aggregated** `"degraded"` period renders
  amber "Degraded" in the timeline/availability bar and on the public status page; neither shows as
  "down".
- **Manual:** make a checker return `StatusWarning`; confirm current status amber "Warning",
  uptime stays 100%, no incident/page; after an aggregation run the rolled-up period shows
  "Degraded".
- `make lint` / `make test` / `make test-dash` pass.

## Files referenced

- `server/internal/checkers/checkerdef/types.go` — `StatusWarning=8`, `String()`, `Severity()`
- `server/internal/db/models/result.go` — `ResultStatusWarning=8`, `ResultStatusDegraded=7`, `StatusToString()`
- `server/internal/db/models/check.go` — `CheckStatusWarning=8`, `Degraded=7` (exists), `Validating` precedent
- `server/internal/jobs/jobtypes/job_aggregation.go` — `successCount` (`:747`), Warning→Degraded promotion + severity tie-break (`:881-898`)
- `server/internal/handlers/incidents/service.go` — third `isWarning` branch, early-return ordering, `pickStatus`/`deriveCheckStatus`/`deriveIncidentClocks`
- `server/internal/handlers/results/service.go` — `statusIntToString` (`:334`) warning+degraded cases
- `server/internal/handlers/checks/service.go` — LastResult mapper (~`:1828`); `handler.go parseStatusFilter` (`:45`) add `"warning"`
- `server/internal/handlers/statuspages/service.go` — already serialises Degraded (verify)
- `server/internal/integrations/slack/mention_commands.go` — `statusIntToString` (`:422`) warning case
- `server/internal/db/{postgres,sqlite}/migrations/001_v0_1_0.up.sql` — widen results.status CHECK (6+7+8)
- dash0: `status-badge.tsx`, `status-dashboard.tsx`, `status-timeline.tsx`, `check-summary-cards.tsx`, `checks/response-time-chart.tsx`; status0: `status-page-view.tsx` (done), `availability-bar.tsx`, `response-time-chart.tsx` — route through one `statusStyle()`

## Validation log

- **2026-06-20 (answers applied):** adopted the **two-status** model (Decision 3): `Warning` (live,
  value 8) + `Degraded` (aggregated, value 7); two wire strings; the aggregation job promotes raw
  `Warning`→`Degraded`. Confirmed A (up), B (neutral), constraint+6 (4), amend-001 (5), shared
  `statusStyle()` (6). Added sub-decisions a–c.
- **2026-06-20 (code review):** corrected `checkerdef.Status` (no `Created`); confirmed
  `CheckStatusDegraded=7` reserved and `CheckStatusValidating=5` as the display-only precedent;
  found the rollup tie-break severity bug; pinned the incident early-return ordering; found there
  are **five** status→string sites (not two): `CheckStatus.String()`, `StatusToString`,
  `results/service.go statusIntToString`, the checks-service LastResult mapper, and Slack — plus
  `parseStatusFilter` already accepting `"degraded"`.
