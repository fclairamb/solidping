---
model: opus
effort: medium
---

# The status-page summary and badge are cached for 60 s; the page itself, which costs far more, is not cached at all

## Problem

Three public status-page endpoints share the same visibility gate and the same
underlying data. Two of them set a cache header:

| endpoint | handler | `Cache-Control` |
|---|---|---|
| `GET …/status-pages/:org/:slug/summary` | [`:616`](server/internal/handlers/statuspages/handler.go#L616) | `public, max-age=60` |
| `GET …/status-pages/:org/:slug/badge` | [`:669`](server/internal/handlers/statuspages/handler.go#L668) | `public, max-age=60` |
| `GET …/status-pages/:org/:slug` (the page) | [`:567`](server/internal/handlers/statuspages/handler.go#L567) | **none** |

The uncached one is the expensive one. `ViewStatusPage` runs
`enrichWithAvailability`, which is `uptimebar.BucketAvailability` **plus**
`fetchRecentResults` — the latter measured at 662 ms on a 20-check page (spec
`2026-08-22-05`). Every anonymous visitor, every refresh, every crawler and
every uptime-monitor-monitoring-the-uptime-monitor pays it in full, and a
status page is by construction the page that gets hammered exactly when the
infrastructure behind it is already unhealthy.

With no `Cache-Control`, shared caches and CDNs must treat the response as
uncacheable, and browsers fall back to heuristic freshness — which for a
response with no `Last-Modified` means "revalidate every time".

### Why this is not a one-line change

`ViewStatusPage` serves all three visibilities
([`status_page.go:61`](server/internal/db/models/status_page.go#L61)):
`public`, `private`, and `password`. A blanket `Cache-Control: public,
max-age=60` would authorize **shared** caches — CDNs, corporate proxies — to
store and re-serve the body of a password-protected or private page to
somebody who never presented the password. That is a disclosure bug shipped in
the name of performance, and it is the reason this spec is not "copy line 642".

The data is also genuinely time-sensitive in one direction: a status page that
keeps showing "all systems operational" for a minute after an incident opens is
the product failing at its one job. 60 s matches what the summary and badge
already promise, and matches the aggregation cadence — but it must be a
deliberate choice, not an accident.

## Proposal

### 1. Cache directive follows visibility

| visibility | directive |
|---|---|
| `public` | `public, max-age=60` |
| `password` | `private, no-store` |
| `private` | `private, no-store` |

`no-store` rather than `no-cache` for the two gated cases: the concern is a
shared cache retaining the body at all, not staleness. Apply the same rule to
the **summary** and **badge** endpoints, which today send `public, max-age=60`
unconditionally and therefore carry a smaller version of the same bug — the
summary body contains the page name and per-status counts, and the badge
renders the rollup status, for a page the requester may not be entitled to see.

Derive the directive in one shared helper so a fourth public endpoint cannot
pick a different answer.

### 2. `Vary` where the response varies

The public page's rendering depends on request-derived inputs (language, and
the unlock cookie for password pages). Set `Vary` accordingly so a shared cache
cannot serve one visitor's variant to another. Enumerate the actual inputs
during implementation and pin them in a test rather than guessing a list here;
if the set turns out to be large enough that `public` caching cannot be made
safe, downgrade `public` pages to `private, max-age=60` and say so — a
browser-only cache still removes the repeat-visitor cost, which is most of it.

### 3. Do not add an ETag in this spec

An `ETag` over the serialized body would let revalidation return 304 and skip
the transfer — but **not** skip the query, since the body must be computed to
be hashed. It therefore does nothing for the cost this spec exists to reduce,
and it interacts with `Vary` and with the unlock cookie in ways that deserve
their own reasoning. Explicitly out of scope.

### Testing

- **Per-visibility headers.** For each of the three visibilities, assert the
  exact `Cache-Control` value on the page, summary and badge endpoints. The
  `password` and `private` cases are the ones that matter; assert the absence
  of the `public` token, not merely the presence of something.
- **Unlocked password page.** A request carrying a valid unlock cookie must
  still get `private, no-store`. Being authorized to see the page does not make
  it cacheable by a shared cache — this is the assertion most likely to be
  missed.
- **`Vary` pinning.** Assert the exact `Vary` header for a public page, so that
  adding a request-dependent field to the response without extending `Vary`
  fails a test rather than shipping a cross-visitor leak.
- **404 parity.** A private or disabled page still 404s exactly as it does
  today, with no cache header that would let the 404 — or its absence — be
  cached into an existence oracle.

### Acceptance

- The public page endpoint sends `public, max-age=60` for `public` pages.
- No response for a `private` or `password` page carries the `public` cache
  token, on any of the three endpoints, unlocked or not.
- Status changes still surface within 60 s.

## Out of scope

- Reducing the cost of the query itself — spec `2026-08-22-05`. Caching lowers
  how often it runs; it does not make it cheap, and a cold cache after a deploy
  or during a traffic spike is precisely when the page is being hit hardest.
- Server-side response caching, CDN configuration, and `stale-while-revalidate`.
  Worth considering once the correct client-facing directive exists to build on.
- `ETag` / conditional requests, per § 3.
