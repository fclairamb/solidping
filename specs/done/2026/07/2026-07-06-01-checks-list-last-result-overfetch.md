# Checks list: `with=last_result` over-fetches at both the DB and wire layers

## Problem

The checks list page (`/orgs/$org/checks`) requests
`GET /api/v1/orgs/$org/checks?with=last_result&…&limit=20` and gets a 24.5KB
response for 20 checks (HAR capture `checks.priv.har`, 2026-07-06). Of that,
~11.8KB (48%) is `lastResult.output` + `lastResult.metrics` — SSL cert chains
run ~1.7KB per check, DNSBL blocklist details ~200B — none of which the list
renders. The page consumes exactly two fields from `lastResult`:

- `status`, only as a fallback for the synthesized `check.status`
  (`web/dash0/src/routes/orgs/$org/checks.index.tsx:157`, `:180`)
- `durationMs` for the Response time column (`checks.index.tsx:183-184`)

The wire payload is the visible half. The invisible half is worse: the DB
query behind `with=last_result` is unbounded.

### Root cause 1 (the actual "enormous data"): `GetLastResultForChecks` loads every raw row

Both backends fetch **all** raw results for the requested checks, sort them,
and discard everything but the newest per check in Go:

- `server/internal/db/postgres/postgres.go:1875-1903` — the comment says
  "Use DISTINCT ON to get the latest result per check_uid" but the query has
  no `DISTINCT ON`, no `LIMIT`, and no window function. It selects every
  `period_type = 'raw'` row for the given `check_uid`s (full rows, including
  the `output`/`metrics` JSONB), orders by `check_uid, period_start DESC`,
  and keeps the first per check in a Go loop.
- `server/internal/db/sqlite/sqlite.go:1786-1813` — same shape ("we need to
  use a subquery" says the comment; there is no subquery).

With the default raw retention of 24h
(`server/internal/config/config.go:696`, `RetentionRaw: 24`) a 1-minute
check accumulates up to 1,440 raw rows per region. A 20-check list page can
therefore pull tens of thousands of rows — each carrying JSONB blobs — from
the DB into the server per page view, to keep 20 of them.

The same helper serves the check **detail** endpoint
(`server/internal/handlers/checks/service.go:1140`), and the detail page
re-fetches the check with `lastResult` on its polling interval
(`checks.$checkUid.index.tsx:442`) — so a single open detail tab on a
1-minute check re-reads ~1,440 rows every poll.

Note the query also omits an `organization_uid` filter, while the covering
partial index is
`results_raw_idx (organization_uid, check_uid, period_start desc) where period_type = 'raw'`
(`server/internal/db/postgres/migrations/001_v0_1_0.up.sql:455`) — so even
the fixed query should add the org filter to ride the index.

### Root cause 2: list responses serialize the full `LastResultResponse`

`LastResultResponse` (`server/internal/handlers/checks/service.go:648-655`)
always carries `output` and `metrics`. That is right for the detail endpoint
— the detail page renders both, plus the SSL-chain card
(`checks.$checkUid.index.tsx:1031-1105`) — but no list consumer touches
them:

| Consumer | Fields of `lastResult` actually used |
|---|---|
| `checks.index.tsx` (list page) | `status` (fallback), `durationMs` |
| `dashboard-page.tsx:89,482` | `status` (fallback), presence check |
| `status-dashboard.tsx:48-49,103` | `status`, `durationMs` |
| `checks.$checkUid.index.tsx` (detail) | everything — **keep full** |

## Considered: `with=cost_ewma` from `check_jobs` (proposal that triggered this spec)

The idea: have the list stop asking for `last_result` and instead ask for an
aggregate of `check_jobs.cost_ewma_ms` (the per-region smoothed execution
cost, `server/internal/db/models/check_job.go:38`). The instinct is right —
`check_jobs` is one tiny indexed row per check×region, far cheaper than a
latest-row lookup on the `results` time series, and `check.status` is
already synthesized on the check row so the list needs nothing else from
`results`.

But it is not a drop-in replacement:

- **Semantics change.** `cost_ewma_ms` is scheduler telemetry: an EWMA that
  lags reality, is **pinned to the timeout ceiling** on timeouts (one
  timeout inflates the value for many runs), and is 0 until the first run.
  Rendering it in a column users read as "last response time" would be
  misleading; the column would have to be relabeled (e.g. "Avg duration").
- **A precedent already exists.** Spec 2026-07-01-04 (D3) exposed exactly
  this value on the **detail** response as `scheduling.costEwmaMs`, using
  **max across regions** (`service.go:625-638`), and deliberately kept it
  off list responses. Adding a second, average-flavored exposure of the
  same number would give the API two aggregation rules for one metric.
- **It doesn't fix the bug.** The dashboard and status-dashboard still need
  `with=last_result`, so `GetLastResultForChecks` must be fixed regardless —
  and once it is (parts A+B), the residual cost of `last_result` on a list
  is one slim indexed row per check, making the swap a product choice, not
  a performance necessity.

Verdict: keep it as an **optional** part C — expose the existing
`scheduling` block on lists behind `with=scheduling` (same max-across-regions
rule as D3, not a new average) — and only flip the list column to it if we
decide we prefer a smoothed duration over the last measured one.

## Proposal

### A. Fix `GetLastResultForChecks` to fetch one row per check (both backends)

- Postgres: `DISTINCT ON (check_uid) … ORDER BY check_uid, period_start DESC`.
- SQLite: window function (`ROW_NUMBER() OVER (PARTITION BY check_uid ORDER BY period_start DESC)`,
  keep `rn = 1`) or a correlated subquery — SQLite ≥ 3.25 supports window
  functions, and per the sync-pg-to-sqlite convention the two
  implementations must stay behaviorally identical.
- Add the `organization_uid` predicate (callers have the org in hand) so the
  query is covered by `results_raw_idx`.
- Regression test: seed several raw rows per check across two checks, assert
  the map returns exactly the newest row per check — and (Postgres) assert
  the query no longer returns O(retention) rows, e.g. via a row-count probe
  or by asserting on a check with many results.

### B. Slim `lastResult` in **list** responses

- In `ListChecks` serialization only, populate `lastResult` as
  `{uid, status, timestamp, durationMs}` — omit `output` and `metrics`.
  Detail responses (`GetCheck`) keep the full object.
- Update `server/internal/app/openapi/openapi.yaml` (list schema notes
  `output`/`metrics` are detail-only), `wiki/api-specification.md`, and
  regenerate the client (`make generate`) if the schema split requires it.
- Expected effect on the captured HAR request: 24.5KB → ~12.2KB (measured on
  the capture by stripping the two fields), before gzip.

### C. (Optional — product decision, not required for the fix)

- Accept `with=scheduling` on the list endpoint, reusing
  `CheckSchedulingResponse` and D3's max-across-regions aggregation.
- If adopted, the list page may switch its duration column to
  `scheduling.costEwmaMs` (relabeled "Avg duration") and drop
  `last_result` from its request entirely, making the list results-free.
- If C is skipped, nothing changes for the frontend: A+B are transparent.

## Out of scope

- Changing the dashboard / status-dashboard queries — they benefit from A+B
  automatically.
- `last_status_change` (`GetLastStatusChangeForChecks`) — different query,
  worth its own look, not this spec.
- The `results` API endpoints — already field-gated via their own `with`.

## Acceptance criteria

- Listing 20 checks issues a last-result query that returns ≤ 1 row per
  check (verified by test), on both Postgres and SQLite.
- List responses with `with=last_result` no longer contain
  `lastResult.output` / `lastResult.metrics`; detail responses still do.
- Checks list page, org dashboard, and status dashboard render unchanged
  (status dots/badges + response-time column still populated).
- Check detail page still renders Output, Metrics, and the SSL-chain card.
- OpenAPI spec and `wiki/api-specification.md` reflect the list/detail
  difference.
- `make test`, `make lint`, `make test-dash` green.

## Implementation plan

Part C is explicitly skipped — optional per the spec's own text, no
acceptance criterion depends on it.

- [x] A: rewrite `GetLastResultForChecks` in `postgres.go` using
      `DistinctOn("check_uid") + Order("check_uid", "period_start DESC")`
      plus an `organization_uid = ?` predicate (rides `results_raw_idx`).
- [x] A: rewrite `GetLastResultForChecks` in `sqlite.go` using the same
      `ROW_NUMBER() OVER (PARTITION BY check_uid ORDER BY period_start DESC)`
      CTE pattern already used by `GetLastStatusChangeForChecks` in both
      files (raw SQL via `s.db.NewRaw(...).Scan(...)`), `rn = 1`, plus the
      `organization_uid = ?` predicate.
- [x] A: update the `db.Service` interface
      (`internal/db/service.go:181`) to add an `orgUID string` parameter;
      thread it through the three call sites:
      `handlers/checks/service.go:774` (`ListChecks`, has `org.UID` in
      scope), `handlers/checks/service.go:1140` (`GetCheck`, has `org.UID`),
      and `checkworker/worker.go:1186` (`executePassiveJob`, has
      `checkJob.OrganizationUID`) — this third call site was not named in
      the spec body but implements the same interface method and must move
      in lockstep.
- [x] A: update the `mockDBService` in
      `internal/notifications/slack_test.go:764` to match the new
      3-arg signature (currently unused/panics — mechanical fix only).
- [x] A: regression tests: `internal/db/postgres/last_result_test.go` and
      `internal/db/sqlite/last_result_test.go` — seed several raw rows
      across two checks (+ a decoy org to prove the org filter is applied),
      assert exactly one row per check comes back and it's the newest one.
- [x] B: `CheckResponse.LastResult` stays `*LastResultResponse` (the field
      and JSON key are unchanged in both paths). `GetCheck` keeps calling
      `convertResultToLastResultResponse` (full, unchanged). `ListChecks`
      now calls a new `convertResultToLastResultResponseSlim`, which
      populates the same `*LastResultResponse` struct but leaves
      `Output`/`Metrics` nil — both fields are already `json:",omitempty"`,
      so they're absent from the wire payload, not just null. No new Go
      type was needed; the status-int→string conversion was factored into
      a shared `resultStatusString` helper used by both conversions.
- [x] B: handler/service tests asserting list vs detail payload shapes
      (`handlers/checks/handler_test.go`
      `TestLastResultListVsDetailShape` — list JSON has no `output`/`metrics`
      keys at all; detail JSON does).
- [x] B: updated `openapi.yaml` (added `LastResultListItem` + `CheckListItem`
      schemas so `CheckListResponse.data[]` documents the slim shape; also
      added the previously-undocumented `durationMs` to `LastResult`) +
      `wiki/api-specification.md`. `make generate` (oapi-codegen client
      regen under `server/pkg/client`) could NOT be run — it fails with a
      pre-existing, unrelated toolchain error (yaml-jsonpath v0.3.2 vs this
      module's yaml.v3/yaml.in-v4 versions), reproduced identically against
      the original openapi.yaml on `main` before this change. The stale
      generated client stays wire-compatible (extra/missing fields are
      additive/optional), so `server/pkg/cli` and integration tests are
      unaffected — but the client's typed `LastResult` struct itself is not
      regenerated to reflect the split. Flagging this as a known gap.
- [x] QA: `make build-backend lint-back test` (the coordinator's assigned
      gate for this backend-only spec, not `make lint`/`make test-dash`
      which target the legacy `web/dash`). Confirmed via a research
      subagent that frontend consumers (`checks.index.tsx`,
      `dashboard-page.tsx` under `components/dashboard/`,
      `status-dashboard.tsx` under `components/shared/`,
      `checks.$checkUid.index.tsx`) only read fields kept in the slim
      shape — no frontend code changes made.
