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
