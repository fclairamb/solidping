---
model: sonnet
effort: medium
---

# `maxCustomDomains` is enforced but invisible — surface it on the Usage page and in the wiki

## Problem

Custom domains for status pages must be a paid feature: the number of custom
domains an org may use is an entitlement, and the **Free plan gets zero**.

The backend half of that already shipped:

- `EntitlementLimits.MaxCustomDomains` (`maxCustomDomains`, nil = unlimited)
  exists in [`entitlements_payload.go:44-47`](server/internal/db/models/entitlements_payload.go).
- SaaS default is `0`, self-hosted stays nil/unlimited —
  [`defaults.go`](server/internal/entitlements/defaults.go)
  (`defaultMaxCustomDomainsSaaS = 0`).
- The resolver merges it ([`service.go:387`](server/internal/entitlements/service.go)),
  the admin PATCH overlays it
  ([`handlers/entitlements/handler.go:317`](server/internal/handlers/entitlements/handler.go)),
  `Usage.CustomDomains` is counted
  ([`usage.go:34,71`](server/internal/entitlements/usage.go)), and
  `CustomDomainAllowed` gates the "page had no domain, now gets one" transition
  ([`custom_domain.go:208-212`](server/internal/handlers/statuspages/custom_domain.go)),
  returning a 402 the domain editor already renders as a quota alert
  ([`status-page-custom-domain.tsx:56,72,149`](web/dash0/src/components/shared/status-page-custom-domain.tsx)).

What is missing is every place a user or operator would go to *understand* the
limit:

1. **The Usage page shows four rows and not this one.**
   [`organization.usage.tsx:95-121`](web/dash0/src/routes/orgs/$org/organization.usage.tsx)
   renders checks, checks/minute, users and private-location agents. A customer
   who hits the 402 in the domain editor has nowhere to see "0 / 0 custom
   domains" or how many their plan allows.
2. **The dash0 API types don't even carry the field.**
   `EntitlementsLimits` / `EntitlementsUsage` in
   [`hooks.ts:4053-4066`](web/dash0/src/api/hooks.ts) stop at
   `maxDeportedAgents` / `agents`, so the data the backend already returns is
   dropped on the floor.
3. **The wiki never mentions it.** `grep -rn maxCustomDomains wiki/` returns
   nothing: [`wiki/features/entitlements.md`](wiki/features/entitlements.md)
   (limits table, per-mode defaults table),
   [`wiki/api-specification/entitlements.md:54`](wiki/api-specification/entitlements.md)
   (wire field list) and
   [`wiki/database-model/entitlements.md:17`](wiki/database-model/entitlements.md)
   (payload key list) all stop at `maxDeportedAgents`.

The companion plan-ladder change lives in `../solidping-billing` and is filed
there as its own spec (`2026-07-28-01-custom-domains-entitlement.md`).

## Proposal

### 1. Dash0 API types

In [`web/dash0/src/api/hooks.ts`](web/dash0/src/api/hooks.ts):

- `EntitlementsLimits` gains `maxCustomDomains?: number | null;` with a doc
  comment ("Cap on status pages served on a customer-owned domain. null =
  unlimited.").
- `EntitlementsUsage` gains `customDomains: number;` — matching the backend's
  `usage.customDomains` key exactly.

### 2. Usage page row

In [`organization.usage.tsx`](web/dash0/src/routes/orgs/$org/organization.usage.tsx),
add a fifth `UsageRow` after "Private location agents":

- label `t("usage.customDomains")`, value `usage.customDomains`,
  limit `limits.maxCustomDomains`, `unlimitedLabel={t("usage.unlimited")}`.
- Add `usage.customDomains` ("Custom domains") to all four locale files
  (`web/dash0/src/locales/{en,de,fr,es}/org.json`), following the existing
  `usage.privateLocationAgents` entry.
- A cap of `0` must render as `0 / 0` (not as "unlimited") — verify
  `UsageRow`'s limit rendering treats `0` as a real cap and not as falsy /
  missing. If it doesn't, fix the nullish check (`limit == null`, not
  `!limit`). This is the whole point of the Free tier being 0, so it needs a
  test.

### 3. Wiki

- [`wiki/features/entitlements.md`](wiki/features/entitlements.md): add
  `maxCustomDomains` to the limits table (enforcement pointer →
  `CustomDomainAllowed` in `entitlements/usage.go`, called from
  `statuspages/custom_domain.go`), note the "only the none→some transition is
  gated; swapping an existing domain is free" semantics, and add the column to
  the per-mode defaults table (SaaS `0`, self-hosted unlimited).
- [`wiki/api-specification/entitlements.md`](wiki/api-specification/entitlements.md):
  add the `limits.maxCustomDomains` row and the `usage.customDomains` row.
- [`wiki/database-model/entitlements.md:17`](wiki/database-model/entitlements.md):
  add `maxCustomDomains` to the payload key list.
- Mention the SaaS plan ladder (Free 0, paid plans > 0) once, pointing at the
  billing repo as the authority — do not duplicate the numbers.

While at it, the same three wiki files are also missing `maxSmsPerMonth` /
`maxCallsPerMonth` (both already in the model). Adding those rows is a
one-line-each drive-by; do it in the same pass rather than leaving the tables
half-stale.

### 4. Tests

- dash0 E2E (`web/dash0/e2e/`, the existing entitlements/usage spec): bump the
  row-count assertion, assert the "Custom domains" label renders, and assert a
  `0` cap renders as `0 / 0` rather than "Unlimited".
- Backend: the counting/guard paths are already covered by
  [`custom_domain_usage_test.go`](server/internal/entitlements/custom_domain_usage_test.go)
  — no new Go tests unless the `0`-cap rendering fix reveals a backend gap.

## Non-goals

- No change to enforcement semantics (soft cap: an org that drops below its
  cap keeps its existing domains working; only *adding* a new one is blocked).
- No change to the wire format or payload version — the key already ships.
- The plan ladder itself (which paid plan gets how many) is the billing repo's
  spec, not this one.
