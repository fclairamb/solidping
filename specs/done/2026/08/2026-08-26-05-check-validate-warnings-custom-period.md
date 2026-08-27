---
model: opus
effort: medium
---

# Field-by-field check validation with warnings, plus honest custom periods

## Problem

A validate endpoint **already exists** — `POST /orgs/:org/checks/validate`
(`server/internal/app/server.go:1005`, handler
`internal/handlers/checks/handler.go:93–107`, service `ValidateCheck` at
`internal/handlers/checks/service.go:420`) returning
`{ valid, fields: [{name, message}] }`. But it is errors-only and
first-error-only (`formatValidateError`, `service.go:680–694`, deliberately
"mirror legacy behavior — surface the first config error"), and it does not
cover the two things users actually trip on at configuration time:

- **Slug collisions** surface only as a 409 on submit, not during editing.
- **Going over the org's `MaxChecksPerMinute`**: nothing tells you that the
  period/regions you are about to save will push the org past its cap and
  get executions skipped (the 2026-08-26 `public`-org incident).

Separately, the period dropdown lies about non-standard values: the select
at `web/dash0/src/components/shared/check-form.tsx:1114–1121` only offers
`buildIntervalOptions` steps (`check-form.tsx:212–231`), so a check whose
stored period is, say, 7 seconds renders as whatever the Select does with
an unknown value — the user never sees the real configured period and can
silently snap it to a different value by saving.

## Proposal

### Backend — extend, don't fork

One validator, two callers: `ValidateCheck` must remain the single source
of truth, and the create/update paths must run the same checks (validate-
only mode), so the dry-run can never drift from enforcement.

1. **Severity per finding**: each field entry gains
   `severity: "error" | "warning" | "info"` (absent ⇒ `"error"` for
   backward compatibility) and a machine `code`. `valid` is false only when
   at least one *error* exists — warnings never block.
2. **All findings, not the first**: collect every field's findings in one
   response instead of the legacy first-error short-circuit.
3. **Slug uniqueness**: validate `slug` against the org's live checks
   (unique index `checks_slug_idx` on `(organization_uid, slug)` where not
   deleted). The request gains an optional `excludeCheckUid` so edit-mode
   validation doesn't flag the check's own slug. Severity `error`, but
   advisory by nature (TOCTOU) — creation keeps its 409.
4. **Org-rate projection**: recompute the org's demand (spec 03's formula)
   with the *proposed* period/regions substituted for this check (or added,
   for a new check). If the result exceeds the resolved
   `MaxChecksPerMinute`: a **warning** (never an error, never blocking) on
   the `period` field, code like `ORG_RATE_OVER_LIMIT`, message stating the
   projected demand vs cap, plus a machine-usable pointer the frontend maps
   to the scheduling page (spec 2026-08-26-04). Passive check types are
   exempt (they consume no rate budget).

### Frontend

5. The check form surfaces warnings inline, visually distinct from errors
   (amber vs red), non-blocking on submit; the over-limit warning renders
   the link to the scheduling page.
6. **Custom period first in the dropdown**: when the check's current period
   is not one of the known steps, prepend it as the first option, labeled
   with its real value plus a "(custom)" marker (localized). Selecting
   another option is an explicit change; loading and re-saving a check must
   never silently rewrite its period. Unit tests in
   `check-form.test.ts` (7s active check; a custom value above/below the
   type's min/max constraints still displays).
7. All four locales.

### Tests

- Service: multi-finding responses; severity semantics (`valid` true with
  warnings only); slug collision incl. `excludeCheckUid` and
  soft-deleted-slug reuse; rate projection for create, edit-shrink,
  edit-grow, multi-region, passive exemption.
- A guard test asserting create/update and validate share the validator
  (e.g. a finding produced by validate is also enforced/produced on the
  real path).

## Non-goals

- No debounce/UX rework of when the form calls validate — keep the current
  live-validation cadence.
- No new endpoint; the OpenAPI spec (`server/internal/app/openapi/`) is
  updated in place for the extended request/response.
