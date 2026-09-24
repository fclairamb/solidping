---
model: opus
effort: xhigh
---

# A check that stops running keeps its last status forever, so a dead region looks healthy

## Problem

`checks.status` only changes when a result arrives. The single writer is
`ProcessCheckResult` → `UpdateCheckStatusAndClocks`
([incidents/service.go:605-702](server/internal/handlers/incidents/service.go#L605)).
When results stop, nothing ever changes the status: the last one stays forever.
"unknown" is only the fallback text for an unrecognized integer
([check.go:40-57](server/internal/db/models/check.go#L40),
[status_wire.go:7-32](server/internal/db/models/status_wire.go#L7)).

On 2026-09-24 the `lauterbourg` region was dark for 8 hours. Its 12 pinned
checks kept `status = up` the whole time.

- **The dashboard** showed them green. The only clue was "last checked", and
  that uses the newest result across all regions, counting `abandoned` rows
  (`check-summary-cards.tsx:64-68`). So one live region hides a dead one.
- **Public status pages** said "operational" for components nobody was
  measuring.
- **Availability** divides downtime by wall-clock time since creation, not by
  time actually measured
  ([availability/service.go:228-242, 268-269](server/internal/handlers/availability/service.go#L228)).
  So an 8-hour gap in an otherwise healthy window reports 100% and zero
  downtime. The comment at `:263-265` claims the opposite.
- **SLO budgets** stretch the same way
  ([slo/budget.go:166](server/internal/slo/budget.go#L166)).
- **Badges** show the newest raw result with no age limit
  ([badges/service.go:634-648, 696-712](server/internal/handlers/badges/service.go#L634)).

For a monitoring product this is the worst failure: false reassurance. It is
also independent of the cause. A dead region, a stuck scheduler, a lease leak,
rate limiting or a database stall all produce the same silent green.

## Proposal

### 1. A new status: `stale`

- New `CheckStatusStale` with wire name `stale`. The UI label is **"No data"**.
  Pick an unused integer: 1, 3, 4, 5, 7 and 8 are taken.
- `checks.status` has no CHECK constraint in either database
  (`postgres/migrations/001_v0_1_0.up.sql:326`,
  `sqlite/migrations/009_v0_8_0.up.sql:88`), so the status itself needs no
  migration. The column comment at `:360` is already wrong; fix it.
- The word "stale" already means other things (the status0 TV board's
  lost-connection state in `tv-board.ts:18-23`, the watchdog's "stale
  incidents"). Keep them apart in code and copy.

**Definition.** A check is stale when its newest **real** result, across all its
regions, is older than `max(3 × period, 5 min)`.

- "Real" excludes `created` and `abandoned` placeholder rows.
- Passive checks count their evaluation rows. They go stale only if their
  evaluator stops (spec `2026-09-25-04`).
- A check that never produced a result goes stale once it is older than the
  threshold: it should have run by then.

**Not considered:**
- disabled checks;
- internal checks;
- checks in an **active maintenance window**. They never enter stale while the
  window is open. The first sweep after the window evaluates them normally.

**Per-region freshness** is shown, not a status: "no result from lauterbourg
since 13:41, 2 other regions reporting". A check still reporting from some
region is still being checked. Its reduced coverage is a warning in the UI (and
the region notice of spec `2026-09-25-03`), not a status change. Real per-region
state is spec `2026-09-25-06` part B.

### 2. The sweeper

A `check_freshness_sweep` job on the jobs node, every minute:

- register the type in `jobdef/types.go:143-171`, **not** in the public-creation
  list at `:202`;
- add the factory in `jobtypes/registry.go:12-66`;
- seed the first run in `job_startup.go:158-179`;
- re-arm with `periodicSweep` (`self_rescheduling_sweep.go:56-114`), which
  survives errors;
- expose the evaluator through an interface in `app/services/services.go:41-77`,
  wired at `app/server.go:1921-1929`.

To keep the sweep a single indexed query, add a denormalized
`checks.last_result_at`:

- `ProcessCheckResult` sets it for every real result, **including** the early
  return for checks in maintenance (`:615-627`).
- Add a migration and index in both databases.
- The sweep selects enabled, non-internal checks outside maintenance where
  `coalesce(last_result_at, created_at) < now() - threshold` and
  `status <> stale`.

**Entering stale** is one guarded update:

```sql
UPDATE checks
   SET status = <stale>, status_changed_at = now(),
       first_failure_at = NULL, first_success_since_failure_at = NULL
 WHERE uid = ? AND status = ?old
   AND coalesce(last_result_at, created_at) < ?cutoff
```

- It is a compare-and-set, so a result landing at the same moment wins.
- It bypasses `ProcessCheckResult` completely: no events, no notifications, and
  `status_streak` untouched.
- **Both confirmation clocks are cleared on purpose.** Today they survive a gap
  (`:1040-1047, 1059`): the first failure after 8 hours of silence would open an
  incident immediately, and the first success would resolve one immediately.
  After a gap, both confirmation and recovery must be earned again from fresh,
  continuous evidence.
- The sweeper emits `checks` realtime hints for the rows it changes. Above 64
  checks they collapse into one "all checks" hint
  ([realtime.go:29-46](server/internal/realtime/realtime.go#L29)), which suits
  a whole region dying.

**Leaving stale** happens in `ProcessCheckResult` on the first real result:

- It writes the result's status even inside a maintenance window. Otherwise a
  check could stay stale until the window ends.
- The existing "fresh edge" handling already resets the streak and bumps
  `status_changed_at` (`:853-893`).
- Decide "changed" against the **current row**, not the claim-time snapshot
  (`backend/direct.go:303-315`). Otherwise a stale→up flip written in between is
  missed and no `checks` hint is sent.

### 3. Incidents: stale is neither down nor up

- Stale never opens, resolves or re-notifies an incident.
- An **open incident stays open** while the check is stale. It gets a timeline
  event "monitoring interrupted since 13:41" and another when data resumes.
  Resolution needs a full recovery window of fresh successes.
- **Rollups**
  ([rollup.go:500-536](server/internal/handlers/incidents/rollup.go#L500)):
  when a parent resolves, any child that is not `down` is detached today, so a
  stale child would be detached and never paged. Treat a stale child as still
  attached: unknown is not recovered.
- **Dependency hold** (`rollup.go:436-458`): a stale parent does **not** hold
  its children (today's behavior, now deliberate). Suppressing a child's alert
  because of a parent we cannot see risks a missed page, while releasing only
  risks an extra one.
- Fix the `check.go:21-25` comment claiming validating "never gates" incidents;
  it does since `rollup.go:439`.

### 4. Everywhere a status is shown

**Server:**
- `?status=stale` filter token in
  [checks/handler.go:34-61](server/internal/handlers/checks/handler.go#L34)
  (400 today).
- The `byStatus` keys in `stats.go:96-106`.
- The `lastStatusChange` value (`service.go:1003-1024`) is printed as-is by the
  CLI and the Slack/Teams/Discord commands; make them say "no data".
- Group status (`check_group_status.go:23-49`, plus the dash0 copy in
  `checks.index.tsx:331-352`): rank **down > validating > warning > stale > up**.
  An all-stale group reads stale, not `created`.
- Status pages:
  - `page_status.go:88-91` counts stale as Unknown, and `publicCheckStatus`
    (`statuspages/service.go:4356-4382`) defaults to `created`.
  - A stale component shows the neutral **"No data, last checked 13:41"**,
    never "operational".
  - Stale components don't count as operational in the page aggregate. An
    all-stale page reads "Status unknown".
- Badges: when the newest real result is older than the threshold, render the
  gray "no data" badge.
- MCP `diagnose_check` (`tools_diagnose.go:79-100`) trims per region, so a dead
  region simply disappears. Say "no result from lauterbourg since 13:41"
  instead.
- OpenAPI: add `stale` to the enums at `openapi.yaml:10007, 10274-10286, 14952`.
  While there, add the missing top-level `status` on `Check` and document
  `?status=` on `listChecks` (1367). Regenerate the client
  (`server/pkg/client/generate.go`).

**dash0:**
- `lib/status-style.ts:39-121`: gray, clock icon.
- `status-badge.tsx:18-21` prints the raw word for any unknown status (the bug
  class behind untranslated labels). Make it go through the label map.
- `api/hooks.ts:191` status type: add `stale`, and the missing `warning`.
- `checks.index.tsx:156, 244-251`: filter and summary order.
- Check detail header (`checks.$checkUid.index.tsx:1091, 1112-1119, 1595-1599`):
  "No data since 13:41" with the per-region freshness list.
- `check-summary-cards.tsx`:
  - "Last checked" ignores `abandoned` rows;
  - shows per-region ages when regions disagree;
  - the "status for X" timer uses `status_changed_at`.
- Check groups are not refreshed by `checks` hints (`hooks.ts:1192`); make them.

**status0:** `lib/status-style.ts:74-200` and `lib/tv-board.ts:359-380`.

**Locales:** every one of the 8 files.
- dash0 `locales/{en,fr,de,es}/checks.json`, both `status.*` and
  `groupSummary.*`. A parity test covers them.
- status0 `locales/{en,fr,de,es}/status.json`. Only `tv.*` has a parity test
  today (`tv-locales.test.ts`); extend it to the status keys so a missing fr/de/es
  key fails CI.

Follow the design reference for every UI change.

### 5. Coverage instead of stretched numbers

- Expected probes = `window / period × max(1, len(regions))`, the same formula
  as `entitlements/check_rate.go:102-130`.
- **Availability** (`availability/service.go:101-115, 199-269`):
  - return `coverage` (measured probes / expected probes) and
    `unmeasuredSeconds`;
  - compute downtime over **measured time only**;
  - fix the comment.
- **SLO** (`slo/budget.go:166`, inputs `:27-45`, built at
  `handlers/slos/service.go:784-790`):
  - same rule, exposed as `dataCoverage`. `CoverageStart` already exists there
    with another meaning;
  - an SLO view with low coverage says so next to the percentage.
- **Uptime bar:** keep coverage out of the size-capped bucket struct
  (`uptimebar/bucketing.go:95-104`). Tooltips compute "measured 67% of the
  interval" from probes vs expected. status0 day bars (`statuspages/service.go:3924-3967`,
  `availability-bar.tsx:25-40`) show the same in their tooltip.

### 6. Operator signal

- Publish `solidping_checks_stale` (count, by region of the placement).
- Add a watchdog detector **stale-checks**: stale checks whose regions are
  **not** dark. Those are platform bugs (scheduler, leases, rate limiting), not
  region outages, and only the operator can fix them.
- ~~Spec `2026-09-25-01` wires `solidping_check_up`. Give stale its own value
  there (for example `-1`) rather than folding it into 0 or 1.~~ **Superseded**:
  spec `2026-09-25-01` (implemented first) REMOVED `solidping_check_up` entirely
  — it was registered but never written to, and is unbounded per-check
  cardinality (slug × type × region × org) that nothing read. It does not exist
  to wire a stale value into. `solidping_checks_stale` (the counter this
  section already proposes above) is this spec's only metrics surface for
  staleness; there is no second gauge to coordinate with.

### Tests

- A pinned check whose worker stops goes stale after `max(3 × period, 5 min)`.
  No incident, no notification, one `checks` hint.
- A result arriving at the same instant as the sweep: the result wins (guarded
  update).
- The first result after stale restores the status. A failure after a gap waits
  for a fresh confirmation window. A success resolves an open incident only
  after a fresh recovery window.
- Maintenance: a check never enters stale during a window, and leaves stale
  immediately if a result arrives during one.
- A multi-region check with one silent region is **not** stale, and shows
  per-region freshness.
- A stale child stays attached to its rollup. A stale parent does not hold its
  children.
- Group ranking, page aggregate, public component and badge all render "No
  data".
- Availability with an 8h gap in a 24h all-up window reports ~67% coverage, 0
  downtime over measured time, and no longer "100% over 24h".
- The locale parity tests cover the new keys in both apps.

## Related

- `2026-09-25-03` tells orgs and the operator **why** checks went stale when a
  whole region is dark. This spec makes the state visible whatever the cause.
- `2026-09-25-04` moves passive evaluation to the jobs node.
- `2026-09-25-06` adds per-region state and failover.
