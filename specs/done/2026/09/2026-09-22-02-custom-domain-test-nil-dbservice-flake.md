---
model: sonnet
effort: medium
---

# `TestHandlerWithCustomDomains` can segfault on a cache-miss fallthrough to a nil `dbService`

## Problem

`TestHandlerWithCustomDomains` (`server/internal/app/custom_domain_routing_test.go:288`)
panics intermittently with a nil pointer dereference in
[`lookupCustomDomain`](server/internal/app/custom_domain_routing.go:235), at
`s.dbService.GetStatusPageByCustomDomain(ctx, host)`.

Observed once during a full `make test` run on 2026-09-22, in the subtest
`custom_host_root_serves_status0_index_with_sp-page`, and green on every isolated
re-run of the package since. Nothing in `internal/app` was modified by the change
that surfaced it — this is a pre-existing latent bug, not a regression.

The test builds a `Server` (around line 255) with `dbService` left as its zero value
(nil) and pre-seeds `s.customDomainCache` with the exact hosts each subtest expects
to hit, so [`resolveCustomDomain`](server/internal/app/custom_domain_routing.go:221)
is meant to always hit the cache and never call `lookupCustomDomain` → `s.dbService...`.

The subtests run under `t.Parallel()` (line 289, and each `t.Run` again at line 422).
`resolveCustomDomain` is a cache with a TTL
(`newCustomDomainCache(customDomainCacheTTL)`, line 255) — if a lookup for some reason
misses the pre-seeded entry (TTL expiry under parallel scheduling, an eviction, or a
host that was never seeded for a given subtest/table case), the code falls through to
`s.lookupCustomDomain`, which dereferences the nil `dbService` and panics the whole
test binary.

## Proposal

Treat this as a real bug in test setup / production nil-safety, not a scheduling
fluke to shrug off:

1. Confirm the root cause: audit every subtest/table case under
   `TestHandlerWithCustomDomains` and verify each host it requests a response for is
   actually present in the pre-seeded `customDomainCache`, and that `customDomainCacheTTL`
   can't plausibly expire an entry mid-run under `t.Parallel()`. Reproduce with:
   ```
   go test ./internal/app/ -run TestHandlerWithCustomDomains -count=50 -short
   ```
   (loop/race flags as needed) until the failure reproduces, or is confirmed absent
   after the fix below.

2. Fix defensively in `lookupCustomDomain` / `resolveCustomDomain`
   (`server/internal/app/custom_domain_routing.go:218-230`): make a nil `s.dbService`
   fail loudly and safely rather than segfault — e.g. an explicit guard that returns
   `customDomainResolution{}` (or logs/panics with a clear message identifying the
   unseeded host) instead of an unguarded nil-pointer dereference. A production
   `Server` should never have a nil `dbService`, but a test double must not be able to
   crash the whole binary on a cache miss it didn't anticipate — prefer a fast, legible
   test failure over a segfault.

3. Alternatively/additionally, give the test a minimal stub `dbService` (there is
   already a pattern for this elsewhere in the file, e.g. `dbSvc` at line 499) so a
   cache miss exercises the real DB path deterministically instead of crashing —
   whichever approach better matches the existing test's intent (verifying cache-only
   behavior vs. also covering the DB fallback).

4. Verify the fix with repeated and parallel/race runs:
   ```
   go test ./internal/app/ -run TestHandlerWithCustomDomains -count=50 -short
   go test ./internal/app/ -run TestHandlerWithCustomDomains -race -short
   ```
