---
model: sonnet
effort: medium
---

# `with=checkSlug,checkName` on the results endpoint silently returns nothing

## Problem

`GET /api/v1/orgs/:org/results?with=checkSlug,checkName` is accepted and
returns 200, but `checkSlug`/`checkName` are never present on any row.

The service marks the request as wanting check info
(`server/internal/handlers/results/service.go:175`,
`filter.IncludeCheckInfo = s.needsCheckInfo(opts.With)`), but that flag is a
dead write:

- `applyResultsFilter` (shared by both dialects,
  `server/internal/db/postgres/postgres.go` and
  `server/internal/db/sqlite/sqlite.go`) never reads
  `filter.IncludeCheckInfo` and never joins `checks`.
- `models.Result` has no `CheckSlug`/`CheckName` fields to hold a joined
  value in the first place.
- `convertResultToResponse` (`server/internal/handlers/results/service.go`)
  never populates `ResultResponse.CheckSlug`/`CheckName` even though both are
  already declared on that struct (`json:"checkSlug,omitempty"` /
  `json:"checkName,omitempty"`) and documented as `with=` options
  (`withCheckSlug`, `withCheckName` constants).

Same shape of bug as the sibling spec 2026-08-18-04 (a documented field that
is never filled) — split out from it per that spec's own scoping decision:
fold in only if cheap, file separately otherwise. Wiring the join turned out
to touch both DB dialects' query builders plus the shared model, which is
more than a small join-and-populate, so here it is as its own spec.

## Proposal

1. Add `CheckSlug *string` / `CheckName *string` (or a nested `Check` value)
   to `models.Result` to hold the joined columns. Bun scans extra
   `ColumnExpr` selections into matching struct tags, so this can stay a
   flat pair of fields keyed by `bun:"check_slug"` / `bun:"check_name"`
   rather than a real relation.
2. In `applyResultsFilter` (or a variant called only when
   `filter.IncludeCheckInfo` is true, to avoid paying the join on every
   request), join `checks` on `results.check_uid = checks.uid` and select
   `checks.slug AS check_slug, checks.name AS check_name` alongside the
   existing columns. Implement identically on both
   `server/internal/db/postgres/postgres.go` and
   `server/internal/db/sqlite/sqlite.go` — see `wiki/CLAUDE.md` /
   `sync-pg-to-sqlite` convention: the two dialects must stay
   behaviorally identical.
3. In `convertResultToResponse`
   (`server/internal/handlers/results/service.go`), populate
   `resp.CheckSlug`/`resp.CheckName` from the joined columns when
   `with` requested them (mirror the existing `withSet["checkslug"]` /
   `withSet["checkname"]` gating already implied by the constants at
   `service.go:421-422`).
4. Decide how to handle a `check_uid` whose check has been hard-deleted
   (results can outlive a hard-deleted check per `RequireCheckExists`'s
   doc comment in `models/result.go`) — an `INNER JOIN` would silently drop
   those rows from the page, which is a different, worse bug than the one
   being fixed. Use a `LEFT JOIN` and leave `CheckSlug`/`CheckName` nil for
   orphaned rows.

## Notes

- Only join when `IncludeCheckInfo` is set — most callers of `ListResults`
  don't pass `with=checkSlug,checkName` and shouldn't pay for a join they
  didn't ask for (aggregation work-discovery, availability computation, etc.
  all call this path too).
- Add a regression test seeding a check + result, requesting
  `with=checkSlug,checkName`, and asserting both fields are populated —
  plus a case with a hard-deleted check's orphaned result proving the row
  still comes back (with nil check fields) rather than disappearing.
