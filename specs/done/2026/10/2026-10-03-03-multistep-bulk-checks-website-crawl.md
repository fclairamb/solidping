---
model: opus
effort: high
---

# Multi-step bulk checks (resumable slices with stored state), applied to website crawls

## Problem

Some checks are long jobs made of many short steps. A website crawl is the
first one: 300 pages at ~200 ms is a minute of work. Today every check is one
execution, so a crawl would have to run as a single job, which breaks three
things:

- **The time limit.** The global check timeout is 15 s
  (`checkworker/scheduling/scheduling.go`, `DefaultExecutionTimeout`). Raising it
  for one type via `BurstBudgeter` (`checkers/checkerdef/interface.go:101`)
  works for a 30 s ping burst, not for several minutes.
- **The runner pool.** A worker has 25 runners by default and slow jobs only get
  `poolSize − fastLaneReserved − busySlow` of them (`checkworker/worker.go:407`,
  `laneLimits`). A crawl holding one for minutes delays every browser and
  database check behind it, and a worker crash at page 280 throws the run away.
- **Billing.** The price axis is check executions (`ReserveCheckExecution`,
  `entitlements/service.go:315`, one token per call). A 300-page crawl must not
  cost the same as one HTTP probe.

The use case is the agency buyer comparing us with Oh Dear
(`wiki/competitors/ohdear.md`): broken links, mixed content and sitemap checks
over a whole site. It is our only real gap against them.

## Design summary

1. **Multi-step check.** A checker can opt into running as a series of
   **slices**. Each slice is an ordinary claimed, leased, time-limited job that
   does a bounded amount of work, saves its state and gives the runner back.
2. **State storage on the attachments rail**, like screenshot capture
   (`handlers/attachments`, spec 2026-08-21-01): the state is a file under topic
   `checks/<checkUid>/step-state`, stored through `filestorage`. `check_jobs`
   only holds small pointer columns, the same way it holds
   `capture_requested_at` / `capture_claimed_at` for "Capture now".
3. **A third lane, `bulk` (`lane = 2`).** Slices are claimed last, share the
   non-reserved part of the pool with the slow lane, are capped per worker and
   are never claimed ahead of time. Fast checks keep their full reservation.
   Slow checks wait at most one slice. The slice budget is our preemption.
4. **Work units.** Each slice reserves N tokens from the org's
   `maxChecksPerMinute` bucket. For the crawl, one page fetched = one unit.
5. **The `crawl` check type** is the first multi-step checker: broken internal
   and external links, mixed content, sitemap validity.

Multi-step checks run on **our own cloud workers only** in this spec. Private
agents are out of scope (see "Out of scope").

## Proposal

### Part 1: multi-step framework

#### 1.1 Checker interface

In `checkers/checkerdef/interface.go`, next to `BurstBudgeter` /
`ExtraBudgeter`, add an optional interface:

```go
// StepChecker is implemented by checkers that run as a series of bounded
// slices with persisted state (spec 2026-10-03-03).
type StepChecker interface {
    Checker
    // Step runs one slice. state is nil on a run's first slice. It must return
    // before budget elapses and must not use more than maxUnits work units.
    // When final is true the run is over (deadline or state cap reached): the
    // checker must return Done with a Result built from what it has.
    Step(ctx context.Context, spec *CheckSpec, in StepInput) (StepOutput, error)
}

type StepInput struct {
    State    []byte        // opaque, checker-owned; nil on the first slice
    Budget   time.Duration // wall-clock budget of this slice
    MaxUnits int           // work units reserved for this slice
    Final    bool          // finish now with what you have
    Previous map[string]any // Output of the previous completed run (for diffs), may be nil
}

type StepOutput struct {
    State    []byte         // next state; ignored when Done
    Done     bool
    Result   *Result        // set when Done
    Report   []byte         // optional full report (JSON) when Done
    Units    int            // units actually used
    Progress map[string]any // small, shown in the UI (e.g. pagesDone, queued)
}
```

`CheckTypeMeta` (`checkers/checkerdef/types.go`, the table around line 421)
gets a `MultiStep bool` field. A type with `MultiStep: true` must implement
`StepChecker` (enforced by a registry test).

#### 1.2 Migration

Append a section to the consolidated migration of the next unreleased release,
in **both** `db/postgres/migrations/` and `db/sqlite/migrations/` (today
`026_v0_38_0` if v0.38.0 has not shipped when this lands, otherwise a new
`027_v0_39_0`; follow the header convention in `026_v0_38_0.up.sql`):

```sql
--   SECTION: multistep-bulk-checks
alter table check_jobs add column if not exists step_run_uid        text;        -- uuid of the run in progress, NULL when idle
alter table check_jobs add column if not exists step_run_started_at timestamptz; -- start of the run (deadline + next-period anchor)
alter table check_jobs add column if not exists step_count          int not null default 0;
alter table check_jobs add column if not exists step_state_file_uid text;        -- files.uid of the CURRENT state, NULL on the first slice

create index if not exists idx_check_jobs_claim_bulk
  on check_jobs (region, effective_scheduled_at)
  where lane = 2 and lease_worker_uid is null;
```

- Mirror the predicate and column list of `idx_check_jobs_claim_fast/_slow`
  (`db/postgres/migrations/002_v0_2_0.up.sql:147-149`), not the sketch above,
  if they differ.
- No new table: run history is the `results` rows (one per completed run) plus
  the stored reports.
- Down migration drops the index and the four columns.
- `db/models/check_job.go`: add the four fields.

#### 1.3 State storage (attachments rail)

In `handlers/attachments/topics.go` and `service.go`:

- New kinds: `KindStepState = "step-state"` and `KindCrawlReport = "crawl-report"`.
  Topics: `checks/<checkUid>/step-state`, `checks/<checkUid>/crawl-report`.
- New `filestorage.GroupTypeCheckState = "check-state"`
  (`handlers/filestorage/filestorage.go:23-39`) for state files. Reports use the
  existing `GroupTypeReports`. Neither may land under `GroupTypeScreenshots`,
  which is what `put` hardcodes today (`service.go`, `put`): pass the group in.
- **State files are append-and-keep-2, not replace-on-write.** `Put` deletes the
  previous file before writing (`service.go:361`), which is unsafe here: a worker
  that dies after writing state N+1 but before releasing its lease would leave
  `step_state_file_uid` pointing at a deleted file. Use `appendCapped` with
  `keep = 2` and let the `check_jobs.step_state_file_uid` pointer decide which
  one is current. Extend the exception in `Put` (today only
  `isCheckScreenshotTopic`) to these kinds.
- Reports: `appendCapped`, keep the last 5 (same as `MaxCheckScreenshots`).
- `sniffMime` (`service.go:473`): `step-state` must parse as the envelope
  `{"v":1,"runUid":"…","checkType":"…","payload":…}`; `crawl-report` must parse
  as the report JSON. Anything else is refused (the function already fails
  closed on unknown kinds).
- State size cap: `MaxStepStateBytes = 1 MiB` (below `MaxAttachmentBytes`,
  4 MiB, `service.go:28`). A slice whose output state exceeds it is re-run as
  `Final` (see 1.5).
- The agent upload authorizer for `checks/` (`handler.go:66`,
  `NewCheckAuthorizer`) must **refuse** both new kinds: in this spec only the
  in-process worker writes them.

#### 1.4 Bulk lane

`checkworker/scheduling/scheduling.go`:

- Add `LaneBulk uint8 = 2`.
- `ClassifyLane` (`:138`): `if prevLane == LaneBulk { return LaneBulk }` first.
  Cost never moves a bulk job.
- New `Params` fields: `BulkLaneMax` (default 4 per worker) and
  `BulkSliceBudget` (default 10 s), from `scheduling.bulk_lane_max` and
  `scheduling.bulk_slice_budget_ms` in `config.SchedulingConfig`, with the
  manual `SP_SCHEDULING_*` env reads in `applySchedulingEnv` (koanf misses
  multi-word keys, see the existing `SP_SCHEDULING_COST_TIMEOUT_*`).

Lane at job creation: jobs of a `MultiStep` type are created with `lane = 2`
(see To verify for the creation site).

`checkworker/worker.go`:

- `laneLimits` (`:407`) returns three limits:

  ```
  slowLimit = clamp((poolSize − fastReserved) − busySlow − busyBulk, 0, free)
  bulkLimit = clamp(min(BulkLaneMax − busyBulk,
                        (poolSize − fastReserved) − busySlow − busyBulk − slowClaimed), 0, free − slowClaimed)
  fastLimit = free
  ```

  Bulk and slow share the non-reserved part of the pool; slow has priority;
  the fast reservation is untouched. Add `busyBulk atomic.Int32` next to
  `busySlow` (`:198`), with the same claim-time accounting.
- `fetchAndDistributeJobs` (`:631`) passes the bulk limit through.

`checkworker/checkjobsvc/service.go` (`ClaimJobs`, the per-lane SELECTs
around `:178-245` and the lane-filtered select around `:876`):

- Order in the claim transaction: **slow, then fast, then bulk.**
- The bulk SELECT uses `scheduled_at <= now`, **not** `now + maxAhead`: a slice
  must never sit parked on a runner waiting for its time.
- Both dialects (PG `FOR UPDATE SKIP LOCKED`, SQLite optimistic update).
- `WorkerBackend.ClaimJobs` (`checkworker/backend/backend.go:91-98`) gains the
  bulk limit. The agent claim path stays unchanged (no lane split there, and no
  multi-step jobs reach agents).

#### 1.5 Slice execution

In `executeJob` (`checkworker/worker.go:921`), when the checker implements
`StepChecker`:

1. **Start or resume.** If `step_run_uid` is NULL, start a run: new run uid,
   `step_run_started_at = now`, `step_count = 0`, state nil. Otherwise load the
   file named by `step_state_file_uid` through a new backend method
   `LoadStepState(ctx, job) ([]byte, error)` (DirectBackend reads it through the
   files service; WSBackend returns `ErrNotSupported`). A missing or corrupt
   state file restarts the run from nil state and logs a warning.
2. **Reserve units.** Replace the single `ReserveCheckExecution` call in
   `applyRateLimitGate` (`:1638`) for this path with a new
   `ReserveCheckExecutionsUpTo(ctx, orgUID, n) (granted int, err error)` in
   `entitlements/service.go`, with `n` = the checker's per-slice unit cap
   (crawl: 20). `granted == 0` follows the existing `deferRateLimited` path.
   Unused units are given back with `RefundCheckExecutions(ctx, orgUID, k)`.
3. **Decide `Final`.** `Final = true` when
   `now − step_run_started_at >= maxRunDuration` (per-check config, default
   30 min, max 2 h).
4. **Run** `Step` under a context of `BulkSliceBudget + 1 s`, the same margin
   rule as the global timeout (spec 2026-07-10-11).
5. **Not done:** write the state file (1.3), then call a new
   `SubmitStep(ctx, job, SubmitStepRequest{RunUID, StateFileUID, Units, Progress})`
   on the backend. It updates `step_state_file_uid`, increments `step_count`,
   sets `scheduled_at = effective_scheduled_at = now` and releases the lease, in
   one statement **fenced on `lease_worker_uid = <me> AND step_run_uid = <run>`**,
   so a worker whose lease expired cannot overwrite a newer slice. **No result
   row, no incident evaluation.** If the new state is over `MaxStepStateBytes`,
   do not save it: call `Step` once more with `Final = true` in the same slice.
6. **Done:** store the report (if any) under `checks/<uid>/crawl-report`, put
   its file uid in `Result.Output["reportFileUid"]`, then go through the normal
   `SubmitResult` (`backend/direct.go:221`). Extend `SubmitResultRequest`
   (`backend/backend.go:31`) with `StepRunUID *string`: when set, the same
   release clears the four `step_*` columns (fenced as above) and sets
   `NextScheduledAt` from `step_run_started_at` + period
   (`calculateNextScheduledAt`, `:1977`, must anchor on the run start, not on
   the last slice's `scheduled_at`).
7. **Errors.** A `Step` error or a timeout counts as one failed slice: keep the
   previous state, requeue. Three consecutive failed slices end the run with an
   error result (`saveErrorResult`, `:1589`) and clear the run.

On-demand runs ("Run now", the express path, `:716-776`): if no run is in
progress, start one now; if one is running, do nothing.

#### 1.6 Billing projection

`entitlements.ProjectChecksPerMinute` (`check_rate.go:189`) projects an org's
rate from check periods. Add an optional config interface
`UnitsPerRunHint() int` (like `MinPeriodHint`, `checkerdef/types.go:28`) so a
crawl counts `maxPages / period`, not `1 / period`. A daily 500-page crawl then
projects to 0.35 checks/min.

#### 1.7 API

- `GET /api/v1/orgs/:org/checks/:check/run` returns the run in progress:
  `{runUid, startedAt, steps, progress}`, or `{"running": false}`. `progress`
  comes from `SubmitStepRequest.Progress`, stored in the state file's `Details`
  (`files.WithDetails`) so no extra column is needed.
- `DELETE /api/v1/orgs/:org/checks/:check/run` cancels it: clears the `step_*`
  columns, purges the state files, schedules the next run normally.
- `GET /api/v1/orgs/:org/checks/:check/crawl-reports` lists the last 5 reports
  with signed download URLs, modelled on `handlers/checkscreenshots`.
- Register all three in `app/openapi_spec.go`.
- No change to check create/update: the config is the type's own JSON.

### Part 2: the `crawl` check type

New package `checkers/checkcrawl/` (`checker.go`, `config/`, `samples.go`,
`alias.go`, same layout as `checkhttp/`), registered in the type table
(`checkerdef/types.go`, around `:421`):

```go
{Type: CheckTypeCrawl, Labels: []string{labelSafe, labelStandalone, labelCatNetwork /* same as http */},
 Description: "Crawl a website for broken links, mixed content and sitemap errors",
 MinPeriod: time.Hour, DefaultPeriod: 24 * time.Hour, MultiStep: true},
```

#### 2.1 Config

| Field | Default | Bounds | Notes |
|---|---|---|---|
| `url` | required | http/https | Start page. Defines the crawled host. |
| `maxPages` | 200 | 1-2000 | Internal HTML pages fetched per run. |
| `checkExternalLinks` | true | | `HEAD` (fallback `GET` on 403/405), once per URL per run. |
| `checkMixedContent` | true | | Only on https pages. |
| `sitemap` | `"auto"` | `auto` / `off` / URL | `auto` = robots.txt `Sitemap:` lines, else `/sitemap.xml`. |
| `respectRobots` | true | | Disallowed paths are skipped, not reported. |
| `include` / `exclude` | none | ≤ 20 patterns each | Path prefixes or globs. |
| `concurrency` | 2 | 1-4 | Parallel requests per host. |
| `delayMs` | 250 | 0-5000 | Between requests to the same host. |
| `timeout` | 10 s | 1-30 s | Per request. |
| `maxRunDuration` | 30 min | 5 min-2 h | See 1.5. |
| `failOn` | `["broken_link","mixed_content_active","sitemap_error"]` | finding types | Which findings make the run `down`. |

`UnitsPerRunHint()` returns `maxPages`. Validation refuses a check bound to a
private location (multi-step runs on cloud workers only) and binds it to a
single region: a crawl from six regions costs six times as much and finds the
same links.

All outbound requests go through the worker's egress guard (`egressGuard`,
`worker.go`) and the shared HTTP client pool. User-Agent:
`SolidPing-Crawler/<version> (+https://solidping.io/bot)`.

#### 2.2 Crawl algorithm

- **Queue** starts with `url`, then the sitemap URLs (when enabled).
- **Internal** = same scheme-less host as `url` (www and apex are distinct
  unless `include` says otherwise). Only internal `text/html` pages are parsed
  and followed.
- Parse with `golang.org/x/net/html`'s tokenizer, collecting `a[href]`,
  `link[href]`, `script[src]`, `img[src|srcset]`, `iframe[src]`,
  `source[src|srcset]`, `video[src]`, `audio[src]`, `object[data]`,
  `form[action]`. Resolve against `<base>` and the page URL; drop fragments,
  `mailto:`, `tel:`, `javascript:`, `data:`.
- Redirects: follow up to 5. A redirect loop or a redirect chain ending in
  4xx/5xx is a broken link.
- Each page fetched costs one unit; external `HEAD`s cost one unit each.
- The slice stops when `Budget` or `MaxUnits` runs out and returns the state.
- The run is **deterministic given the same site**: the queue is FIFO and
  ordered by discovery, so slicing never changes the findings (tested).

#### 2.3 State payload (inside the envelope)

```json
{"queue":[{"u":"https://x/a","d":2,"src":"https://x/"}],
 "seen":"<base64 of sorted 8-byte URL hashes>",
 "sitemap":{"status":"done","urls":143,"errors":[]},
 "robots":{"fetched":true,"disallow":["/admin"]},
 "external":{"https://github.com/…":404},
 "findings":[…], "pages":87, "units":112}
```

`maxPages` bounds `seen` (2,000 × 8 bytes) and the queue; the external-result
cache and findings are capped at 2,000 entries each. That keeps the state well
under 1 MiB.

#### 2.4 Findings

| Type | Meaning | Default effect |
|---|---|---|
| `broken_link` | internal link → 4xx, 5xx, network error, redirect loop | down |
| `broken_external_link` | external link → 4xx, 5xx, network error | warning |
| `mixed_content_active` | `http://` script, stylesheet, iframe, object, form action on an https page | down |
| `mixed_content_passive` | `http://` img, audio, video, source on an https page | warning |
| `sitemap_error` | sitemap unreachable, invalid XML, or a listed URL not 2xx | down |

Each finding has `type`, `url`, `source` (the page it was found on), `status`
or `error`, and a `fingerprint = sha1(type|url|source)`.

#### 2.5 Result

- **Status:** `down` if any finding type in `failOn` is present; else
  `StatusWarning` (amber, counts as up, no incident; `checkerdef/expiry.go:9`)
  if there are other findings or the run was `Final` before the queue emptied;
  else `up`.
- **Output:** counts per type, the first 50 findings, `newFindings` (count, plus
  the first 20 of them) diffed by fingerprint against `Previous`, `pagesCrawled`,
  `incomplete`, `reportFileUid`. The previous run's fingerprints come from its
  report; read it in the server-side final submit path, not in the checker.
- **Metrics:** `pages_crawled`, `broken_links`, `broken_external_links`,
  `mixed_content`, `sitemap_errors`, `run_duration_ms`, `slices`.
- **Report:** the full findings list as JSON (1.3).

Incidents work as they do for any check: one opens while the status is `down`
and resolves when it is not. Notifications show the `newFindings` list rather
than every finding.

#### 2.6 Dashboard (`web/dash0`)

- Check form for `crawl` (fields from 2.1, with advanced options folded).
- Check detail: a progress line while a run is in progress (pages done / max,
  started at, Cancel), a findings table grouped by type (new ones flagged), and
  "Download report" links for the last 5 runs.
- EN and FR strings.

## Tests

- `checkworker/scheduling/scheduling_test.go`: `ClassifyLane` keeps
  `LaneBulk` at every cost; `laneLimits` with bulk: the fast reservation is
  never reduced (property test over pool/reserved/busy values), bulk never
  exceeds `BulkLaneMax`, bulk + slow never exceed `poolSize − fastReserved`,
  slow takes slots before bulk.
- `checkworker/checkjobsvc/service_test.go` (PG and SQLite): a bulk job due in
  1 s is **not** claimed while a fast job due in 1 s is (claim-ahead);
  claim order slow → fast → bulk; bulk limit respected under `SKIP LOCKED`
  races.
- `checkworker/worker_test.go` (step path, with a fake `StepChecker`):
  - partial slice: no `results` row, state file written, `step_count`
    incremented, `scheduled_at == now`;
  - final slice: one `results` row, report stored, `step_*` columns cleared,
    next run anchored on `step_run_started_at + period`;
  - crash after the state write, before `SubmitStep`: the next claim resumes
    from the previous state file (keep-2 retention);
  - stale worker: a `SubmitStep` after its lease expired and another worker
    advanced the run changes nothing (fencing);
  - state over 1 MiB → same slice re-run as `Final`, run ends `incomplete`;
  - deadline reached → `Final = true` passed to the checker;
  - three failing slices → error result, run cleared;
  - `granted == 0` units → deferred via `deferRateLimited`, no slice runs;
    unused units refunded.
- `entitlements/service_test.go`: `ReserveCheckExecutionsUpTo` grants
  `min(n, available)`, 0 when empty, unlimited cap grants `n`; refunds never
  exceed the burst cap.
- `handlers/attachments/service_test.go`: `step-state` refuses non-envelope
  JSON; state keeps exactly 2 files; `crawl-report` keeps 5; neither lands in
  the screenshots group.
- `handlers/attachments/handler_test.go`: an agent upload to
  `checks/<uid>/step-state` or `crawl-report` is refused.
- `checkers/checkcrawl/checker_test.go` (httptest site):
  - internal 404, 500, redirect loop → `broken_link`;
  - external 404 → `broken_external_link`, status warning;
  - active vs passive mixed content on https; none on an http page;
  - sitemap: missing (auto mode, no error), invalid XML, a listed URL returning
    404, robots.txt `Sitemap:` line used;
  - robots `Disallow` respected; `exclude` respected;
  - other-host links never followed; `maxPages` stops the crawl;
  - **slicing invariance:** the same site crawled with `MaxUnits = 1` per slice
    and with one big slice yields identical findings;
  - `newFindings` diff against a previous output.
- `handlers/checks` validation: `crawl` with period < 1 h refused; bound to a
  private location refused; `maxPages` 0 or 2001 refused.
- Migration up/down on both dialects (the existing migration tests).
- `app/openapi_spec_test.go`: the three new routes are documented.

## To verify

- Where `check_jobs` rows are created and how their region set is chosen, to
  set `lane = 2` for `MultiStep` types and pin them to one region.
- Whether the existing lease release (`ReleaseLeaseWithSchedulingState`) is
  already fenced on `lease_worker_uid`; if so, `SubmitStep` reuses it.
- Exact names and lines of the per-lane claim functions in
  `checkjobsvc/service.go` (`:178-245`, `:876` at the time of writing).
- The predicate of `idx_check_jobs_claim_fast/_slow`
  (`002_v0_2_0.up.sql:147-149`) and whether migration 009 later replaced them
  (comment at `checkjobsvc/service.go:855` says "migration 009").
- How `ProjectChecksPerMinute` (`entitlements/check_rate.go:189`) reads periods,
  to plug `UnitsPerRunHint` in.
- The `web/dash0` check-detail and check-form components to extend.
- Whether the express path (`worker.go:716-776`) can start a run on a job whose
  lane is bulk.

## Open questions

1. **Should `broken_external_link` make the check `down`?** Recommended: no,
   warning by default. External sites break all the time and the user can't fix
   them; `failOn` lets them opt in.
2. **Is `maxPages` capped per plan?** Recommended: one fixed ceiling (2000) for
   now. The work-unit charge already makes big crawls cost more within the
   checks-per-minute budget. A per-plan entitlement can come later if COGS says
   so.
3. **Respect robots.txt by default?** Recommended: yes, with an opt-out. It is
   usually their own site, but agencies often crawl staging sites with
   `Disallow: /`, so the opt-out matters.

## Resolved open questions

Answered unattended on 2026-10-03 with the spec's recommended answers; to be reviewed by the owner.

1. `broken_external_link` does not make the check `down`: it is a warning by default. `failOn` lets the user opt in to `down`.
2. `maxPages` has one fixed ceiling of 2000 for every plan. No per-plan entitlement in this spec.
3. Respect robots.txt by default, with a per-check opt-out.

## Out of scope

- Multi-step checks on private agents. It needs the state shipped with the
  claim (a signed download URL from `handlers/files/signedurl`) and a push
  upload for state, since today's agent upload is server-requested
  (`checkworker/backend/ws_capture.go`).
- A cross-run cache of external link results.
- Lighthouse / page-load timings, port scans and other future multi-step types.
  The framework allows them; this spec ships only `crawl`.
- JavaScript-rendered pages (the crawler parses server HTML only).
