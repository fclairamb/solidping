---
model: opus
effort: medium
---

# Degraded detection ships a dry run (stamp, banner, list filter) nobody needs: drop it and keep only the per-check toggle

## Problem

Degraded detection (spec `2026-09-22-03`, not released yet: latest tag is `v0.31.1`)
ships **off** for every existing check and **on** for new ones. To drive adoption
on the existing checks, it adds a dry run:

- The evaluator sweeps **every** check, `degraded_enabled` or not
  (`server/internal/db/postgres/degraded.go:17-19`, interface doc at
  `server/internal/db/service.go:1211-1217`).
- On a disabled check it opens nothing and stamps `checks.degraded_would_fire_at`
  (earliest wins) in `applyFiring`
  (`server/internal/handlers/degradedeval/service.go:183-195`), and clears the
  stamp once the check is enabled (`service.go:113-118`).
- The check page turns the stamp into a banner
  (`web/dash0/src/components/checks/degraded-dry-run-banner.tsx`, rendered from
  `routes/orgs/$org/checks.$checkUid.index.tsx`).
- The checks list gets a "Would have fired" toggle, `?wouldHaveFired=true`
  (`routes/orgs/$org/checks.index.tsx`, button around line 1680, search param
  around lines 143-207), backed by `ListChecksFilter.WouldHaveFired`
  (`server/internal/db/models/check.go:698-702`) and
  `degraded_would_fire_at IS NOT NULL` in both drivers
  (`postgres.go:2031`, `sqlite.go:1935`).

This is a lot of surface for a single toggle. The button reads as cryptic on the
list page ("would have fired" what?). We decided to remove the dry run entirely.

## Decisions (already made, do not re-open)

1. **Existing checks stay OFF on upgrade.** `degraded_enabled` keeps
   `NOT NULL DEFAULT FALSE`, so rows that exist when the migration runs are off.
   New checks keep getting `true` (`models.NewCheck` already does this). With no
   dry run, an existing check only gets degraded detection when someone turns on
   its toggle.
2. **Edit migration `023_v0_32_0` in place** (postgres + sqlite, up + down) as if
   the dry run never existed. It is in no tag yet, and every deployed environment
   (dev included) only runs tagged releases, so no deployed database has applied it.
   Do **not** add a `024` migration.
3. **Keep the per-check `degradedEnabled` toggle.** A disabled check is not
   evaluated at all. No stamping.

## Proposal

### Schema

- `server/internal/db/postgres/migrations/023_v0_32_0.up.sql` and
  `server/internal/db/sqlite/migrations/023_v0_32_0.up.sql`: drop the
  `degraded_would_fire_at` column and its `comment on column`, and rewrite the
  header comment (postgres lines ~29-35, sqlite ~23-30). It should say: off for
  existing rows, on for new checks, enabling is a per-check decision. No mention
  of a dry run, a banner or a filter.
- Matching `.down.sql`: remove the `drop column degraded_would_fire_at`.
- Keep `degraded_evaluated_at`. It is evaluator rotation state and is unrelated.

### Backend

- `models/check.go`: remove the `DegradedWouldFireAt` field, the
  `CheckUpdate.DegradedWouldFireAt` / `ClearDegradedWouldFireAt` pair, and
  `ListChecksFilter.WouldHaveFired`.
- `db/postgres/postgres.go` and `db/sqlite/sqlite.go`: remove the list filter
  (~2031 / ~1935) and the update branches (sqlite ~2111-2113, postgres
  equivalent).
- **Sweep only enabled checks.** `ListChecksForDegradedEval` in both drivers adds
  `degraded_enabled = true`. Update the doc comments at
  `db/postgres/degraded.go:17-19` and `db/service.go:1211-1217`, plus the sqlite
  twin. The index behind this query may need `degraded_enabled` added. Check the
  plan and add it to 023 if the sweep now scans.
- `handlers/degradedeval/service.go`: `applyFiring` drops the
  `!check.DegradedEnabled` branch. Keep a defensive `return nil` for a disabled
  check if you like, but it must not stamp anything. Remove the
  clear-on-enable block (113-118). Fix the "dry-run" wording in the
  `applyLifecycle` / `applyFiring` / line-103 comments.
- `handlers/checks/degraded.go:111-141`: remove the clear-on-enable and its
  comment.
- `handlers/checks/service.go`: remove the `degradedWouldFireAt` API field
  (~910-913, ~3202), `ListChecksOptions.WouldHaveFired` (~1040-1042, ~1093),
  the query-param parsing in the handler, and the export/import note at ~4639.
  Leave the unrelated `/apply` "dry run" code alone: it is a different feature.
- `app/openapi/openapi.yaml`: remove the `wouldHaveFired` parameter (~1395), the
  `degradedWouldFireAt` property (~10075), and the "runs as a dry run that only
  stamps `degradedWouldFireAt`" sentences in the `degradedEnabled` descriptions
  (~10074, ~10440, ~10560). Replace them with "When false, degraded detection is
  not evaluated for this check." Then regenerate `server/pkg/client`
  (`go generate ./pkg/client/...`).
- MCP (`server/internal/mcp/tools_checks.go`): remove any `wouldHaveFired`
  argument or `degradedWouldFireAt` output if present.

### Open incident when the toggle goes off (behavior change to handle)

Today a disabled check is still swept. So a degraded incident opened while the
check was enabled still auto-resolves after someone turns the toggle off. Once
the sweep skips disabled checks, that incident would stay **open forever**.

Fix: when a check's `degradedEnabled` goes from true to false, resolve its open
degraded incident, if any (`FindActiveDegradedIncident`), in the same request.
This must hold on **every** write path that can flip the flag: PATCH
(`handlers/checks/degraded.go`), `/apply` and import (`service.go` ~4639,
`apply.go`), and MCP `update_check`. Put it in one shared place rather than in
each handler. Title and resolution reason should say it was resolved because
degraded detection was turned off, not because the check recovered.

Alternative, if the shared hook turns out awkward: sweep
`degraded_enabled = true OR <has an open degraded incident>` and resolve
immediately when disabled. Pick one and say which in the commit.

### Dashboard (dash0)

- Delete `components/checks/degraded-dry-run-banner.tsx` and its `.test.tsx`,
  and its usage in `routes/orgs/$org/checks.$checkUid.index.tsx`.
- `routes/orgs/$org/checks.index.tsx`: remove the "Would have fired" button, the
  `wouldHaveFired` search param (type, `validateSearch`, the active-filter
  computation around line 1185, the query option around line 1239).
- `api/hooks.ts`: remove `degradedWouldFireAt`, the `wouldHaveFired` option and
  param (~671-689, ~782), and fix the `degradedEnabled` doc comment (~173-178).
- `components/shared/check-form.tsx` (+ `check-form.test.ts`): the
  `degraded.enabledHelp` text says "Off means dry run: …". Rewrite it to
  something like "Off: degraded detection is not evaluated for this check."
- Locales (all four: `en`, `fr`, `de`, `es` under `src/locales/*/checks.json`):
  remove `wouldHaveFiredFilter` and the dry-run banner keys (`bannerTitle`,
  `bannerDescription`, `enableAction`, `enableFailed`, and siblings under the
  `degraded` block around `en` line 726), and rewrite `enabledHelp`.
  `locale-parity.test.ts` must stay green.
- e2e: `web/dash0/e2e/degraded-detection.spec.ts` covers the banner and filter.
  Remove those cases and keep the rest. `checks-import-sources.spec.ts` also
  matches; check it and adjust only if it touches this.

### Docs

- `wiki/features/degraded-detection.md`: remove the `degraded_would_fire_at` row
  (~115), the "Rollout: the dry run" section (~194-215), and rewrite the
  references at ~150-153 and ~240. Rollout becomes: off for existing checks,
  on for new ones, per-check toggle.
- Grep `wiki/` and `web/docs/` for `would have fired`, `wouldHaveFired`,
  `degradedWouldFireAt` and `dry run` in the degraded context. Update
  `wiki/api-specification/checks.md` if it documents the param or field. The
  other `dry run` hits (config-as-code, cli, mcp, custom-domain-tls) are
  unrelated `/apply` / validate dry runs. Leave them.
- `CHANGELOG.md`, `## Unreleased`, line 7: the degraded-detection entry ends with
  "…and while off, a dry run stamps only 'this check would have been flagged
  degraded at …' with a banner and a deep link, so nothing starts paging on its
  own". Edit that entry in place, since it has not been released. Keep "ships off
  for every existing check and on for new ones" and drop the dry-run clause. Do
  not add a separate changelog entry.

### Local databases only

No deployed environment has run 023: dev (solidping.k8xp.com) and prod both get
tagged releases only, and 023 is in no tag yet. Local databases built from `main`
or a batch branch (`make dev`, side-car E2E databases) have run the old 023.
`bun_migrations` records it by name, so the edited file will not re-run there.
Reset them (`SP_DB_RESET`), or drop the column by hand:

```sql
alter table checks drop column if exists degraded_would_fire_at;
```

## Acceptance

- `git grep -i "would_fire_at\|wouldFireAt\|wouldHaveFired\|would have fired\|dry-run-banner"`
  returns nothing outside `specs/done/`.
- A disabled check is not returned by `ListChecksForDegradedEval` (test on both
  SQLite and Postgres). An enabled one still is.
- A check whose rules match while `degradedEnabled` is false opens no incident
  and writes nothing on the check row except `degraded_evaluated_at` (or not
  even that, since it is no longer swept).
- Turning `degradedEnabled` off while a degraded incident is open resolves that
  incident, through PATCH **and** through `/apply`. Positive control: the same
  flow with the flag left on keeps the incident open.
- A fresh database from the edited 023 has no `degraded_would_fire_at` column,
  and `degraded_enabled` defaults to false for pre-existing rows. `models.NewCheck`
  still yields true.
- `make lint`, `make test`, `make test-postgres`, dash0 `bun run test:unit`
  (locale parity), and `web/dash0/e2e/degraded-detection.spec.ts` pass.
