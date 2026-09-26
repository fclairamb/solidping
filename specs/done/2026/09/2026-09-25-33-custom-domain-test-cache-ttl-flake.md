---
model: sonnet
effort: medium
---

# TestHandlerWithCustomDomains fails when its parallel subtests start more than 60s after the cache was seeded

## Problem

`TestHandlerWithCustomDomains` (`server/internal/app/custom_domain_routing_test.go:291`)
is flaky under `make test` (full `go test ./... -short`). On 2026-09-25 it failed 14
subtests at once: every response fell through to the `next` handler (body `"next"`,
does not contain `content="acme/main"`, 200 instead of 404/503). Re-running
`go test -short -count=1 ./internal/app/` passed twice. Per repo policy a flaky test
is a bug to root-cause, not something to re-run.

Likely root cause, a wall-clock TTL racing test scheduling:

- `newCustomHostTestServer` (`custom_domain_routing_test.go:217`) builds the server
  with `newCustomDomainCache(customDomainCacheTTL)` (line 258) and seeds four hosts
  with `customDomainCache.set` (lines 264-284): `status.acme.com`,
  `unknown.example.com`, `locked.acme.com`, `demoted.acme.com`.
- `customDomainCacheTTL` is 60s (`custom_domain_routing.go:25`). `set` stamps
  `expiresAt: time.Now().Add(c.ttl)` and `get` treats `time.Now().After(expiresAt)`
  as a miss (`custom_domain_routing.go:127-142`).
- The parent test seeds the cache in its own body, then its subtests call
  `t.Parallel()` (line 434). Parallel subtests only start after the parent body
  returns and a `-parallel` slot frees up. Under a full `go test ./...` on a loaded
  machine that can be more than 60s later (the `internal/app` package took 146s in
  the failing run). The seeded entries have expired, the lookup misses, the test
  server has no DB to resolve against, and every host is treated as unknown, so the
  request falls through to `next`.

The same pattern (seed in the parent, run in parallel subtests) exists in:

- `TestCustomHostLegacyStatusPrefixRedirects` (line 135, subtests at 158-159)
- `TestCustomHostLegacyDashPrefixIs404` (line 184, subtests at 194-195)

Other callers of `newCustomHostTestServer` use it without parallel subtests, so the
seed-to-request gap is tiny, but they still depend on wall-clock time in principle:
`TestCustomHostShellCacheControl` (461), `TestPathBasedShellVaryMatchesCustomHost`
(557), `TestLookupCustomDomainDistinguishesDemotedFromUnknown` (598),
`TestCustomDomainUnavailablePageIsLegibleAndUncached` (640), and
`TestServeDemoShortcutCustomHost404s` in `server/internal/app/demo_shortcut_test.go:66`.

## Proposal

1. **Reproduce first.** Before changing anything, make the failure deterministic,
   e.g. temporarily seed with a tiny TTL, or add a `time.Sleep` longer than the TTL
   in the parent body before the `t.Run` loop. Confirm the same symptom as the
   2026-09-25 run (`"next"`, 200 instead of 404/503). Record the command and output
   in the commit message or PR description. Do not commit the reproduction hack.

2. **Remove the wall-clock dependency from the tests.** Pick one approach and apply
   it to every helper/test above:
   - Preferred: give `customDomainCache` an injectable clock (`now func() time.Time`,
     defaulting to `time.Now`) and have `newCustomHostTestServer` pin it, so entries
     never expire during a test. This also enables a direct unit test of expiry.
   - Or: build the test cache with a TTL that cannot elapse in a test run (e.g. a
     named test constant of 24h), keeping production code untouched.
   - Or: seed per subtest (build the server inside each `t.Run`), which removes the
     parent-to-subtest gap but still leaves a wall-clock TTL in place.
   Production behaviour (`customDomainCacheTTL` = 60s, positive and negative caching)
   must not change.

3. **Prove the fix holds.** Re-apply the step 1 reproduction on top of the fix and
   show the tests pass (for the clock approach: sleep past 60s or advance real time
   and show entries are still served). Then remove the hack.

4. If the clock is injected, add a small unit test for `customDomainCache` expiry
   (set, advance the fake clock past the TTL, expect a miss; before it, expect a hit)
   so the TTL itself stays covered.

5. Follow `server/CLAUDE.md`: testify `require`, `t.Parallel()` everywhere it is
   today, `make lint-back` clean, `make test` green.

## Open questions

- Is there a second, unrelated cause? If the step 1 reproduction does not produce
  the exact 2026-09-25 symptom, stop and investigate further rather than shipping
  the TTL change as the fix.

## Resolved open questions

- **Is there a second, unrelated cause?** Reproduce first (step 1). Ship the TTL/clock fix only
  if the reproduction produces the exact 2026-09-25 symptom (`"next"` body, 200 instead of
  404/503). If it does not, investigate further, find and fix the real cause, and document it in
  the commit body. Never ship the TTL change as "the fix" for a symptom it does not reproduce.
