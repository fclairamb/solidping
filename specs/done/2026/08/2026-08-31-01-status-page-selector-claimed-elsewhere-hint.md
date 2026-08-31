---
model: sonnet
effort: high
---

# A selector section whose matches are claimed elsewhere renders empty with no explanation

## Problem

Status-page section selectors dedupe page-wide (spec 2026-08-29-11, "Overlapping
selectors"): sections are reconciled in position order, a check is claimed by the
first selector section that matches it, and manual placement wins over every
selector (`reconcileSection`, `server/internal/handlers/statuspages/selector.go:307`).

The consequence bites in the dash0 editor. A later section with
`{"labels":{"company":"claude"}}` that matches 2 checks renders with **zero**
components when an earlier `{"all":true}` section already claimed both. The
editor gives no hint why — the section just sits empty under its "Required
labels" rule, which looks exactly like a label-matching bug. A real user hit
this and reported it as one: the label existed, was attached to 2 enabled
checks, and the selector's own SQL returned both — yet the section showed
nothing, because "All services" (earlier by position) had swept every check on
the page.

The backend already enriches admin section payloads with match counts for the
truncation notice — `enrichSelectorCounts`
(`server/internal/handlers/statuspages/service.go:1658`) fills
`selectorMatchTotal` / `selectorTruncated` via `countSelectorMatches`
(`server/internal/handlers/statuspages/selector.go:473`), and the editor renders
"Showing {{shown}} of {{total}} matching checks"
(`web/dash0/src/routes/orgs/$org/status-pages.$statusPageUid.index.tsx:1139`,
locale key `sections.membership.truncated`). But nothing reports how many of the
matches were **claimed elsewhere**, so an empty (or partially-filled) selector
section cannot explain itself.

## Proposal

Make a selector section whose matches are all or partly claimed by earlier
sections or manual rows say so, e.g. **"2 matching checks are already shown in
'All services'."**

### Backend — expose the claimed-elsewhere count on the admin section payload

- Extend `enrichSelectorCounts` (or a sibling) to compute, per selector
  section, how many of its matched checks are displayed by *other* sections of
  the same page. Mechanically: resolve the selector's matched check UIDs (the
  same `ListChecks` + `selector.Filter()` call `desiredChecks` uses,
  `selector.go:414`), intersect with the page's resource rows section-by-section
  (`loadPageState` already reads them all), and count matches sitting outside
  the section being enriched. Manual rows and earlier selector sections both
  count as "claimed elsewhere" — the distinction doesn't matter to the reader.
- Add admin-only fields next to `selectorMatchTotal` on
  `StatusPageSectionResponse` (`service.go:749`), e.g.
  `selectorClaimedElsewhere int` and, to name the claimant,
  `selectorClaimedSections []{ uid, name }` (or just the first/most-populous
  section name — keep the payload minimal, the UI copy needs at most one name
  plus a count). Follow the existing pattern: `omitempty`, admin responses
  only, never on the public payload (selectors leak label taxonomy).
- Best-effort like the existing enrichment: a failed count logs and leaves the
  fields zero rather than failing the page load.
- Do **not** change reconciliation semantics — the claim/dedup rule is
  deliberate and load-bearing; this spec is purely explanatory UI.

### Frontend — explain the section in the editor

- Consult the design reference first
  (`web/dash0/src/routes/orgs/$org/design-reference.tsx`) per CLAUDE.md.
- In the status-page editor
  (`web/dash0/src/routes/orgs/$org/status-pages.$statusPageUid.index.tsx`,
  around the existing `selectorTruncated` notice at line 1139) and/or the
  section membership UI
  (`web/dash0/src/components/shared/section-membership.tsx`), render an
  informational hint when `selectorClaimedElsewhere > 0`:
  - Fully claimed (section empty): the message must carry the full
    explanation — "All {{count}} matching checks are already shown in
    '{{section}}'. A check appears only once per page; move this section
    higher or narrow the other section's rule to show them here."
  - Partially claimed: a lighter variant — "{{count}} more matching checks are
    already shown elsewhere on this page."
- Add the new API fields to the section type in
  `web/dash0/src/api/hooks.ts:2608`.
- Add locale keys to **all four** locales (`de`, `en`, `es`, `fr`) in
  `web/dash0/src/locales/*/statusPages.json`, next to the existing
  `sections.membership.truncated` key. Use i18next pluralization
  (`_one`/`_other`) where the count is inflected.

### Tests

- Backend: a service test pinning the enrichment — page with an `{"all":true}`
  section at position 1 and a labels section at position 2 whose 2 matches are
  claimed → the labels section reports `selectorClaimedElsewhere == 2` and
  names the claiming section; a partial-overlap case; and a
  manual-row-claims case.
- Frontend: unit test (or Playwright, whichever the surrounding code already
  uses for this editor) asserting the hint renders with the right count and
  section name for the fully-claimed case, and is absent when
  `selectorClaimedElsewhere` is 0/absent. Note dash0 unit tests
  (`bun run test:unit`) also verify every locale key exists in all locales.

### Non-goals

- No change to which section wins a claim, to `maxManagedResourcesPerSection`
  truncation, or to the public status-page payload.

## Implementation Plan

### Backend

1. `server/internal/handlers/statuspages/service.go` — add two admin-only
   fields to `StatusPageSectionResponse`, next to `SelectorMatchTotal` /
   `SelectorTruncated`:
   - `SelectorClaimedElsewhere int json:"selectorClaimedElsewhere,omitempty"`
   - `SelectorClaimedSectionName string json:"selectorClaimedSectionName,omitempty"`
   Keeping to a single name field (rather than the `[]{uid,name}` variant the
   spec also allows) matches "keep the payload minimal — at most one claimant
   name plus a count."
2. `server/internal/handlers/statuspages/service.go` — change
   `enrichSelectorCounts` to accept the page's full `[]sectionState` (from
   `loadPageState`, already declared in `selector.go`) alongside the section
   being enriched. It already calls `ListChecks` indirectly via
   `countSelectorMatches`; switch to calling `s.db.ListChecks` directly so we
   keep both the total count AND the actual matched check list (needed to
   intersect against other sections' rows). `Filter()` sets `Limit: 0` (no
   limit) so this is the same unbounded query `countSelectorMatches` already
   ran — no new semantics, just reusing the result.
3. Add a new pure helper `claimedElsewhereBySection(states []sectionState,
   sectionUID string, matched []*models.Check) (count int, claimantName
   string)` in `selector.go` (co-located with `sectionState`/`loadPageState`):
   - Build a `checkUID -> section index` map from every resource row in
     `states`, first section by position order wins a given check (mirrors
     reconciliation's own precedence — a check normally has only one owning
     row anyway).
   - For each matched check, if its owning section is a DIFFERENT section
     than `sectionUID`, tally it under that owning section's index.
   - Return the total tally and the name of the section with the highest
     per-section tally (ties broken by earliest position/index), or `(0, "")`
     when nothing is claimed elsewhere.
4. Update all three call sites of `enrichSelectorCounts` to load
   `[]sectionState` once per request and pass it through:
   - `ListSections` — replace the existing `s.db.ListStatusPageSections`
     call with `s.loadPageState(ctx, page.UID)` (removes a redundant query,
     reuses the same states for every section in the loop).
   - `GetSection` — call `s.loadPageState(ctx, page.UID)` once, pass to
     `enrichSelectorCounts`.
   - `UpdateSection` — same as `GetSection`, after re-fetching the updated
     section.
   A `loadPageState` failure is logged and enrichment is skipped (fields stay
   zero), matching the existing best-effort contract — it must never fail the
   page/section load.
5. No changes to `reconcileSection`, `desiredChecks`, `materialize`, or any
   claim/dedup semantics.

### Frontend

6. `web/dash0/src/api/hooks.ts` — add `selectorClaimedElsewhere?: number` and
   `selectorClaimedSectionName?: string` to the section type at line ~2608.
7. `web/dash0/src/routes/orgs/$org/status-pages.$statusPageUid.index.tsx` —
   next to the existing `selectorTruncated` notice (~line 1139), render a
   second informational notice (reusing the same Alert/notice primitive) when
   `selectorClaimedElsewhere > 0`:
   - Fully claimed (`section.resources` empty after selector materialization,
     i.e. the section shows zero components): full explanation copy with
     `{{count}}` and `{{section}}`.
   - Partially claimed (section has some resources but
     `selectorClaimedElsewhere > 0`): lighter "N more matching checks are
     already shown elsewhere" copy, no section name needed (spec explicitly
     drops the name in the partial case).
8. Add `sections.membership.claimedElsewhere.full_one` /
   `_other` and `sections.membership.claimedElsewhere.partial_one` / `_other`
   keys to `web/dash0/src/locales/{de,en,es,fr}/statusPages.json`, next to
   `sections.membership.truncated`.

### Tests

9. Backend, in `server/internal/handlers/statuspages/selector_test.go`
   (mirroring `TestSelector_MatchTotalIsAdminOnly` /
   `TestSelector_OverlappingSectionsDoNotDuplicate`):
   - `TestSelector_ClaimedElsewhere_FullyClaimed` — `{"all":true}` section at
     position 1, labels section at position 2 whose 2 matches are both
     claimed by position 1 → position 2's response has
     `SelectorClaimedElsewhere == 2` and `SelectorClaimedSectionName` equal
     to the `{"all":true}` section's name; also assert the public payload
     never carries the field (`r.Zero`/`r.Empty`, mirroring
     `TestSelector_MatchTotalIsAdminOnly`).
   - `TestSelector_ClaimedElsewhere_PartialOverlap` — labels section matches
     3 checks, 1 claimed by an earlier section → `SelectorClaimedElsewhere
     == 1`, the section itself still shows the other 2.
   - `TestSelector_ClaimedElsewhere_ManualRowClaims` — a manual resource row
     (not a selector section) claims one of the labels section's matches →
     same count/name semantics, naming the section that holds the manual
     row.
10. Frontend unit test (Vitest, colocated with the existing tests for this
    route/component) asserting the hint renders with the right count/section
    name in the fully-claimed case and is absent when
    `selectorClaimedElsewhere` is 0/absent. `bun run test:unit` also verifies
    locale-key parity across all four locales, covering item 8.
