# Check detail: Recent Results never shows the region

## Problem

On the check detail page (`/orgs/$org/checks/$checkUid`), the "Recent
Results" table has a Region column that always renders "-", even for checks
probed from several regions (e.g. `default`, `us-1`, `eu-2`). Since regions
have very different baseline latencies (adjacent rows alternating 68ms and
730ms), the Duration column is impossible to interpret without knowing which
region produced each row.

The data exists end to end; only the fetch omits it:

1. **Raw results persist the region at insert time** — the worker copies it
   from the job on both the success and error paths
   (`server/internal/checkworker/worker.go:868`, `:930` —
   `Region: checkJob.Region`). Single-worker dev setups register with region
   `default` (`worker.go:269-272`), so even local rows carry a value.
   Aggregated rollups are per-region too (one row per period × region).
2. **The results API returns `region` only when explicitly requested** via
   the `with` query param
   (`server/internal/handlers/results/service.go:309-311`:
   `if withSet["region"] { resp.Region = result.Region }`). The OpenAPI
   schema documents the field as "Region identifier (with=region)"
   (`server/internal/app/openapi/openapi.yaml:2483`).
3. **The page requests `with: "durationMs"` without `region`**
   (`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:338-343`), so
   `result.region` is always `undefined` and the cell fallback renders "-"
   (`checks.$checkUid.index.tsx:975-977`).

The Response Times chart on the same page already requests
`with: "durationMs,region"`
(`web/dash0/src/components/checks/response-time-chart.tsx:293`) — that is how
its "Showing all regions (…)" subtitle knows the region list — confirming the
field flows fine when asked for.

## Proposal

1. **Request the field**: change the Recent Results `useResults` call to
   `with: "durationMs,region"` (`checks.$checkUid.index.tsx:341`).
2. **Render it the way regions are rendered elsewhere on the page**: the
   Configuration card maps a region slug to its definition and shows
   `${emoji} ${name}` in an outline `Badge`, falling back to the raw slug
   when no definition matches (`checks.$checkUid.index.tsx:773-779`). Reuse
   that mapping for the Region cell — `regionsData` from `useRegions(org)` is
   already fetched on this page (`:347`). Keep "-" for rows without a stored
   region (legacy data).

No backend change: the endpoint, the OpenAPI schema, and the `OrgResult`
type (`web/dash0/src/api/hooks.ts:144`) already declare the field.

## Out of scope

- Region selection on the Response Times chart → spec **2026-07-05-03**.
- The pinned chart tooltip box shows the raw slug
  (`web/dash0/src/components/checks/pinned-result-box.tsx:137-140`), as does
  the result detail page; aligning them on emoji+name is possible polish,
  not required here.

## Acceptance criteria

- On a multi-region check, each Recent Results row shows the region the
  result ran from (emoji + name when a definition exists, otherwise the
  slug).
- Local dev (single worker): rows show the default region instead of "-".
- Rows whose stored region is null keep showing "-".
- No additional HTTP request — same results call, one more `with` token.
- e2e `web/dash0/e2e/check-detail.spec.ts` asserts the Region cell is
  populated once a result lands.
- `make lint` and `make test-dash` green (no new eslint errors).

## Implementation plan

- [ ] Add `region` to the `with` param of the Recent Results `useResults`
      call.
- [ ] Region cell: map slug → emoji+name via `regionsData` (outline Badge,
      slug fallback, "-" when absent).
- [ ] Extend `check-detail.spec.ts` with the region-cell assertion.
- [ ] Run `make lint` + `make test-dash`.

## Implementation Plan

1. **`checks.$checkUid.index.tsx:341`** — change the Recent Results
   `useResults(org, {...})` call's `with` from `"durationMs"` to
   `"durationMs,region"`. Zero new HTTP requests (same call, one more `with`
   token), satisfying the "no additional HTTP request" acceptance criterion.

2. **Region cell (`checks.$checkUid.index.tsx:975-977`)** — replace
   `{result.region || "-"}` with the same slug → definition lookup already
   used for the Configuration card's regions badges (lines 773-779):
   `regionsData?.regions?.find((r) => r.slug === result.region)`. Render an
   outline `Badge` with `${region.emoji} ${region.name}` when a definition
   matches, the raw `result.region` slug when it doesn't (unknown/removed
   region), and `"-"` only when `result.region` itself is falsy/undefined
   (legacy rows with no stored region). `regionsData` is already in scope
   from the existing `useRegions(org)` call at line 347 — no new fetch.

3. **e2e (`web/dash0/e2e/check-detail.spec.ts`)** — extend the existing
   "should display summary cards" test (or add a small new test) to wait for
   a Recent Results row (`[data-testid^="result-row-"]`) and assert its
   Region cell renders something other than `"-"` once a result has landed.
   Local/test workers register under region `default`, and `useRegions`
   seeds a `default` region definition, so the cell should render a Badge
   (emoji + "Default" or equivalent) — not the literal fallback dash.

4. **QA** — `make build-dash0`, `cd web/dash0 && bun run lint` (no new
   errors in the touched file), attempt `make test-dash` /
   `bunx playwright test check-detail.spec.ts` locally; if the local
   devloop isn't in `SP_RUNMODE=test` (login 401s), report the e2e test as
   authored-but-not-run rather than skipping it.
