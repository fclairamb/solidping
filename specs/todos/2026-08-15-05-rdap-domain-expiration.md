---
model: sonnet
effort: high
---

# Domain expiration checks rely on WHOIS only — add RDAP support

## Problem

The `domain` check type looks up domain expiration exclusively through WHOIS
([checker.go:66](../../server/internal/checkers/checkdomain/checker.go) —
`whois.Whois(cfg.Domain)` + `likexian/whois-parser`). WHOIS has well-known
weaknesses that RDAP (RFC 7480–7484, the IANA-standardized successor) fixes:

- **Unstructured text**: WHOIS replies are free-form; the parser regularly fails
  or misses the expiration date on less common TLDs (we already have the
  "could not find expiration date in WHOIS data" error path at
  [checker.go:80](../../server/internal/checkers/checkdomain/checker.go)).
- **Rate limiting / blocking**: registry WHOIS servers throttle aggressively;
  RDAP endpoints are plain HTTPS and generally more tolerant.
- **Sunset pressure**: ICANN has been phasing WHOIS out for gTLDs in favor of
  RDAP (gTLD WHOIS sunset in 2025); WHOIS-only lookup gets more fragile over
  time.
- **No context support**: the current `whois.Whois` call ignores the
  `context.Context` passed to `Execute`, so cancellation/timeout comes only
  from the library's own defaults.

RDAP returns structured JSON over HTTPS (`GET {base}/domain/{name}`), with the
expiration date in the `events` array (`eventAction: "expiration"`) and the
registrar in `entities` — no fragile text parsing.

## Proposal

Add RDAP as the **preferred lookup method inside the existing `domain` check**,
with WHOIS as fallback — not a separate check type (users care about "is my
domain expiring", not the protocol; a second type would duplicate config, UI
and docs surface).

1. **RDAP client** — either use `github.com/openrdap/rdap` or write a small
   internal client (an internal client is likely simpler and dependency-free):
   - Bootstrap: fetch the IANA registry `https://data.iana.org/rdap/dns.json`,
     map the domain's TLD to its RDAP base URL. Cache the bootstrap document
     in-process (TTL ~24h) so per-check executions don't re-fetch it.
   - Query `GET {base}/domain/{domain}` (`Accept: application/rdap+json`),
     follow redirects, honor the passed `context.Context`.
   - Parse `events[]` for `eventAction == "expiration"` → `eventDate`
     (RFC 3339), and the registrar name from the entity with role
     `"registrar"`.
2. **Method selection** — add an optional `method` field to `DomainConfig`
   ([config.go:12](../../server/internal/checkers/checkdomain/config.go)):
   `auto` (default: RDAP first, WHOIS fallback on any RDAP failure — no
   bootstrap entry for the TLD, HTTP error, missing expiration event),
   `rdap`, or `whois` (current behavior). Keep zero-value behavior = `auto`
   so existing checks upgrade transparently.
3. **Result compatibility** — keep the existing metrics/output keys
   (`days_remaining`, `expiry_date`, `registrar`, threshold logic at
   [checker.go:89-99](../../server/internal/checkers/checkdomain/checker.go))
   unchanged so dashboards and alert rules keep working. Add
   `"method": "rdap"|"whois"` to `Output` so results show which path answered.
4. **Registration** — no new check type: `registry.go` and
   `checkerdef/types.go` entries for `CheckTypeDomain`
   ([types.go:296](../../server/internal/checkers/checkerdef/types.go)) stay
   as-is; only the checkdomain package changes. Update the type's description
   if it mentions WHOIS anywhere user-facing (docs, dash0 check form).
5. **Tests** — table-driven per `server/CLAUDE.md`: `httptest` fake serving a
   bootstrap document + RDAP domain responses; cases for RDAP success, RDAP
   failure → WHOIS fallback (fallback verified with a positive control),
   unknown TLD, missing expiration event, `method: whois` forcing the legacy
   path, and context cancellation.

### Open questions

- The request said "add a RDAP one" — if a *separate* `rdap` check type was
  actually intended (side-by-side with `domain`), say so before implementing;
  this spec deliberately folds RDAP into `domain` as the better product shape.
- Whether the dash0 check form should expose the `method` selector or keep it
  API-only initially (leaning: expose it as an "Advanced" select, default
  `auto`).

## Resolved open questions

> The request said "add a RDAP one" — if a *separate* `rdap` check type was
> actually intended (side-by-side with `domain`), say so before implementing;
> this spec deliberately folds RDAP into `domain` as the better product shape.

**Decision:** Fold RDAP into the existing `domain` check exactly as the
Proposal describes. Do **not** add a separate `rdap` check type — no new
`CheckTypeRDAP`, no new `registry.go` or `checkerdef/types.go` entry, no
second form/docs surface. RDAP is a lookup *method* inside `domain`, selected
by the new `method` field (`auto` default = RDAP first with WHOIS fallback,
plus `rdap` and `whois` to force a path). Existing `domain` checks must keep
working untouched with zero config changes.

> Whether the dash0 check form should expose the `method` selector or keep it
> API-only initially (leaning: expose it as an "Advanced" select, default
> `auto`).

**Decision:** Expose it in the dash0 check form as an **Advanced** select with
options `auto` / `rdap` / `whois`, defaulting to `auto`. Follow the existing
advanced/collapsible pattern already used by the domain check form, and build
it from the primitives catalogued in
`web/dash0/src/routes/orgs/$org/design-reference.tsx` — do not hand-roll a
raw Radix select. The field is optional everywhere: leaving it unset must
serialize to the zero value and behave as `auto`.
