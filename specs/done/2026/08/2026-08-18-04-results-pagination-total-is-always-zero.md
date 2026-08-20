---
model: sonnet
effort: medium
---

# `pagination.total` on the results endpoint is hardcoded to 0, contradicting the OpenAPI contract

## Problem

`GET /api/v1/orgs/:org/results` always returns `"pagination": {"total": 0}`,
even when `data` carries a full page of rows. This is not a filter divergence
or a `periodType` quirk — **the count is never computed at all**.

`server/internal/db/postgres/postgres.go:2258-2283` (and the byte-identical
SQLite path at `server/internal/db/sqlite/sqlite.go:2222-2247`):

```go
// For now, we don't calculate total count as it's expensive
// It can be added later as an optional feature
return &models.ListResultsResponse{
    Results: results,
    Total:   0,
}, nil
```

The service propagates the literal verbatim
(`server/internal/handlers/results/service.go:203-209`), and the model even
documents it (`server/internal/db/models/result.go:197`:
`Total int64 // Total count of results (expensive, may be 0)`).

The problem is the **contract**, not the shortcut. The OpenAPI
`CursorPagination` schema
(`server/internal/app/openapi/openapi.yaml:8291-8300`) declares `total` as a
plain integer with no caveat, and the wiki documents `pagination.total`
semantics for incidents (`wiki/api-specification/results-incidents.md:42-44`)
while saying nothing about results. An API consumer that sizes a pager from
`total` gets "0 results" over a non-empty page. The dash0 org-results hook
already reads it (`web/dash0/src/api/hooks.ts:952`).

## Proposal

Pick one and make the contract say so — do **not** simply switch on an
unbounded `COUNT(*)`:

1. **Drop `total` from the results response** and document the cursor + `size`
   model. Cheapest, honest, and matches how the endpoint is actually consumed.
2. **Make it opt-in** — `?with=total` (or `?count=true`) — computed by running
   the existing `applyResultsFilter` against a second query with the limit and
   cursor predicates omitted. Callers that ask for it accept the cost.
3. **Compute it only when the request is bounded** by a time range, so the scan
   is bounded too.

Whichever is chosen: `results` is the largest table in the system, and an
unbounded `COUNT(*)` scoped only to `organization_uid` + `period_type = 'raw'`
can scan tens of millions of rows on **every page load**. That is why the code
says "expensive", and it is the reason this is a design decision rather than a
one-line fix. The checks list already returns a real total via a dedicated
store-level count (`server/internal/handlers/checks/service.go:903`, `1004`) —
worth reading as prior art, and as a reminder that the same answer is not
automatically right for a time-series table.

The fix must land with a regression test pinning `pagination.total` against
`len(data)` for a known fixture (or, for option 1, pinning that the field is
gone from the response and the OpenAPI schema).

## Notes

- Split out of spec 2026-08-18-02 by explicit decision: that spec allowed an
  in-scope fix only if the cause was a trivial filter divergence. It is not —
  the count is deliberately absent and restoring it carries a performance
  decision — so it is written up separately as that spec instructed.
- Adjacent, found in the same read and worth folding in if cheap:
  `filter.IncludeCheckInfo` is set by the service
  (`server/internal/handlers/results/service.go:175`) but `applyResultsFilter`
  never joins `checks`, and `convertResultToResponse` never populates
  `CheckSlug`/`CheckName` — so `with=checkSlug,checkName` silently returns
  nothing. Same shape of bug: a documented field that is never filled.

## Open questions

- Drop, opt-in, or bounded-only? The answer decides whether this is an API
  break (needs an OpenAPI change and a dash0 follow-up) or an addition.
- `models.ListResultsResponse` also declares `NextCursor` and `HasMore`
  (`result.go:198-199`) that the DB layer never populates — the service
  recomputes both from a `Limit: size + 1` over-fetch. Dead fields on the same
  struct; remove them in the same pass?

## Resolved open questions

> Drop, opt-in, or bounded-only? The answer decides whether this is an API
> break (needs an OpenAPI change and a dash0 follow-up) or an addition.

**Decision: drop `total` from the results response entirely** (option 1 in the
Proposal). Do not add `?with=total`, and do not compute it conditionally.

The endpoint is cursor-paginated, nothing needs a real total, and shipping a
field that is always `0` is worse than not shipping it — an API consumer that
sizes a pager from it reads "0 results" over a full page. Removing it makes the
contract honest instead of preserving a documented lie.

This IS an API break, and that is accepted (the repo is pre-1.0 and
`2026-08-18-01` already established that API changes are fine). The work must
therefore include, in the same change:

- removing `total` from the results response shape and from the
  `CursorPagination` schema **as it applies to this endpoint**
  (`server/internal/app/openapi/openapi.yaml:8291-8300`) — take care not to
  break the incidents endpoint, which returns a genuine total and documents it
  at `wiki/api-specification/results-incidents.md:42-44`,
- removing the dash0 read at `web/dash0/src/api/hooks.ts:952` and anything
  downstream of it,
- documenting the cursor + `size` model for this endpoint in
  `wiki/api-specification/`,
- a regression test pinning that the field is gone from the response **and**
  from the OpenAPI schema, per the spec's existing requirement.

> `models.ListResultsResponse` also declares `NextCursor` and `HasMore`
> (`result.go:198-199`) that the DB layer never populates — the service
> recomputes both from a `Limit: size + 1` over-fetch. Dead fields on the same
> struct; remove them in the same pass?

**Decision: yes, remove them in the same pass.** Same struct, same class of bug
as `Total` — declared but never filled — and this spec is already editing that
struct. They are internal model fields, so removing them is not an additional
API break. Leaving them behind would keep a trap for the next reader who
believes `HasMore` means something.

The adjacent `filter.IncludeCheckInfo` finding in the Notes section (set at
`server/internal/handlers/results/service.go:175`, but `applyResultsFilter`
never joins `checks` and `convertResultToResponse` never populates
`CheckSlug`/`CheckName`, so `with=checkSlug,checkName` silently returns nothing)
stays as the spec wrote it: **fold it in only if it is cheap.** If making it
work is more than a small join plus field population, file it separately rather
than widening this spec.
