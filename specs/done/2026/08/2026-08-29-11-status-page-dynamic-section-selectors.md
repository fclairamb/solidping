---
model: opus
effort: high
---

# Status page membership is static — no way to include all checks, or all checks with a label

## Problem

A status page's contents are a hand-maintained list: each
`StatusPageResource` points at exactly one check or one check group
(`server/internal/db/models/status_page.go`, XOR `check_uid` /
`check_group_uid`, constructor `NewStatusPageResource` at `:457`). Three
consequences:

- **Toil that scales with the org.** Every new check that belongs on a page
  requires someone to remember to open the page and add it. Nobody does this
  reliably.
- **The silent-omission failure mode.** The worst version of the above: a
  new service ships, its check is created, the check goes down — and the
  status page (or the office wallboard from spec `2026-08-29-08`, which
  inherits the page's curation) stays green because the check was never
  attached. A board that lies green is worse than no board.
- **Labels exist but status pages can't use them.** Checks already have a
  full labels feature — key-value `Label` + many-to-many `CheckLabel`
  (`server/internal/db/models/check.go:430-475`) with autocomplete
  suggestions and AND-filtering in `ListChecksFilter` (`check.go:481`) —
  but the only grouping a status page understands is the check group, and a
  `check_group_uid` resource deliberately renders as **one rolled-up
  component**, not as individual rows. There is no way to say "this section
  is: every check labeled `public=true`".

## Proposal

Add **dynamic membership at the section level**: a section may carry a
selector — "all checks" or "all checks matching these labels" — and the
system keeps its resources in sync. Explicitly *not* page-level, so a page
can mix a hand-curated "Core" section with a dynamic "Everything else"
section.

### 1. Selector on the section (model + API)

- New nullable `selector` JSON column on `StatusPageSection`
  (`server/internal/db/models/status_page.go`, constructor
  `NewStatusPageSection` at `:400`):
  - `{"all": true}` — every check in the org.
  - `{"labels": {"env": "prod", "public": "true"}}` — AND semantics over
    key=value pairs, matching the existing `ListChecksFilter` behavior
    (`check.go:481`). Reuse that filter; do not reimplement matching.
- Section create/update endpoints (`server/internal/handlers/statuspages/
  service.go`, section create ~`:1567`) accept and validate the selector
  (unknown fields rejected; `all` and `labels` mutually exclusive; empty
  `labels` map rejected). Update the OpenAPI spec
  (`server/internal/app/openapi/openapi.yaml`).
- A selector section may still hold manually added resources; see dedupe
  below.

### 2. Materialize, don't virtualize (reconciler)

Too much existing machinery assumes real resource rows — availability
enrichment (`enrichWithAvailability`), positions, the badge/summary/embed
endpoints, and the read-time resolution of publications'
`affectedResources`. So the selector **materializes real
`StatusPageResource` rows**, flagged with a new `managed_by_selector`
boolean column:

- **Reconcile function** (idempotent, per section): compute the matching
  check set; insert missing managed rows; remove managed rows whose check no
  longer matches, **via the same soft-delete path as a manual resource
  delete** (resources already carry `deleted_at`) so downstream behavior —
  including past publications' affected-resource display — is exactly what a
  manual removal already produces.
- **Dedupe within the page**: a check that is already an explicit (manual)
  resource anywhere on the same page is skipped by the selector — the manual
  placement wins. Manual rows are never touched by the reconciler.
- **Ordering**: manual resources keep their explicit positions first;
  managed rows follow, alphabetical by check name, positions rewritten by
  the reconciler.
- **Triggers**: call reconcile synchronously from the write paths that can
  change membership — check create/delete and label attach/detach (the
  checks and labels handlers), scoped to the org's selector-bearing
  sections — plus a cheap backstop reconcile on page view (skippable via a
  staleness marker) so drift can never persist. Requirement, however it is
  wired: a newly created matching check must appear on the public page
  without any manual action, and a deleted check's row must disappear.
- **Cache**: membership changes must invalidate/refresh whatever
  `server/internal/statuspagecache/statuspagecache.go` allows to be shared-
  cached for public pages, the same way manual resource edits do today.
- Group-type resources (`check_group_uid`) are untouched by all of this; a
  selector materializes check-type rows only.

### 3. Guardrails — public pages (dash0 UX)

Auto-inclusion on a **public** page is a disclosure footgun: a scratch check
named after an internal hostname lands on the public internet the moment
it's created. Therefore:

- Selectors are **never a default**. The existing manual flow (and the
  default "Services" section from spec `2026-08-28-16`) stays as-is.
- When enabling `all` — or any selector — on a page whose visibility is
  `public`, dash0 shows an explicit warning: *"Every current and **future**
  matching check will appear on this public page, including checks created
  later."* The `all` selector on a public page gets the strongest copy.
  No warning needed on `private`/`password` pages.
- Recommend label-based opt-in in the UI copy and docs (`public=true` as
  the suggested pattern): labeling puts the publish decision on the check,
  where it belongs, and inverts the leak risk.

### 4. dash0 UI

- The section create/edit dialog
  (`web/dash0/src/routes/orgs/$org/status-pages.$statusPageUid.index.tsx`)
  gains a membership mode: **Manual** (today's behavior) / **All checks** /
  **By label** — the label mode reusing the existing label key/value
  autocomplete. Follow the design reference
  (`web/dash0/src/routes/orgs/$org/design-reference.tsx`); if the
  mode-picker pattern is new, add it to the reference page.
- Managed resources render in the section list with an "auto" badge and are
  not individually deletable or reorderable (the selector owns them);
  manual rows in the same section behave as today.
- i18n: new strings in all four dash0 locales
  (`web/dash0/src/locales/{en,fr,de,es}/`), locale-parity unit tests.

### 5. status0

No rendering changes — the public page renders materialized rows exactly as
manual ones. Verify ordering is stable across reconciles (no row-shuffling
between two polls of an unchanged page).

### 6. Performance note

A selector can put every check in a large org onto one page, and
per-resource availability enrichment is linear in resources. Don't build
pagination for this spec, but measure the page payload with ~100+ resources
and, if needed, cap managed rows per section (with a visible "and N more"
truncation and a dash0 warning) rather than shipping a page that times out.

### 7. Tests

- **Backend**:
  - Creating a labeled check makes it appear in a matching selector section
    (and in the public payload); removing the label / deleting the check
    removes it via soft-delete.
  - AND semantics: check matches only when *all* selector pairs match.
  - Dedupe: a check manually placed on the page is not duplicated by a
    selector section; deleting the manual row lets the selector re-adopt it
    on the next reconcile.
  - Ordering: manual first in position order, managed alphabetical;
    idempotence: reconciling twice changes nothing.
  - `all` selector picks up a brand-new check with no labels.
  - Past resolved publications keep their affected-resource display after
    the referenced managed row is soft-deleted (mirror whatever the manual
    delete path guarantees — verify in
    `server/internal/handlers/incidentpublications/`).
- **E2E** (Playwright, `web/dash0/e2e/`): create a section with a label
  selector → create a check with that label → the public page shows it
  without touching the page; remove the label → gone. Public-visibility
  warning appears when enabling a selector.
- **i18n**: locale-parity run for the new dash0 keys.

### 8. Docs

Extend the status pages docs (`web/docs/`) with dynamic sections: selector
kinds, the `public=true` opt-in pattern, the public-page warning rationale,
and the manual-wins dedupe rule. Cross-reference the TV mode docs (spec
`2026-08-29-08`): private page + `all` selector + kiosk token = a
zero-maintenance company wallboard.

### Open questions (decide during implementation, don't grow scope)

- Whether reconcile-on-write should be transactional with the triggering
  write or best-effort immediately after (recommendation: after, with the
  page-view backstop as the safety net — a check create must not fail
  because a status page reconcile did).
- Whether a selector section's `labels` values should support existence-only
  matching (`"public": "*"`); v1 can require exact values.

---

## Implementation Plan

### Premise corrections found while reading the code

Two statements in the Proposal do not match the tree, and the plan follows the
tree:

1. **`StatusPageResource` has no `deleted_at`.** Sections soft-delete;
   resources are **hard**-deleted (`DeleteStatusPageResource`,
   `postgres.go:5443` / `sqlite.go:5389`; the service comment at
   `statuspages/service.go:1977` says so explicitly). The spec's normative
   clause is "*via the same path a manual resource delete uses … so downstream
   behaviour is exactly what a manual removal already produces*". The
   reconciler therefore calls the **same** `DeleteStatusPageResource`, and a
   test pins that a managed removal and a manual removal produce byte-identical
   `affectedResources` on a past publication. Inventing a soft-delete for
   managed rows only would *diverge* from manual removal, which is the opposite
   of what the spec asks for.
2. **`internal/statuspagecache` caches nothing.** It is a stateless helper that
   derives `Cache-Control` / `Vary` headers (`Control`, `Apply`, `ApplyGated`);
   there is no store and no invalidation API. Public pages are served
   `public, max-age=60`. So "invalidate the cache the way manual resource edits
   do today" = *nothing*, because manual edits do nothing either. The plan
   instead aligns the page-view backstop interval with `PageMaxAge` (60 s) so a
   selector change is visible on the same timescale as any manual edit, and
   dash0's React Query keys are invalidated by the existing section-mutation
   hooks.

### Decisions taken (the spec's open questions, closed)

- Reconcile-on-write is **best-effort immediately after** the triggering write,
  never transactional with it. A check create must not fail because a status
  page reconcile did.
- `labels` values require **exact values**; no `"*"` existence matching.
- Matching reuses `models.ListChecksFilter` + `db.ListChecks`, which by default
  **excludes internal checks** (`Internal` nil ⇒ `internal = FALSE`). That is
  kept — an internal check must never be auto-published — and documented.
- Overlapping selectors: sections are reconciled in **position order** and a
  check is claimed by the first selector section that matches it, so a page
  never renders the same check twice. Manual placement always wins over every
  selector.

### Steps

**1. Model + migration.**
- `models.SectionSelector{All bool; Labels map[string]string}` with
  `Validate()` (mutually exclusive `all`/`labels`, non-empty `labels`, key
  regex reuse, value length cap) and `IsAll()`/`Filter()` helpers producing a
  `ListChecksFilter`.
- `StatusPageSection.Selector *SectionSelector` (`bun:"selector,type:jsonb"`),
  `StatusPageSectionUpdate.{SetSelector bool, Selector *SectionSelector}`
  (presence pattern, mirroring `SetAutoPublish`).
- `StatusPageResource.ManagedBySelector bool`
  (`bun:"managed_by_selector,notnull,default:false"`) +
  `NewManagedStatusPageResource(...)`.
- Append a `SECTION: status-page-section-selector` block to the **existing open
  cycle migration** `017_v0_21_0.up.sql` / `.down.sql` in **both** dialects (the
  repo's one-consolidated-migration-per-release rule), adding
  `status_page_sections.selector` and
  `status_page_resources.managed_by_selector`, plus a partial index on
  selector-bearing sections.

**2. DB layer.** Handle the two new columns in
`Create/Update/GetStatusPageSection` and the resource writes for both
postgres and sqlite; add `ListSelectorSectionPageUIDs(ctx, orgUID) ([]string,
error)` to `db.Service` and both implementations (distinct live page UIDs in
the org owning at least one live selector-bearing section).

**3. Reconciler** — new `server/internal/handlers/statuspages/selector.go`:
- `parseSelector(raw json.RawMessage) (*models.SectionSelector, error)` — strict
  decode with `DisallowUnknownFields`, then `Validate()`; all failures map to
  `VALIDATION_ERROR`.
- `reconcilePage(ctx, orgUID, pageUID) error` — idempotent:
  1. load sections (position order) + their resources;
  2. build the page-wide **manual** check set (never touched);
  3. per selector section in position order: `ListChecks` with the selector's
     filter, drop manual-claimed and already-claimed checks, sort by
     `lower(name)` then `uid` for a total order;
  4. delete managed rows no longer desired via `DeleteStatusPageResource`;
  5. insert missing managed rows;
  6. rewrite **only** managed positions, starting at `max(manual position)+1`,
     and only when the stored value differs — so two reconciles of an unchanged
     page issue zero writes (ordering stability).
- `ReconcileOrgSelectors(ctx, orgUID)` — best-effort fan-out over
  `ListSelectorSectionPageUIDs`, errors logged, never returned.
- `maybeReconcileOnView(ctx, orgUID, pageUID)` — the backstop: a `sync.Map`
  staleness marker per page with a 60 s interval (== `statuspagecache.PageMaxAge`),
  so a hot public page pays at most one reconcile per minute.

**4. Triggers.**
- `statuspages`: section create/update that sets a selector, manual resource
  create/delete, and the public/summary view paths (backstop).
- `checks`: a `StatusPageReconciler` interface + `SetStatusPageReconciler`
  injector (the `SetPublicIncidentProvider` precedent), called after
  `CreateCheck`, `UpdateCheck` (labels are replaced there via
  `SetCheckLabels`) and `DeleteCheck`. Wired in `internal/app`.

**5. API + OpenAPI.** `selector` on create/update section requests (raw-body
presence so an explicit `null` clears it) and on the **admin** section response
only — the public payload strips it, because a page's label taxonomy is
internal. `managedBySelector` on the resource response. `openapi.yaml` updated.

**6. dash0.** Membership mode picker (Manual / All checks / By label) in the
add + edit section dialogs, reusing `LabelInput` for the label mode; a
public-visibility warning (strongest copy for `all`); managed rows render an
"auto" badge and lose their delete/reorder controls. New pattern added to
`design-reference.tsx` **and rendered** in `DesignReferencePage` + listed in
`SECTIONS`. Strings in all four locales with a parity test.

**7. Guardrails.** Selector is never a default; the default "Services" section
is untouched; internal checks are excluded; docs and UI copy push `public=true`
label opt-in.

**8. Performance.** Measure the public payload and latency at 100+ managed
resources; cap per section only if the measurement demands it. Reported in the
final notes either way.

**9. Tests.** Backend: label round trip, AND semantics, dedupe + re-adoption
after manual delete, ordering + idempotence (zero writes on the second
reconcile), `all` adopting a brand-new unlabelled check, internal-check
exclusion, selector validation errors, public payload strips the selector, and
the publication `affectedResources` parity between managed and manual removal.
dash0 unit: locale parity. E2E: label selector → create matching check →
appears on the public page untouched → remove label → gone; plus the
public-visibility warning.

**10. Docs.** `web/docs/` status pages: selector kinds, `public=true` opt-in,
the warning rationale, manual-wins, and the TV-mode cross-reference (private
page + `all` + kiosk token = zero-maintenance wallboard).
