---
model: opus
effort: high
---

# Every public status-page read recomputes the page; memoize it per process with singleflight

## Problem

`ViewStatusPage`, `ViewDefaultStatusPage`, `ViewStatusPageSummary` and
`GenerateBadge` compute their answer from scratch on every request: sections,
200 per-resource lookups, the availability buckets, the response-time seam,
incidents, updates. The public directive on the response is `public,
max-age=60`, and the product's own rule (`statuspagecache.PageMaxAge`) is that
sixty seconds is the freshness a status page owes its readers. Nothing on the
server side honours that budget: two wallboards, a README badge and a customer
refreshing the page are four independent recomputations of one answer, and
they queue on the same 11-connection pool (`SP_DB_MAX_OPEN_CONNS` on the
reference deployment).

Specs `2026-09-22-05` to `-08` bring the cost of one computation from ~7 s to
about a second. This spec makes the second reader within the freshness window
cost nothing, and makes N concurrent readers cost one computation — which is
exactly the load shape of an incident, when everybody opens the page at once
and the infrastructure behind it is already unhealthy (the scenario spec
`2026-08-22-06` was written for).

## Decision

An in-process, TTL-bound memo of the computed view, keyed by page and payload
shape, with `singleflight` collapsing concurrent misses. The access gate runs
on every request **before** the memo is consulted. The HTTP directive gains
`stale-while-revalidate` so a browser's own cache stops blocking on the
refresh.

## Design

### What is memoized

Two products, in `statuspages.Service`:

| Product | Key | Producer |
|---|---|---|
| `StatusPageResponse` | `(pageUID, ViewOptions)` — the `include` set from spec `2026-09-22-07` is part of the key | the body of `ViewStatusPage` after the gate |
| `StatusPageSummary` | `(pageUID, withAvailability)` | the body of `viewStatusPageSummary` after the gate; the badge and the summary endpoint share it |

The memoized value is the **computed struct**, not the encoded bytes: encoding
3 MB of JSON is a few milliseconds and the struct is what the two default-page
and slug paths share. Entries carry the time they were computed.

### What is not memoized

- `GetOrganizationBySlug`, `GetStatusPageBySlug`, `publicAccessError` (the
  kiosk / unlock / visibility gate) and `maybeReconcileOnView` run on every
  request, unchanged. A `password` page's body is the same for everyone who
  passed the gate, so sharing the computed body in-process is safe; what must
  never happen is serving it to someone who did not pass, and the gate-first
  order is what guarantees that. The HTTP layer still answers `private,
  no-store` for gated pages, unchanged.
- Anything derived from the request: `publicPageURL` uses the request's
  scheme and host and stays in the handler.

### TTL

**15 seconds.** Half the TV's fastest poll and a quarter of the 60 s the
directive promises, so the memo never adds visible staleness on top of what
the HTTP layer already permits. A check status flip therefore reaches a
reader within `TTL + poll`, which is inside the existing 60 s contract; check
status changes are deliberately **not** invalidation events (there are
thousands a minute on a large installation).

### Invalidation

Every write that changes a page's public body evicts that page's entries
(both products, every key variant):

- `statuspages`: `UpdateStatusPage`, `DeleteStatusPage`, `CreateSection`,
  `UpdateSection`, `DeleteSection`, `ReorderSections`, `CreateResource`,
  `UpdateResource`, `DeleteResource`, `ReorderResources`, and selector
  materialisation (the reconcile path, whether triggered by a write or by
  `maybeReconcileOnView` — evict only when it changed something).
- `statuspageassets`: logo / favicon upload and delete (the body carries the
  branding URLs through `resolvePublicBranding`).
- `incidentpublications`: `CreatePublication`, `UpdatePublication`,
  `PublishIncident`, resolution, and the auto-publish / auto-resolve jobs.
- `statusupdates`: create, update, delete.

The other packages reach the memo through a small `PageMemoInvalidator`
interface (`Invalidate(pageUID string)`) injected the way
`PublicIncidentProvider` already is — the wiring lives in `server.go` next to
the existing seam; no import cycle. An eviction is a map delete; a write path
that cannot resolve the page UID evicts by organisation (`InvalidateOrg`).

Kiosk-token rotation, unlock-cookie changes and organisation renames do not
touch the body and evict nothing (the key is the page UID, not the slug).

### Singleflight

`golang.org/x/sync/singleflight` (already a dependency). A miss computes under
the key; concurrent misses for the same key wait for that one computation and
share its result. A computation that returns an error is not stored.

### Bounds

Entries expire at TTL and are evicted lazily on read, plus a sweep on every
insert when the map exceeds 256 entries (oldest first). A very large page is
about 3 MB in memory; 256 of them would be an installation with hundreds of
huge public pages, which is not a shape that exists. No configuration knob:
the TTL is a product constant next to `PageMaxAge`, in `statuspagecache`, so
the two freshness numbers are declared side by side.

### Metrics and logs

Counters `statuspage_memo_hits_total` / `_misses_total` /
`_singleflight_shared_total` in the existing metrics registry
(`internal/middleware/metrics.go` and whatever `HTTPMetrics` feeds), so a
graph can show the memo doing its job during the next incident.

### `stale-while-revalidate`

`statuspagecache.Control` for public pages becomes
`public, max-age=60, stale-while-revalidate=30`. A browser (and a CDN that
honours it) then answers an expired entry immediately and refreshes it in the
background instead of blocking the render. Worst-case staleness for a reader
polling every 30 s rises from 60 s to about 90 s in the unlucky alignment,
still inside what a wallboard's stale indicator tolerates (`isStale`), and
the memo below makes the background refresh cheap. It applies to the page,
summary, badge, incidents and feed responses alike, because they share
`Control`. Update the exact-string pins in
[`cache_control_test.go:218`](server/internal/handlers/statuspages/cache_control_test.go:218),
`feed_cache_test.go`, `incidents_cache_test.go`,
`statuspagecache_test.go`, and the `Cache-Control` sentence in the OpenAPI
descriptions of the public operations.

## Tests

1. **Hit**: two `ViewStatusPage` calls within the TTL against a counting fake
   DB → the second runs no results / sections / checks query and returns an
   equal response.
2. **Expiry**: with an injected clock, a call after the TTL recomputes.
3. **Gate first**: warm the memo with an unlocked request on a `password`
   page, then request without the cookie → `401`, and the counting fake shows
   no query beyond the page lookup.
4. **Key variants**: `include=` and the default shape are separate entries;
   the default-page and slug paths for the same page share one.
5. **Invalidation**: table-driven over every write path listed above — warm,
   write, read → recomputed. A missing row in that table is what this test
   is for; make the table the single list of invalidators, referenced from the
   service so a new write path without an entry fails to compile or fails the
   test.
6. **Singleflight**: 50 goroutines miss at once against a fake whose compute
   blocks on a channel → exactly one computation, all 50 get the result; an
   erroring compute is retried by the next caller.
7. **Summary and badge** share the memo: a badge render after a summary read
   runs no enrichment.
8. Header pins updated as above; a test that a gated response still carries
   no `stale-while-revalidate`.

## Docs

- `wiki/features/status-pages.md` (or the closest page describing the public
  reads): the memo, its TTL, the invalidation list, the "gate first" rule.
- `statuspagecache` package doc: the two freshness constants and the SWR
  reasoning, in the same voice as the existing `Vary` discussion.
- Changelog entry.

## Acceptance

On the dev deployment: the second request for the 200-resource page within
15 s answers in under 50 ms server-side (`HTTP request … duration` in the
log); ten concurrent cold requests produce one slow-query burst, not ten.

## Out of scope

- A cross-replica cache. The memo is per process; with several API replicas
  each warms independently, which is fine at 15 s.
- Memoizing anything authenticated.

## Implementation Plan

1. **Freshness constant.** `statuspagecache.PageMemoTTL = 15 * time.Second`
   declared next to `PageMaxAge`, with the package doc explaining why the two
   numbers belong side by side.
2. **The memo itself** (`statuspages/memo.go`). `memoKey{product, orgUID,
   pageUID, availability, responseTime}` — comparable, so it is a map key and
   its string form is the singleflight key. `pageMemo` holds the map, a
   `singleflight.Group` and an injectable clock. Lazy TTL eviction on read, a
   sweep of the oldest entries on insert above 256, `invalidate(pageUID)` /
   `invalidateOrg(orgUID)` as map deletes. Values are stored as `any` and
   returned through a generic `memoDo[T]`, so the two products share one map
   without two parallel implementations. A nil `*pageMemo` computes straight
   through — the `&Service{}` literals in tests must keep working.
3. **Gate-first ordering.** Split `ViewStatusPage` into the *gate* (org lookup,
   page lookup, `publicAccessError`, `maybeReconcileOnView`) and a
   `computeStatusPageView` body, with the memo wrapping only the body. Same
   split for `viewStatusPageSummary`. Nothing that can deny a request may sit
   inside the memoized closure, and nothing memoized may be reachable without
   the gate having returned nil first.
4. **Invalidation table.** `statuspages/memo_invalidation.go` declares one
   `PageMemoWritePath` constant per write path plus the file/function that must
   carry the eviction. The service's own paths pass their constant to
   `s.invalidatePageMemo(path, pageUID)`, so the table is referenced from the
   code rather than only from a test. Foreign packages
   (`statuspageassets`, `incidentpublications`, `statusupdates`) reach it
   through a `PageMemoInvalidator` interface (`Invalidate` / `InvalidateOrg`)
   declared locally and injected in `server.go` exactly where
   `SetPublicIncidentProvider` already is.
5. **Selector reconcile.** `reconcilePage` learns to report whether it wrote
   anything, so the read-path backstop evicts only on a real change. Because it
   runs before the memo is consulted, the eviction is always visible to the
   read that triggered it.
6. **Metrics.** `solidping_statuspage_memo_hits_total` / `_misses_total` /
   `_singleflight_shared_total` in `prommetrics`, with recording helpers, wired
   into `allCollectors`.
7. **`stale-while-revalidate`.** `statuspagecache.Control` for a public page
   becomes `public, max-age=60, stale-while-revalidate=30`; gated stays
   `private, no-store`. Update the exact-string pins in `cache_control_test.go`,
   `feed_cache_test.go`, `incidents_cache_test.go`, `statuspagecache_test.go`
   and the OpenAPI `Cache-Control` sentences.
8. **Tests** — the spec's eight, in `statuspages/memo_test.go` (hit, expiry,
   gate-first, key variants, invalidation table, singleflight, summary/badge
   sharing) plus the header pins, against a counting `db.Service` wrapper.
9. **Docs.** `wiki/features/status-pages.md`, the `statuspagecache` package doc,
   changelog.
