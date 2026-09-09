---
model: opus
effort: high
---

# A resolved incident publication is still rendered in the public status page's active section

## Problem

`web/status0/e2e/incident-publications.spec.ts:152` — *"resolving a publication
removes it from the active section"* — fails deterministically at
[`incident-publications.spec.ts:177`](web/status0/e2e/incident-publications.spec.ts:177):

```
await expect(page.locator(`#incident-${publication.uid}`)).toHaveCount(0);
// Expected: 0, Received: 1
```

The test creates a status page and a publication, `PATCH`es it to
`state: "resolved"`, reloads `/s/test`, and expects the incident to be gone from
the active section — reachable only from the collapsed history panel.

### What is already established — do NOT re-derive it

- **Not caused by the `/dash0` → `/d` base-path move.** A binary built from
  `b2c057a85` (before that batch), run in a throwaway worktree against a fresh
  `SP_RUNMODE=test` Postgres database, fails **identically**. This is an older
  bug.
- **Not a flake and not accumulated state.** It reproduces with `--workers=1`
  against a fresh database.
- **Nobody noticed because CI runs 3 of the 13 `web/status0/e2e` spec files** —
  `translate-resilience`, `response-time-chart`, `dark-mode` (see
  [`.github/workflows/ci.yml`](.github/workflows/ci.yml) around line 454 and the
  comment block that justifies the selection). `incident-publications.spec.ts`
  has never run in CI.

### What the code says so far (starting points, not conclusions)

The obvious backend suspects look *correct* on a first read, which is why this
spec is `opus`/`high` rather than a one-liner:

- `PATCH /api/v1/orgs/:org/status-pages/:page/incidents/:uid` →
  `UpdatePublication` ([`service.go:628`](server/internal/handlers/incidentpublications/service.go:628))
  writes `public_state = resolved` **and** stamps `resolved_at`.
- The active feed is
  `ListPublicIncidents(ctx, page, activeOnly=true)`
  ([`service.go:1271`](server/internal/handlers/incidentpublications/service.go:1271)),
  which sets `ActiveOnly` on the filter; both DB backends translate that to
  `public_state <> 'resolved'`
  ([postgres:88](server/internal/db/postgres/incident_publication.go:88),
  [sqlite:88](server/internal/db/sqlite/incident_publication.go:88)).
- The frontend renders exactly what the payload hands it: `activeIncidents` →
  `<ActiveIncidents>` → `IncidentCard`, whose `id={`incident-${uid}`}` is the
  locator the test uses
  ([`active-incidents.tsx:57`](web/status0/src/components/shared/active-incidents.tsx:57),
  [`status-page-view.tsx:325`](web/status0/src/components/shared/status-page-view.tsx:325)).
  There is no client-side merge of history into the active list.

**Leading hypothesis (must be confirmed or killed first, with evidence):** the
public status-page payload is served **`Cache-Control: public, max-age=60`**
(spec 2026-08-22-06 — see
[`cache_control_test.go:130`](server/internal/handlers/statuspages/cache_control_test.go:130)
and [`summary_test.go:90`](server/internal/handlers/statuspages/summary_test.go:90)).
A Playwright `page.reload()` is a *normal* reload, so the browser is entitled to
replay the still-fresh JSON — including the pre-resolution `activeIncidents` —
without ever hitting the server. If that is what happens, the product is behaving
as designed and the **test's premise is wrong**: it asserts a server-truth
transition through a client that is allowed to answer from cache.

Alternatives that must be ruled out rather than assumed away:

1. The resolve genuinely does not land (row not updated, wrong page/org scoping,
   the `getPublication` → `UpdateIncidentPublication` path silently no-oping).
2. `activeOnly` is not actually threaded through on the wired-up adapter path
   ([`server/internal/app/public_incidents.go:26`](server/internal/app/public_incidents.go:26)),
   even though the service-level call site passes `true`.
3. Something re-opens the publication after the PATCH — the auto-publish /
   auto-resolve reconciliation, or the `publishHint` live-update push racing the
   reload.
4. The history panel's cards leak into the DOM before the toggle is opened
   (they share `IncidentCard`, hence the same `#incident-<uid>` id) — currently
   gated behind `{open && …}` in
   [`incident-history.tsx`](web/status0/src/components/shared/incident-history.tsx),
   so this looks unlikely, but the shared id is a real footgun worth noting.

The decisive evidence is cheap: capture the network activity of the failing run
(was the post-reload status-page request served from cache? what does its
`activeIncidents` contain?) and independently `curl` the public endpoint after
the PATCH. Do that before changing a line.

## Proposal

### 1. Diagnose, with evidence, before fixing

Read the resolve path end to end and produce a one-paragraph verdict —
**product bug** or **test premise** — backed by the actual payload after the
PATCH, not by reading the code. Record it in the spec's completion notes.

### 2a. If it is a product bug

Fix it at the layer that is actually wrong, keep
`incident-publications.spec.ts:152` as the regression guard unchanged, and add
backend coverage (table-driven, both DB backends where the query differs) that
fails without the fix.

### 2b. If the test premise is wrong (e.g. HTTP caching)

Re-ground the test on the real contract. **Do not weaken or delete the
assertion.** Acceptable re-groundings, in order of preference:

- Assert against the **server payload** — after the PATCH, fetch the public
  status-page endpoint with cache defeated (`Cache-Control: no-cache` request
  header, or `page.reload({ waitUntil: … })` plus a `page.route`/`request`-level
  bypass) and assert `activeIncidents` contains no entry with that `uid`; then
  assert the DOM matches.
- Or keep the DOM assertion but force a genuinely fresh fetch (hard reload /
  cache-busting query param / `context.clearCookies()`-style cache reset), so the
  assertion still observes the rendered active section.

Whatever lands, the **negative control is mandatory and must be stated in the
final report**: temporarily break the exclusion — e.g. drop the `ActiveOnly`
clause in one DB backend, or have `ListPublicIncidents` ignore `activeOnly` —
and show the re-grounded test **fails**. A test that passes with the filter
removed is not a regression guard and does not satisfy this spec.

Also fix the shared-id footgun if it turns out to matter: an active card and a
history card for the same publication both render `id="incident-<uid>"`, so any
future assertion of the form "count === 0" is fragile by construction. Consider
scoping the locator to the active section
(`[data-testid="active-incidents"] #incident-<uid>`) **in addition to** — never
instead of — the global assertion.

### 3. Put the suite in CI

Priority is the bug; CI wiring is the follow-through.

- At minimum, add `incident-publications.spec.ts` to the status0 E2E step in
  [`.github/workflows/ci.yml`](.github/workflows/ci.yml) (~line 468) and update
  the explanatory comment block above it so the selection stays self-documenting.
- Ideally, take the whole suite green. Four specs currently fail on a
  `SP_RUNMODE=test` server for a **different and benign** reason:
  `status-page.spec.ts` and `subscribe.spec.ts` target the `default` org and its
  `status-0` page, which only `make dev` seeds — test mode seeds
  `demo` / `test` / `test2` / `test3`. Re-ground those on a test-mode org (the
  same `E2E_ORG=test` convention the other specs already use) rather than
  seeding `default` in test mode.
- If re-grounding those four turns out to be more than a contained change, land
  the bug fix plus `incident-publications.spec.ts` in CI, and say explicitly in
  the report which specs are still excluded and why. Do not silently leave the
  CI list untouched.

### Running it locally

```bash
make build-status0 copy-status0 build-backend
# start a side-car on a spare port, with its OWN database and SP_DB_RESET=true
cd web/status0 && CI=true E2E_BASE_URL="http://localhost:<port>" E2E_ORG=test \
  bunx playwright test incident-publications.spec.ts --reporter=list
```

**Never run `pkill -f 'solidping serve'`** — it would kill the developer's
`:4000` devloop. Kill your own side-car by PID.

## Implementation Plan

1. **§1 Diagnose with evidence** — reproduce against a fresh `SP_RUNMODE=test`
   Postgres side-car: `curl` the public endpoint after the PATCH, and run an
   instrumented Playwright script that logs `page.on("requestfinished")` sizes
   for the post-reload status-page request plus an in-page `cache: "no-store"`
   fetch. Cross-check the server access log for a matching line. Record the
   verdict in a `## Diagnosis` section and commit it.
2. **§2 Fix on whichever branch the diagnosis lands.** (It landed on *test
   premise* — see Diagnosis.) Re-ground
   `incident-publications.spec.ts:152` on the real contract without weakening
   the assertion: assert the **server payload** first (unauthenticated,
   `cache: "no-store"`, from Node so no browser cache exists), then assert the
   DOM after a genuinely fresh fetch. Add the active-section-scoped locator
   **in addition to** the global `toHaveCount(0)` so the shared
   `#incident-<uid>` id between active and history cards cannot mask a
   regression.
3. **Negative control (gate)** — drop the `ActiveOnly` clause in the Postgres
   backend, show the re-grounded test FAILS, revert, show it passes.
4. **Backend coverage** — table-driven `ActiveOnly` cases on both DB backends
   so the exclusion is guarded below the e2e layer too.
5. **§3 CI** — add `incident-publications.spec.ts` to the status0 E2E step in
   `.github/workflows/ci.yml` and update the comment block that documents the
   selection. Take as much of the rest of the suite green as is contained;
   report explicitly on anything still excluded.
6. **Gate** — `make build-backend lint-back test`, `make build-status0`,
   `cd web/status0 && bun run lint && bun run typecheck:e2e`, and the full
   status0 suite against the side-car.
