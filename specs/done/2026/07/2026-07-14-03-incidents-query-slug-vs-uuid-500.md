---
model: sonnet
effort: medium
---

# Check detail page 500s loading incidents when navigated by slug

## Problem

On the check detail page, opening `.../dash0/orgs/$org/checks/$slug` fires
`.../api/v1/orgs/$org/incidents?checkUid=$slug&limit=100` using the **slug** as
the `checkUid`. The incidents endpoint treats that value as a UUID, so Postgres
rejects it and the incidents section fails with a 500:

```json
{
  "title": "Internal server error",
  "code": "INTERNAL_ERROR",
  "detail": "failed to list incidents: ERROR: invalid input syntax for type uuid: \"http-api-acme-io-datalake\" (SQLSTATE=22P02)"
}
```

Source issue: [#127 — dash0: bug: Bad loading of incidents](https://github.com/fclairamb/solidping/issues/127).

The check detail route param `$checkUid` intentionally accepts **either a slug or
a UUID** — the check REST fetch resolves both via `GetCheckByUidOrSlug`. So
slug-addressed pages are a supported, first-class URL and must keep working. The
bug is narrow: one query forwards the raw slug to an endpoint that can't resolve
it.

## Root cause: a server-side slug-resolution asymmetry

The check detail page issues several check-scoped queries with the same raw
route param. Auditing every one of them (see table below), **`/incidents` is the
only endpoint that does not resolve a slug** — all of its siblings do:

- `/results?checkUid=` resolves the filter through `resolveCheckIdentifiers`
  (its handler comment literally says the param is "check UIDs **or slugs**").
- `/checks/{checkUid}`, `/checks/{checkUid}/availability`, and
  `/checks/{checkUid}/dependencies` all resolve via `GetCheckByUidOrSlug`.
- `/incidents?checkUid=` instead does `strings.Split(v, ",")` and hands the raw
  strings straight to a UUID column filter
  ([`incidents/handler.go:67`](server/internal/handlers/incidents/handler.go),
  `parseListIncidentsOptions`).

That inconsistency — not slug navigation — is the real defect. Any current or
future caller passing a slug to `/incidents` hits the same 500.

## Fix

Slug-addressed check pages stay fully supported. Both the client and the server
change — this is not either/or:

1. **Client — always send the resolved UUID (required).** The frontend must
   never send a slug as `checkUid` for the incidents query; it must send the
   resolved check's UUID. Drive `useIncidents` off `check.uid` from the
   `useCheck` result (which already resolves slug→check), gating the query with
   `enabled: !!check?.uid` so it fires only once the check has loaded. In
   [`checks.$checkUid.index.tsx`](web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx)
   the call is `useIncidents(org, { checkUid, size: 100 })` (~line 522) where
   `checkUid` is the slug-or-uid route param — replace that argument with
   `check.uid`. This guarantees a stable UUID on the wire (matching how the WS
   scope is already validated uid-only per the comment near line 425), and it is
   the primary fix for issue #127: even before the server change lands, the page
   stops 500-ing. The route keeps accepting slug or uid; only the downstream
   incidents call is normalized to the UUID.

2. **Server — make `/incidents` resolve slug-or-uid too (root cause).** Make the
   incidents list handler resolve its `checkUid` filter the same way `/results`
   does — run the comma-separated values through `resolveCheckIdentifiers` (slug
   **or** uid) before building the DB filter, in
   `server/internal/handlers/incidents/`. This makes `/incidents` consistent with
   every other check-scoped endpoint so a slug filter returns the right incidents
   instead of a 500, for *all* callers — not just this page. It's the durable fix
   for the asymmetry; the client change above is what fixes the reported page.
   - This subsumes the old "return 400 instead of 500" idea: once the value is
     resolved as an identifier rather than parsed as a UUID, an unknown
     identifier simply resolves to no match (empty result set), exactly like
     `/results` — no DB type error, nothing to 400 on.

3. **Tests:**
   - Playwright E2E: deep-link to a check **by slug**, and (a) assert the
     incidents section loads with no error toast / no 500, and (b) assert the
     outgoing `/incidents` request carries the check's **UUID** as `checkUid`,
     not the slug — i.e. verify the client-side normalization actually happened
     (intercept the request / assert on its query string).
   - Backend handler/service test: `GET /incidents?checkUid=<slug>` returns 200
     with that check's incidents (not a 500), matching `/results` behavior.

## Audit: every `checkUid` consumer on this page

Traced each consumer of the raw `$checkUid` route param in
[`checks.$checkUid.index.tsx`](web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx)
to its backend endpoint. **Only `useIncidents` hits a slug-unaware endpoint** —
confirming no other client query needs a fix:

| Page consumer | Endpoint | Server-side resolution | Slug safe? |
|---|---|---|---|
| `useCheck` (463) | `GET /checks/{uid}` | `GetCheckByUidOrSlug` | ✅ |
| `useAllResults` (478) + `ResponseTimeChart` (925) | `GET /results?checkUid=` | `resolveCheckIdentifiers` ("UIDs **or slugs**") | ✅ |
| `useResults` (514) | `GET /results?checkUid=` | `resolveCheckIdentifiers` | ✅ |
| **`useIncidents` (522)** | `GET /incidents?checkUid=` | **none — `strings.Split` into a UUID filter** ([`incidents/handler.go:67`](server/internal/handlers/incidents/handler.go)) | ❌ **500** |
| `useUpdateCheck` (527) / `useDeleteCheck` (568) / `useCloneCheck` (578) | `PATCH`/`DELETE`/`POST /checks/{uid}[/clone]` | `GetCheckByUidOrSlug` | ✅ |
| `AvailabilityTable` → `useCheckAvailability` (950) | `GET /checks/{checkUid}/availability` | `GetCheckByUidOrSlug` ([`availability/service.go:118`](server/internal/handlers/availability/service.go)) | ✅ |
| `DependenciesCard` → `useCheckDependencies` + dep update/delete (1148) | `.../checks/{checkUid}/dependencies` | `resolveCheck` → `GetCheckByUidOrSlug` ([`checkdependencies/service.go:94`](server/internal/handlers/checkdependencies/service.go)) | ✅ |

## Non-goals

- **Do not drop slug support.** `.../checks/$slug` URLs and the slug-or-uid route
  param stay exactly as they are; the incidents endpoint should *gain* slug
  support, not lose it elsewhere.
- **Do not rename `checkUid` to `check` (or anything else).** Yes, once the param
  accepts a slug the `Uid` suffix is technically a misnomer — but the whole API
  already treats `checkUid` as "check identifier (uid *or* slug)": the path param
  `/checks/{checkUid}` has always resolved both, `/results?checkUid=` resolves
  both, and the convention is documented in `CLAUDE.md` (`?checkUid=a,b`). This
  fix makes `/incidents` *match* that established meaning, not diverge from it.
  Renaming only the query param would create a new path-vs-query inconsistency;
  renaming the whole surface (path + every `checkUid` query param + OpenAPI +
  docs + MCP schemas + frontend hooks) is a breaking, deprecation-gated API
  change that must be decided on its own merits, never folded into a 500 bugfix.
  The only naming-related change in scope is a **doc-only** tweak: update the
  OpenAPI description of `checkUid` to "check UID or slug" so the contract is
  honest, matching what `/results` already documents.

## Implementation Plan

1. **Backend root-cause fix** — `server/internal/handlers/incidents/service.go`,
   `ListIncidents`: resolve `opts.CheckUIDs` (raw uid-or-slug identifiers) via
   a new `(s *Service) resolveCheckIdentifiers` helper (mirrors
   `results.Service.resolveCheckIdentifiers`, calling `GetCheckByUidOrSlug`
   per identifier) before building `models.ListIncidentsFilter`. Handles the
   edge case the literal `/results` parity misses: if identifiers were
   supplied but none resolved to a real check, short-circuit to an explicit
   empty page instead of falling through to "no filter" (which would
   silently return every incident in the org, since the DB layer's
   `check_uid IN (?)` gate is `len(filter.CheckUIDs) > 0`).
2. **Frontend client fix** — `web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx`:
   drive `useIncidents` off `check.uid` instead of the raw `checkUid` route
   param, gated with `enabled: !!check?.uid` so the query fires only once the
   check has resolved. Add an `enabled?: boolean` option to
   `useIncidents` in `web/dash0/src/api/hooks.ts` (same pattern as
   `useLabelSuggestions`/`useCheckAvailability`: `(options?.enabled ?? true) && !!org`)
   since the hook has other callers that don't want this gating.
3. **Tests**:
   - Backend: `server/internal/handlers/incidents/list_by_slug_test.go` —
     SQLite-backed handler-level tests (fast, run under `make test`) proving
     `GET /incidents?checkUid=<slug>` returns 200 with the right incidents,
     `checkUid=<uid>` still works, and an unmatched identifier returns an
     empty page (not every incident).
   - Backend: `server/internal/handlers/incidents/list_by_slug_postgres_test.go` —
     embedded-Postgres regression test (self-skips under `-short`/CI,
     mirroring `results.TestGetResultNeighbors_Postgres`) that reproduces the
     actual reported 500 against a real `uuid`-typed `check_uid` column.
     Manually verified to fail with the exact reported SQLSTATE=22P02 error
     when the fix is reverted, and pass with it applied.
   - E2E: extend `web/dash0/e2e/` to deep-link a check by slug and assert (a)
     no incidents-section error and (b) the outgoing `/incidents` request
     carries the resolved UUID, not the slug.
4. **Doc-only**: update the OpenAPI description of `checkUid` on `/incidents`
   to "check UID or slug".
5. QA: `make build-backend lint-back test`, `make build-dash0` + dash0 lint,
   Playwright E2E for the new/extended spec file.
