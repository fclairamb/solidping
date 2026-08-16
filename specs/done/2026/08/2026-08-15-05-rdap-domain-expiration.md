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

## Implementation Plan

### Backend

1. **`server/internal/checkers/checkdomain/rdap.go` (new)** — small internal
   RDAP client, no new dependency:
   - `rdapClient` struct: `httpClient *http.Client`, `bootstrapURL string`,
     `cacheTTL time.Duration`, plus a mutex-guarded `bootstrap *bootstrapDoc` /
     `bootstrapAt time.Time` cache. A package-level `defaultRDAPClient` singleton
     (`bootstrapURL = "https://data.iana.org/rdap/dns.json"`, TTL 24h, 10s HTTP
     client) is what production `DomainChecker` instances use — this lives at
     package scope (not on the `DomainChecker` struct) because
     `registry.GetChecker` constructs a fresh `&DomainChecker{}` on every call,
     so an instance-level cache would never actually be reused.
   - `bootstrapDoc.Services [][][]string` decodes IANA's
     `[[tlds...],[urls...]]` service-array shape directly.
   - `lookup(ctx, domain) (*rdapResult, error)`: extract the TLD, resolve it to
     a base URL via the (cached) bootstrap doc, `GET {base}/domain/{name}`
     with `Accept: application/rdap+json` via `http.NewRequestWithContext`
     (context honored, redirects followed by the default `http.Client`).
   - Parse `events[]` for `eventAction == "expiration"` → RFC3339 `eventDate`;
     parse the registrar name from the `entities[]` entry with role
     `"registrar"` via its `vcardArray` `"fn"` property.
   - Distinct sentinel errors (`errRDAPUnknownTLD`, `errRDAPNoExpirationEvent`)
     so `auto` mode's fallback logic and error-result messages can
     distinguish failure modes; err113-compliant (declared `errors.New` vars,
     wrapped with `%w`).
2. **`config.go`** — add `Method string` (`json:"method,omitempty"`) to
   `DomainConfig`, `auto`/`rdap`/`whois` constants, `FromMap`/`GetConfig`
   wiring, and `Validate` rejecting anything outside `{"", auto, rdap,
   whois}` (empty stays the zero value = auto, so existing checks are
   untouched).
3. **`checker.go`** — inject testability without changing production
   wiring: unexported `rdapClient *rdapClient` and `whoisFunc func(string)
   (string, error)` fields on `DomainChecker`, both defaulting (nil) to
   `defaultRDAPClient.lookup` / `whois.Whois` inside `Execute`. Dispatch on
   `cfg.Method` (auto → try RDAP, on ANY error fall back to WHOIS; rdap →
   RDAP only, error result on failure, no fallback; whois → today's path
   unchanged, RDAP never invoked). Honor `ctx` for the RDAP path (was
   previously ignored entirely). Keep `days_remaining`/`expiry_date`/
   `registrar`/threshold logic byte-for-byte identical; add `"method":
   "rdap"|"whois"` to `Output`.
4. Update the WHOIS-only description in `web/docs/docs/features/check-types.md`
   (Domain Expiration section) to describe RDAP-first/WHOIS-fallback and
   document the `method` option. Leave `checkerdef/types.go`'s
   `CheckTypeDomain` description untouched (doesn't mention WHOIS). Light-touch
   fix the same wording in `server/CLAUDE.md` and `wiki/architecture.md` since
   they're one-liners in the neighborhood.
5. **Tests (`checker_test.go`)** — `httptest.NewServer` fake serving both the
   bootstrap doc and one or more `/domain/{name}` responses, `rdapClient`
   pointed at it via the injected field:
   - RDAP success (method auto and method rdap)
   - RDAP failure → WHOIS fallback (method auto), with a positive control
     (RDAP success + method auto asserts the fallback whois func was NOT
     called, e.g. via a call-counting stub) proving the fallback path isn't
     vacuously passing
   - unknown TLD (bootstrap doc has no entry) → auto falls back, rdap errors
   - missing expiration event in the RDAP response → auto falls back, rdap
     errors
   - `method: whois` forces the legacy path and never touches the RDAP
     client (assert via call counter / a client that errors if hit)
   - `method: rdap` does NOT fall back to WHOIS on failure
   - context cancellation: pre-cancelled `context.Context` + `method: rdap`
     surfaces a context error in the result, never falls back (avoids a real
     WHOIS network call in the failure path)

### Frontend (`web/dash0/src/components/checks/form/types/dns.tsx` + `index.ts`)

6. Extend `DomainState` with `method: string` (`""` | `"auto"` | `"rdap"` |
   `"whois"`); `fromConfig` reads it via `getConfigField(config, "method")`;
   `toConfig` writes `cfg.method` only when non-empty/non-"auto" (so the
   default serializes to nothing, matching the backend's zero-value = auto).
7. Add `DomainAdvancedFields` (a `Select` with Auto (default) / RDAP / WHOIS
   options, mirroring `HttpOptionsFields`'s shape) and
   `domainAdvancedSummary` (mirroring `httpOptionsSummary`: summary text +
   `customized` only when `method` is set to something other than
   `""`/`"auto"`) in `dns.tsx`.
8. Register `domain: { Fields: DomainAdvancedFields, summary:
   domainAdvancedSummary }` in `advancedFieldsRegistry` in `index.ts`, adding
   the two new imports from `./dns`.
9. Add/extend a Playwright spec asserting the Advanced section on a Domain
   check exposes the method select, defaults to Auto, and round-trips a
   non-default selection through save/reload (author-only if the local
   `:4000` devloop can't run E2E in `SP_RUNMODE=test`).
