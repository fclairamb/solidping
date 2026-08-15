---
model: sonnet
effort: high
---

# Check edit page: docs link is generic, domain expiration has no warning/critical days, and periods stop at 24h

## Problem

Three related gaps on the check create/edit page
(`/orgs/$org/checks/$uid/edit`), reported while editing a domain-expiration
check:

1. **The docs link always points to the generic check-types page.** The check
   form renders a single static
   `<DocsLink href="/docs/features/check-types" />`
   ([check-form.tsx:852](web/dash0/src/components/shared/check-form.tsx:852)),
   regardless of the selected check type. The embedded docs page
   [check-types.md](web/docs/docs/features/check-types.md) already has one
   `###` heading — hence one anchor — per check type (e.g.
   `### Domain Expiration` at line 201 → `/docs/features/check-types#domain-expiration`),
   so a domain check should link straight there instead of the top of the page.

2. **Domain expiration checks cannot set warning/critical days.** The
   dashboard's domain form module only exposes `domain` and `method`
   ([dns.tsx:252](web/dash0/src/components/checks/form/types/dns.tsx:252)) —
   no threshold field at all. Backend-side, `DomainConfig` has a single
   `ThresholdDays` (default 30) that goes straight to down
   ([checkdomain/config.go](server/internal/checkers/checkdomain/config.go)),
   while the SSL check already has the full two-tier pattern:
   `warningDays`/`criticalDays` with a legacy `thresholdDays` alias
   ([checkssl/config.go:40](server/internal/checkers/checkssl/config.go:40)),
   `validateThresholds` (warning ≥ critical ≥ 0), and the shared
   `GradedExpiryStatus` helper — whose doc comment explicitly says
   "checkssl today; **checkdomain in a follow-up**"
   ([checkerdef/expiry.go](server/internal/checkers/checkerdef/expiry.go)).
   This spec is that follow-up.

3. **Check frequency cannot be set beyond 24 hours.** The interval dropdown's
   ladder tops out at `24:00:00`
   ([check-form.tsx:191](web/dash0/src/components/shared/check-form.tsx:191)
   `buildIntervalOptions`), yet the domain type declares `MinPeriod: 6h`,
   `DefaultPeriod: 24h` and **no MaxPeriod cap**
   ([checkerdef/types.go:298](server/internal/checkers/checkerdef/types.go:298)).
   For slow-moving checks like domain expiration, users want e.g. every
   2 weeks or every month.

## Proposal

### 1. Per-type docs anchors

- Map each check type to its anchor on `/docs/features/check-types` and make
  the check form's `DocsLink` use `#<anchor>` for the currently selected
  type (falling back to the bare page for unknown/unmapped types).
- Docusaurus auto-generates anchors from heading text (`### HTTP/HTTPS` →
  `#httphttps`), which is fragile. Prefer pinning explicit heading IDs in
  `check-types.md` (`### Domain Expiration {#domain-expiration}`) — keeping
  the already-published auto-generated slugs (the user links
  `#domain-expiration` today) so no existing deep link breaks.
- "Sync each check type with the correct anchor": add a test that fails when
  the mapping drifts — e.g. a Go or unit test that walks the
  `checkTypesRegistry` type list and asserts every monitorable type has a
  matching `{#anchor}` in `web/docs/docs/features/check-types.md` (and that
  every mapped anchor exists). Passive/synthetic types with no docs section
  can be explicitly exempted in the test.

### 2. Domain expiration warning/critical days

- Port the checkssl threshold pattern to `checkdomain`, using the shared
  `GradedExpiryStatus`: `warningDays` (amber `StatusWarning`, counts as up,
  no incident) and `criticalDays` (`StatusDown`, pages).
- Keep backward compatibility exactly like checkssl did: existing configs
  store `threshold_days` — treat it as the legacy alias for `criticalDays`
  when `criticalDays` is absent, and keep decoding it. Same validation:
  warning ≥ critical ≥ 0, sane upper bound.
- Dashboard: add Warning (days) / Critical (days) inputs to the domain form
  module, mirroring the SSL fields in
  [misc.tsx:118](web/dash0/src/components/checks/form/types/misc.tsx:118)
  (same labels, placeholders showing the defaults).
- Update the Domain Expiration section of the docs page accordingly.

### 3. Longer check periods

- Extend `buildIntervalOptions` with long-horizon options: **1 week**
  (`168:00:00`), **2 weeks** (`336:00:00`), and **30 days** (`720:00:00`).
  They only surface for types whose `MaxPeriod` allows them (0 = uncapped),
  via the existing min/max filter — no per-type special-casing needed.
- Verify (and fix if needed) that multi-day periods survive the whole path:
  - the `HH:MM:SS` helpers (`hmsToSeconds`/`secondsToHMS`, `formatPeriod`)
    with 3-digit hour values;
  - backend period parsing/validation and the scheduler for periods ≫ 24h;
  - downstream consumers that assume "roughly daily at most": staleness /
    missing-result detection, confirmation & recovery estimates, results
    aggregation buckets, availability math. Add tests for a 2-week-period
    check rather than assuming.

### Open questions

- Should very-long options (≥ 1 week) be offered on every uncapped type
  (http every 30 days is legal but odd), or should fast types get a
  `MaxPeriod` in the registry? Default proposal: offer them wherever the
  registry allows; tighten `MaxPeriod` per type only if something breaks.
- Whether the docs-anchor mapping should live frontend-side (a small
  `type → anchor` map next to the form) or be served in the check-type
  metadata API. Default proposal: frontend map + sync test — no API change.

## Resolved open questions

Both questions are settled by this spec's own stated default proposals. They
are directives, not choices — implement them as written:

> Should very-long options (≥ 1 week) be offered on every uncapped type
> (http every 30 days is legal but odd), or should fast types get a
> `MaxPeriod` in the registry?

**Decision:** Offer the long options wherever the registry already allows them
— i.e. purely via the existing min/max filter, with no per-type special-casing.
Do **not** add or tighten `MaxPeriod` on any type as part of this spec; only
revisit that if something concretely breaks.

> Whether the docs-anchor mapping should live frontend-side (a small
> `type → anchor` map next to the form) or be served in the check-type
> metadata API.

**Decision:** Frontend map plus the drift-detecting sync test. Do **not**
change the check-type metadata API.
