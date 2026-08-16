# DNSBL checker misreads Spamhaus `127.255.255.x` error codes as listings

## Problem

The DNSBL checker reports a target as **Down** whenever a blocklist zone returns
*any* A-record, but the `127.255.255.0/24` range is reserved by Spamhaus (and
other DNSBLs) for **error/status replies**, not real listings. Treating those as
listings produces false "blocklisted" alerts and flapping checks.

Observed in production: check `api.acme.io (dnsbl)`
(`14ccfb28-d549-4992-bfbd-6f5a69a91958`, org `acmetech`) sits **Down** with:

```json
"clean":     ["b.barracudacentral.org", "bl.spamcop.net", "dnsbl-1.uceprotect.net"],
"listed_on": ["zen.spamhaus.org"],
"return_codes": { "zen.spamhaus.org": ["127.255.255.254"] }
```

Three of four lists are clean; the only "hit" is `zen.spamhaus.org` returning
`127.255.255.254`. That code is **not** a listing — it is Spamhaus's
"query came through a public/open DNS resolver, refused" reply. solidping's
workers run in AWS and use the default VPC / public resolver, so Spamhaus
refuses to serve `zen.spamhaus.org` results and returns the error code. Real
listings live in `127.0.0.x` (`.2` SBL, `.3` CSS, `.4–.7` XBL, `.9` DROP,
`.10/.11` PBL). `api.acme.io` is not actually listed.

Spamhaus reserved error/status codes:

| Code | Meaning |
|---|---|
| `127.255.255.252` | Typo / malformed DNSBL zone name |
| `127.255.255.254` | Query via public/open resolver — refused |
| `127.255.255.255` | Query volume / rate limit exceeded |

The bug is in the classification loop
([`checker.go:121-131`](server/internal/checkers/checkdnsbl/checker.go:121)):

```go
case lookupErr == nil && len(addrs) > 0:
    listedSet[zone] = true
    returnCodes[zone] = appendUnique(returnCodes[zone], addrs)
```

Any non-empty answer → `listedSet`, and a single listed zone forces
`StatusDown` via
[`classifyStatus`](server/internal/checkers/checkdnsbl/checker.go:175). There is
no special-casing of the `127.255.255.x` error range. Because the refusal only
happens for *some* probes (depending on which resolver a worker lands on), the
check also **flaps** up/down between periods.

## Proposal

Stop counting DNSBL error/status replies as listings.

1. **Filter the `127.255.255.0/24` error range** in the classification loop.
   When a zone's answer set contains *only* addresses in `127.255.255.0/24`,
   classify the zone as **inconclusive** (not `listed`), and surface the code so
   operators can see why. When a zone returns a mix, keep only the genuine
   `127.0.0.x`-style listing addresses in `listed_on` / `return_codes` and drop
   the error codes. A helper such as `isDNSBLErrorCode(addr string) bool`
   (true for `127.255.255.252`–`127.255.255.255`, or the whole
   `127.255.255.0/24` block) keeps the intent explicit.

2. **Result surfacing.** Record the error codes in output (e.g. an
   `errors`/`refused` map alongside `return_codes`) so a `127.255.255.254`
   refusal is visible as "resolver refused", not hidden. A check whose only
   non-clean zones are error replies should not be `Down`; with the fix above it
   becomes `Up` when ≥1 zone is genuinely clean, or `Timeout` when every zone is
   inconclusive (existing `classifyStatus` semantics,
   [`checker.go:175-187`](server/internal/checkers/checkdnsbl/checker.go:175)).

3. **Docs / config note.** Reliable Spamhaus results from cloud IPs require the
   Spamhaus **Data Query Service (DQS)** with an account key zone rather than the
   public `zen.spamhaus.org`. The checker already supports a custom `nameserver`
   / zone config — document that DQS is the recommended setup for Spamhaus zones,
   and that public `zen.spamhaus.org` from a cloud resolver will be refused.

4. **Tests.** Add table cases to
   [`checker_test.go`](server/internal/checkers/checkdnsbl/checker_test.go):
   - zone returns only `127.255.255.254` → zone inconclusive, not listed; overall
     status driven by the other zones (Up when another zone is clean).
   - zone returns only `127.255.255.255` and `127.255.255.252` → same.
   - zone returns a real `127.0.0.2` → still `listed` / Down (no regression).
   - mixed `127.0.0.4` + `127.255.255.254` → listed on the real code only.

## Open questions

- Should the whole `127.255.255.0/24` block be treated as error, or only the
  documented `.252`/`.254`/`.255`? Blanket-treating the /24 is safer and matches
  common DNSBL client behavior; no legitimate DNSBL uses `127.255.255.x` for a
  real listing.
- Do we want a distinct check status (e.g. a `warning`/"misconfigured") for
  "all zones refused" instead of folding into `Timeout`? Out of scope unless we
  want operators paged differently for refusals vs. genuine timeouts.

## Implementation Plan

All changes are confined to the backend `checkdnsbl` package plus one docs note.

1. **Error-code helper** (`checker.go`). Add `isDNSBLErrorCode(addr string) bool`,
   true for any address in `127.255.255.0/24` (blanket per the open-question
   resolution — no legitimate DNSBL uses `127.255.255.x` for a real listing).
   Checks the first three octets `127.255.255` on the IPv4 form. Add a
   `partitionDNSBLAddrs(addrs []string) ([]string, []string)` helper that splits a
   zone's answer set into genuine listing addresses and reserved error codes.

2. **Classification loop** (`checker.go` Execute). When a zone answers with a
   non-empty address set, partition it:
   - genuine listings present → `listedSet[zone] = true`, `return_codes[zone]`
     records only the genuine listing addresses (error codes dropped).
   - only error codes → treat the zone as **inconclusive**, not listed.
   - error codes (any) → record them in a new `error_codes[zone]` map so the
     `127.255.255.254` refusal is visible to operators.
   This makes a check whose only non-clean zone is an error reply become `Up`
   (when ≥1 zone is genuinely clean) or `Timeout` (when every zone is
   inconclusive), via the existing `classifyStatus` semantics — no change there.

3. **Result surfacing** (`checker.go` Execute). Add `error_codes` to `result.Output`
   when non-empty, alongside the existing `return_codes`.

4. **Docs** (`web/docs/docs/features/check-types.md`). Note in the DNSBL section
   that `127.255.255.x` replies are treated as error/status codes (not listings),
   and that reliable Spamhaus results from cloud IPs require the Spamhaus Data
   Query Service (DQS) account-key zone via the `nameserver`/`blocklists` config
   rather than the public `zen.spamhaus.org`, which refuses queries from public
   cloud resolvers.

5. **Tests** (`checker_test.go`). Add table cases:
   - zone returns only `127.255.255.254` → inconclusive, not listed; overall
     status driven by other zones (Up when another zone is clean), and
     `error_codes` surfaces the code.
   - zone returns only `127.255.255.255` + `127.255.255.252` → same.
   - zone returns real `127.0.0.2` → still listed / Down (no regression).
   - mixed `127.0.0.4` + `127.255.255.254` → listed on the real code only, error
     code surfaced separately.
   Add a direct `isDNSBLErrorCode` unit test covering boundary addresses.

## QA
- `make build-backend lint-back test` (Go-only change).
