# Discovery & notifications chrome, list cleanup, and multi-check IP promotion

## Context

Four small UX gaps in the operator dashboard, all around the network-discovery feature and
the personal notifications page:

1. **`/me/notifications` and `/discovery` lack breadcrumbs**, and their header icons are
   inconsistent with the sidebar — notifications shows `Bell` in its header but `BellRing` in
   the sidebar, and discovery has no header icon at all (sidebar uses `Network`). When a user
   clicks a sidebar entry, the breadcrumb and page title should carry the same icon (the
   convention established by spec `2026-05-03-56-header-icon-and-breadcrumb-consistency`).
2. The **discovery scan list's first column is labelled "IP Address" but renders the scan
   UID** — it's meaningless and should be removed.
3. **IP promotion is single-check only.** A discovered host already carries a
   `suggestedChecks[]` array (e.g. http + ssl + ping), but the promote screen ignores all but
   the first and forces one `checkType` via a dropdown. Users should be able to tick the
   checks they want and create them in one go. The promote action also reuses the `Pencil`
   (edit) icon, which is semantically wrong.

Intended outcome: consistent chrome on both pages, a cleaner scan list, and a promote flow
that turns a discovered host into the several checks the scan suggested.

## Honest opinion

- **Reuse the shared `PageHeader`** for both page headers rather than hand-rolled `<h1>` +
  icon. It already renders `icon`/`title`/`description`/`actions` consistently, so it directly
  satisfies "the icon should always be the same" and matches the design reference. The
  discovery index's existing action buttons (refresh / Freebox / new scan) move into
  `PageHeader`'s `actions` slot.
- **Mark the host promoted to the *first* created check.** `promoted_to_check_uid` is a single
  FK and only functions as a "has been promoted" flag in the UI. Pointing it at the first
  created check is sufficient; a full many-to-many is out of scope and unjustified.
- **Generate distinct slugs per check.** Slugs are unique per org, so promoting http+tcp for
  one host needs `host-http`, `host-tcp`. Suffix name+slug with the type only when >1 check is
  selected; a lone selection keeps the clean base name/slug.
- **Keep a manual fallback.** Hosts with an empty `suggestedChecks[]` still need to be
  promotable, so retain the existing 5-type picker for that case.

## Goal

- Breadcrumb + matching header icon on `/discovery` (`Network`) and `/me/notifications`
  (`BellRing`).
- Remove the bogus first column from the discovery scan list.
- Multi-check promotion: select from suggested checks (or a manual type when none) and create
  all selected checks in one request; `CirclePlus` promote icon.

## Non-goals

- No many-to-many host↔check model; `promoted_to_check_uid` stays single (first check).
- No transactional rollback rework of check creation beyond fail-fast on error (see Risks).
- No translation of the notifications page's hard-coded English strings (pre-existing).
- No changes to the discovery scan engine or `suggestedChecks` generation.

## Backend (multi-check promotion)

`server/internal/handlers/discovery/service.go`

- Replace the single-check `PromoteRequest` with a list shape:

  ```go
  type PromoteCheckSpec struct {
      CheckType   string         `json:"checkType"`
      Name        string         `json:"name,omitempty"`
      Slug        string         `json:"slug,omitempty"`
      Period      *string        `json:"period,omitempty"`
      ExtraConfig map[string]any `json:"extraConfig,omitempty"`
  }

  type PromoteRequest struct {
      Checks []PromoteCheckSpec `json:"checks"`
  }
  ```

- `PromoteHost(ctx, orgUID, orgSlug, hostUID, req)` returns `[]checks.CheckResponse`:
  fetch host, reject if already promoted, then for each spec build config via the existing
  `buildCheckConfig` (refactored to take a `PromoteCheckSpec`), attach the same
  `auto-discovery` / `discovery-job` labels, and call `checksSvc.CreateCheck`. Default an
  empty per-spec `Name` to `host.IP`. After all succeed, set `promoted_to_check_uid` to the
  **first** created check's UID. On any `CreateCheck` error, abort and return it.

`server/internal/handlers/discovery/handler.go`

- `PromoteHost`: decode the new body; validate `len(req.Checks) >= 1` and each
  `CheckType != ""` (else `VALIDATION_ERROR`). Keep `ErrHostNotFound` → 404,
  `ErrAlreadyPromoted` → 409. Respond `201` with `{ "data": [check, …] }` (list wrapper per
  API convention).

Tests: update `service_test.go` / `handler_test.go` for the array request/response — cover
single-check, multi-check (distinct slugs), already-promoted, empty-checks validation.

## Frontend

`web/dash0/src/api/hooks.ts`

- Add `PromoteCheckSpec`; change `PromoteCandidateRequest` to `{ checks: PromoteCheckSpec[] }`.
- `usePromoteCandidate` now resolves to `{ data: Check[] }`; keep invalidating both
  `discoveryHosts` and `checks` queries.

`web/dash0/src/routes/orgs/$org.tsx` (breadcrumbs)

- Add icon imports `Network`, `BellRing`.
- Add flags: `isDiscovery = matches.some(m => m.routeId.startsWith("/orgs/$org/discovery"))`
  and `isNotifications = routeIds.has("/orgs/$org/me/notifications")`.
- Discovery branch (mirrors the incidents/checks pattern): root `Network + t("discovery")`
  link/active, `t("new")` leaf for `/discovery/new`, scan-UID sub-crumb for `$jobUid`, and a
  `t("promote")` leaf for the promote route.
- Notifications branch: single active crumb `BellRing + t("myPages")`.

`web/dash0/src/routes/orgs/$org/me.notifications.tsx`

- Swap `Bell` → `BellRing`; replace the inline header `<div>` with
  `<PageHeader icon={BellRing} title="My pages" description="Incidents you were paged for, in reverse chronological order." />`.
  Keep `data-testid="my-notifications-page"` on the page wrapper.

`web/dash0/src/routes/orgs/$org/discovery.index.tsx`

- Replace the inline header with
  `<PageHeader icon={Network} title={t("title")} description={t("subtitle")} actions={…} />`
  (refresh button + `FreeboxLauncher` + New Scan button go in `actions`).
- Remove the first `<TableHead>{t("ip")}</TableHead>` and the first `<TableCell>` in
  `ScanRow` (the one rendering `scan.uid.slice(0,8)`). Leave the `ip` translation key (still
  used by the host table).

`web/dash0/src/routes/orgs/$org/discovery.$jobUid.tsx`

- `HostRow`: change the promote link icon `Pencil` → `CirclePlus`; update imports.

`web/dash0/src/routes/orgs/$org/discovery.$jobUid.$hostUid.promote.tsx`

- Replace the single `checkType` `Select` with a checkbox list of `host.suggestedChecks`
  (each row: `Checkbox` pre-checked + type label + a short config hint, e.g. port/url). Keep
  shared `Name` and `Period` inputs at the top.
- Fallback when `suggestedChecks` is empty: render the existing 5-type picker so a single
  check can still be created.
- On submit, build `checks[]` from the ticked rows. Per-check slug =
  `slugify(baseName)` for a single selection, else `slugify(baseName)-<type>`; per-check name
  = `name` for one, else `name (<type>)`. Call `promote.mutateAsync({ uid, req: { checks } })`,
  toast, navigate back. Disable submit when nothing is selected.
- Use `CirclePlus` on the submit button.

i18n

- `web/dash0/src/locales/{en,fr}/nav.json`: add `promote` (en `"Promote"`, fr `"Promotion"`).
- `web/dash0/src/locales/{en,fr}/discovery.json`: add keys for the new UI —
  `selectChecks`, `createChecks`, `noSuggestedChecks` (en + fr). Reuse existing
  `name`/`slug`/`period`/`checkType`/`cancel`/`promote`.

## Files to create / modify

Backend:
- `server/internal/handlers/discovery/service.go`
- `server/internal/handlers/discovery/handler.go`
- `server/internal/handlers/discovery/service_test.go`, `handler_test.go`

Frontend:
- `web/dash0/src/routes/orgs/$org.tsx`
- `web/dash0/src/routes/orgs/$org/me.notifications.tsx`
- `web/dash0/src/routes/orgs/$org/discovery.index.tsx`
- `web/dash0/src/routes/orgs/$org/discovery.$jobUid.tsx`
- `web/dash0/src/routes/orgs/$org/discovery.$jobUid.$hostUid.promote.tsx`
- `web/dash0/src/api/hooks.ts`
- `web/dash0/src/locales/{en,fr}/nav.json`, `web/dash0/src/locales/{en,fr}/discovery.json`
- `web/dash0/e2e/discovery.spec.ts` (extend)

## Verification

Backend / API (server on :4000, login per `server/CLAUDE.md`):
- `POST /api/v1/orgs/<org>/discovery/hosts/<uid>/promote` with
  `{"checks":[{"checkType":"http"},{"checkType":"ping"}]}` → `201` and a 2-element
  `data` array; both checks appear under `GET …/checks`; the host now reports
  `promotedToCheckUid`. Empty `checks` → `422`; re-promote → `409`.

Frontend (Playwright + manual, `make dev-test`):
- `/discovery` and `/me/notifications` show a breadcrumb with `Network` / `BellRing`
  respectively, matching the sidebar and the page-header icon.
- Discovery scan list no longer has the "IP Address" column.
- Promote page lists suggested checks with pre-checked boxes; selecting two and submitting
  creates two checks and returns to the scan detail; the row icon is `CirclePlus`.
- A host with no suggested checks still promotes via the manual type picker.
- FR locale: labels translate, icons unchanged.
- `make fmt && make lint && make test && make test-dash`.

## Risk log

| Risk | Mitigation |
|---|---|
| Slug collision when creating several checks for one host | Per-check slug suffixed with type; validate/trim to 48 chars as today |
| Partial creation if a later `CreateCheck` fails | Fail-fast returns the error; earlier checks persist. Document; full tx rollback is a follow-up if needed |
| Changing `PromoteRequest` shape breaks existing callers/tests | Only the dash0 client and discovery tests use it; both updated in this spec |
| `promoted_to_check_uid` points at only the first check | Acceptable — it is a boolean-ish "promoted" flag in the UI; not a relation |

## Implementation Plan

1. **Backend service (`service.go`)** — Replace `PromoteRequest` with the array shape
   (`PromoteCheckSpec` + `PromoteRequest{ Checks []PromoteCheckSpec }`). Refactor
   `buildCheckConfig` to take a `PromoteCheckSpec`. Rewrite `PromoteHost` to return
   `[]checks.CheckResponse`: fetch host, reject already-promoted, build each check via
   `buildCheckConfig`, attach `auto-discovery`/`discovery-job` labels, default empty per-spec
   `Name` to `host.IP`, call `CreateCheck` fail-fast, then set `promoted_to_check_uid` to the
   first created check's UID.
2. **Backend handler (`handler.go`)** — Decode the new body; validate `len(Checks) >= 1` and
   each `CheckType != ""` → `VALIDATION_ERROR` (422). Keep `ErrHostNotFound` → 404,
   `ErrAlreadyPromoted` → 409. Respond 201 with `{ "data": [check, …] }`.
3. **Backend tests** — `service_test.go`: single-check, multi-check (distinct slugs),
   already-promoted, empty-checks. Add `handler_test.go`: HTTP-level multi-check 201,
   empty-checks 422, already-promoted 409, missing checkType 422.
4. **Frontend hooks (`hooks.ts`)** — Add `PromoteCheckSpec`; change
   `PromoteCandidateRequest` to `{ checks: PromoteCheckSpec[] }`; `usePromoteCandidate`
   resolves to `{ data: Check[] }`; keep invalidating `discoveryHosts` + `checks`.
5. **Breadcrumbs (`$org.tsx`)** — Import `Network`, `BellRing`. Add `isDiscovery` /
   `isNotifications` flags. Discovery branch (root link + `new` leaf + scan-UID sub-crumb +
   `promote` leaf). Notifications branch (single `BellRing + myPages` crumb).
6. **Notifications page (`me.notifications.tsx`)** — Swap `Bell` → `BellRing`; replace inline
   header with `PageHeader`. Keep `data-testid`.
7. **Discovery index (`discovery.index.tsx`)** — Replace inline header with `PageHeader`
   (refresh + Freebox + New Scan into `actions`). Remove the bogus first `IP Address`
   column header + cell.
8. **Scan detail (`discovery.$jobUid.tsx`)** — Promote icon `Pencil` → `CirclePlus`.
9. **Promote page (`discovery.$jobUid.$hostUid.promote.tsx`)** — Checkbox list of
   `suggestedChecks` (pre-checked, type label + config hint), shared Name + Period inputs.
   Empty `suggestedChecks` → fall back to the 5-type picker. Build `checks[]` with per-check
   slug/name suffixing; `CirclePlus` submit; disable when nothing selected.
10. **i18n** — `nav.json` `promote`; `discovery.json` `selectChecks`, `createChecks`,
    `noSuggestedChecks` (en + fr).
11. **e2e** — Extend `discovery.spec.ts`: breadcrumb icons present, no IP column, promote
    page shows checkboxes.
12. **QA** — `make fmt`, build backend + dash0, lint-back, test until green; completeness
    audit.
