# Fix discovery host promotion (missing Outlet) + promote e2e test

## Context

Discovery host promotion is reported as "doesn't work at all" — navigating to
`/dash0/orgs/<org>/discovery/<jobUid>/<hostUid>/promote` shows the scan-detail page,
never the promote form.

Root cause (verified): the promote route file
`web/dash0/src/routes/orgs/$org/discovery.$jobUid.$hostUid.promote.tsx` nests, via
TanStack flat file-routing, under the scan-detail route `discovery.$jobUid.tsx`. That
parent component `ScanDetailPage` (`discovery.$jobUid.tsx:125`) imports only `Link`
and **never renders an `<Outlet />`**, so the child promote route has nowhere to mount.
Visiting the promote URL just re-renders the scan detail. The sibling discovery layout
`discovery.tsx:8` correctly renders `<Outlet />`; the `$jobUid` route was never given
the same treatment.

The backend promote endpoint (`POST /orgs/:org/discovery/hosts/:uid/promote`,
`server/internal/handlers/discovery/handler.go:224`) and its `PromoteHost` service are
correct and already covered by `service_test.go` / `handler_test.go`. The failure is
purely frontend. No existing test promotes a host —
`web/dash0/e2e/discovery.spec.ts` only covers navigation and the scan form — so the
regression slipped through.

Intended outcome: the promote form renders again, a host can be promoted into a check
end-to-end, and a Playwright test guards the flow so it can't silently break again.

## Honest opinion (recorded at planning time)

- **Fix the routing by splitting into layout + index**, mirroring the existing
  `discovery.tsx` / `discovery.index.tsx` pattern, rather than dropping an `<Outlet />`
  into `ScanDetailPage`. The latter would render the promote form *below* the full host
  table; the split makes promote a clean standalone page and matches the established
  convention.
- **Seed the promotable host; don't drive a scan.** In CI/test mode a CIDR or Freebox
  scan can't find deterministic hosts (Freebox needs a paired channel that doesn't exist
  in test mode), so the e2e test needs data inserted by the test seeder. This is the only
  reliable, network-free way to exercise the real promote flow.
- **Seed with a TCP suggested check carrying `{host, port}`.** `buildCheckConfig`
  (`service.go:441`) copies a matching suggestion's config into the new check, so a
  seeded `{"type":"tcp","config":{"host":"127.0.0.1","port":8080}}` promotes cleanly and
  validates. This avoids the separate manual-fallback bug (below).

## Goal

- The promote page renders at `/orgs/$org/discovery/$jobUid/$hostUid/promote` and submits
  successfully, creating a check from a discovered host.
- Test mode seeds one deterministic, unpromoted discovered host (with a scan) so the flow
  is exercisable in CI.
- A Playwright e2e test promotes that host and asserts the resulting check exists — and,
  by reaching the promote form at all, guards against the Outlet regression.

## Non-goals

- **Manual-fallback promote path is out of scope** (explicit scope decision). Promoting a
  host with an empty `suggestedChecks[]` via the manual type `Select`
  (`discovery.$jobUid.$hostUid.promote.tsx:211-227`) currently sends no config, so the
  backend rejects it (TCP needs a port; dns/ssl/http need their own fields) → 500. Fixing
  it requires per-type config inputs in the form. Flag as a follow-up; do not fix here.
- No change to the discovery scan engine, `suggestedChecks` generation, or the promote
  backend handler/service.
- No transactional rework of multi-check creation (pre-existing, `service.go:330`).
- No DB schema/migration changes.

## Frontend (routing fix)

Split the `$jobUid` route into a layout + index so children get an outlet, matching
`discovery.tsx` + `discovery.index.tsx`.

`web/dash0/src/routes/orgs/$org/discovery.$jobUid.index.tsx` (new)
- Move the entire current `ScanDetailPage` implementation (and `HostRow`,
  `statusBadgeVariant`) here. Route path: `createFileRoute("/orgs/$org/discovery/$jobUid/")`.

`web/dash0/src/routes/orgs/$org/discovery.$jobUid.tsx` (reduce to layout)
```tsx
import { createFileRoute, Outlet } from "@tanstack/react-router";
export const Route = createFileRoute("/orgs/$org/discovery/$jobUid")({
  component: () => <Outlet />,
});
```

`routeTree.gen.ts` regenerates automatically via the TanStack Router vite plugin on
dev/build — do not hand-edit it.

## Backend (seed a promotable host in test mode)

`server/test/testdata/testdata.go` — `CreateTestData` (line 16) is the `SP_RUNMODE=test`
seeder (DB reset via `SP_DB_RESET`). It is idempotent and currently seeds only
orgs/users/memberships/tokens. After the test org `...0001` is created, add a helper that
inserts (via `dbService.DB().NewInsert()`, consistent with the existing direct-DB usage):

- A discovery **job** (`models.Job`, fixed UID `00000000-0000-0000-0000-000000000007`,
  `OrganizationUID = &testOrgUID`, `Type = string(jobdef.JobTypeNetworkDiscovery)` =
  `"network_discovery"`, `Status = models.JobStatusSuccess`). Insert first —
  `discovered_hosts.job_uid` is `notnull`.
- A **discovered host** from
  `models.NewDiscoveredHost(testOrgUID, jobUID, "127.0.0.1", models.DiscoverySourceLAN)`
  (`server/internal/db/models/discovered_host.go:40`), fixed UID
  `00000000-0000-0000-0000-000000000008`, `ICMPReachable = true`, `OpenPorts = [8080]`,
  `PromotedToCheckUID = nil`, and
  `SuggestedChecks = [{"type":"tcp","config":{"host":"127.0.0.1","port":8080}}]`.

Fixed UIDs let the test navigate deterministically. New import: `jobdef`
(`server/internal/jobs/jobdef`). `ListScans` filters to `network_discovery` /
`freebox_lan_discovery` job types (`handler.go:162`), so the seeded scan appears in the
list and at `GET /scans/:jobUid`; `ListHosts?jobUid=` returns the seeded host.

## Tests

`web/dash0/e2e/discovery-promote.spec.ts` (new; or extend `discovery.spec.ts`). Follow
existing conventions: inline `beforeEach` login as `test@test.com` / `test`, org `test`
(`discovery.spec.ts:4-11`); select by role/label/text (no testids on promote controls).

1. `goto("/dash0/orgs/test/discovery/00000000-0000-0000-0000-000000000007")`.
2. Assert the host row shows `127.0.0.1` with a "Pending" (not "Promoted") status.
3. Click the promote control (`getByRole("link", { name: /promote/i })`; title is i18n
   `promote` = "Promote to check").
4. Assert the promote page renders (heading `promoteTitle` = "Promote to Check") — the
   Outlet regression guard.
5. Name (`#name`) is prefilled; the TCP suggested-check checkbox (`aria-label="tcp"`) is
   pre-ticked. Submit via the "Create checks" button (i18n `createChecks`).
6. Assert navigation back to the scan and a success toast ("Host promoted to check").
7. Assert the created check exists — navigate to `/dash0/orgs/test/checks` and find it by
   name, or re-open the scan and assert the host now shows the "Promoted" badge.

Regression: the seeded scan adds one row to the discovery scan list; confirm existing
`discovery.spec.ts` tests don't assert an empty list (they create their own scan) and
re-run that suite.

## Files to create / modify

New:
- `web/dash0/src/routes/orgs/$org/discovery.$jobUid.index.tsx`
- `web/dash0/e2e/discovery-promote.spec.ts`

Modified:
- `web/dash0/src/routes/orgs/$org/discovery.$jobUid.tsx` (reduce to `<Outlet />` layout)
- `server/test/testdata/testdata.go` (seed discovery job + promotable host)

## Verification

- Backend: `make build` then `make test` (seeder compiles; Go tests pass).
- Manual (`make dev-test`, `SP_RUNMODE=test`): open
  `http://localhost:4000/dash0/orgs/test/discovery/00000000-0000-0000-0000-000000000007`,
  confirm the promote page now renders and promotion creates a check.
- Playwright: from `web/dash0`, against a running dev-test server
  `bun run test:e2e:dev e2e/discovery-promote.spec.ts`; or full self-contained
  `bun run test:e2e e2e/discovery-promote.spec.ts`. Also run
  `bun run test:e2e:dev e2e/discovery.spec.ts` for regression.
- `make fmt && make lint`.

## Risk log

| Risk | Mitigation |
|---|---|
| Splitting `$jobUid` breaks the existing scan-detail render | Index route keeps path `/discovery/$jobUid/`; `ScanDetailPage` moves verbatim. Covered by re-running `discovery.spec.ts`. |
| Seeded scan perturbs other tests asserting an empty discovery list | Existing discovery tests create their own scan and don't assert emptiness; re-run the suite to confirm. |
| Seeded host promotes into a real check the in-process worker tries to run | Harmless in test mode (a TCP check on 127.0.0.1:8080); does not affect assertions. |
| `discovered_hosts.job_uid` FK / notnull | Insert the job row before the host. |
| Manual-fallback promote still 500s | Documented as an out-of-scope follow-up; the seeded host uses the working suggested-check path. |

## Implementation Plan

1. **Routing** — Create `discovery.$jobUid.index.tsx` with the current `ScanDetailPage`
   (+ `HostRow`, `statusBadgeVariant`); reduce `discovery.$jobUid.tsx` to an `<Outlet />`
   layout. Let the router plugin regenerate `routeTree.gen.ts`.
2. **Seed** — Add a `createTestDiscoveryData` helper in `testdata.go`, called from
   `CreateTestData` after the test org: insert the `network_discovery` job (`...0007`) then
   the discovered host (`...0008`) with a TCP suggested check. `make build && make test`.
3. **e2e** — Add `discovery-promote.spec.ts`: navigate to the seeded scan, open promote,
   submit, assert success + created check.
4. **QA** — `make fmt && make lint`; run the new spec and `discovery.spec.ts`; manual
   `make dev-test` smoke; completeness audit, then archive the spec to
   `specs/done/2026/05/`.
