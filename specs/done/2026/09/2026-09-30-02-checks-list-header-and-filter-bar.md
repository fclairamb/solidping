---
model: sonnet
effort: medium
---

# The checks list header and filter bar look messy: five equal secondary buttons, five control styles in three sizes

## Problem

On `/orgs/$org/checks` (`web/dash0/src/routes/orgs/$org/checks.index.tsx`):

1. **Header: five outlined buttons with the same weight** next to New Check (`:1525-1579`): Export, Import, Automatic placement, New Group, Check scheduling, plus a floating docs icon (`docsHref`, `:1577`). Export, Import and Automatic placement are org-level operations run a few times a year. Automatic placement (`components/checks/auto-placement-bulk.tsx:26`) is shown even when nothing can be switched, and its dialog then says "none".
2. **Filter bar: five control styles in three sizes** (`:1608-1712`):
   - the search `Input` (`text-sm`);
   - the `SegmentedControl` with smaller text;
   - the super-admin scope `Select` (`text-sm`, `w-[160px]`, `:1636-1647`);
   - two `FacetedFilter`s with `size="sm"` text in an `h-9 w-[160px]` box (`components/shared/faceted-filter.tsx:62-73`), mostly empty ("All statuses");
   - the `LabelFilter` trigger at `size="sm"`, 32px high while everything else is 36px (`components/shared/label-filter.tsx:115-123`).
3. **Inconsistent prefixes.** "Group by:" (`:1618`) and "Labels:" (`:1683`) have a text prefix, Status and Type don't.
4. **Mixed control types.** Refresh (`:1670-1681`, an action) and Group by (`:1617-1634`, a view setting) sit between the filters.
5. **No result count** when a filter is active.

## Proposal

Same rule as spec `2026-09-30-01-check-detail-header-redesign.md`: one primary action, at most one visible secondary, everything else behind `⋯`. Implement that spec first. Both edit the "Page header" section of `design-reference.tsx`, and this one reuses its wording.

Target layout (desktop):

```
[tile] Checks                                          [📁+ New group] [+ New check] [⋯]
       Manage your monitoring checks
[🔍 Search checks…      ] [⊕ Status │ Down  Degraded] [⊕ Type] [⊕ Labels] Reset ×     3 of 42 checks [📁 By group|🖥 By host] [⟳]
```

`⋯` menu: Import checks… · Export checks · ── · Check scheduling · Automatic placement… · ── · Check types docs

Mobile (< `sm`):

```
[tile] Checks                    [📁+] [+] [⋯]
[🔍 Search checks…                        ]
[Status │ 2] [Type] [Labels] →scroll      [📁|🖥] [⟳]
```

(Rendered mockups from the UI review exist locally under `.bb/chats/thr_adjvkvew5v/artifacts/checks-list-la*.png`, not tracked. The `/` shortcut hint drawn in the search box is not part of this spec.)

Steps:

1. **Header actions** (`:1529-1576`). Keep in this order:
   - `New group`: outline button, `FolderPlus`, label at `sm+`, icon-only below. Unchanged `data-testid="new-group-button"`.
   - `New check`: default (primary) button, unchanged link and `data-testid="new-check-button"`.
   - `⋯`: `Button variant="outline" size="icon"`, `Ellipsis` icon, aria-label "More actions" (reuse the key added by spec 2026-09-30-01 if it lives in `common`, otherwise add one). It opens a `DropdownMenu` aligned `end` with:
     1. Import checks… (`Upload`), `openImportDialog`, `data-testid="import-button"`.
     2. Export checks (`Download`), `handleExport`, `data-testid="export-button"`.
     3. Separator.
     4. Check scheduling (`CalendarClock`), `asChild` `Link` to `/orgs/$org/checks/scheduling`, `data-testid="scheduling-link"`.
     5. Automatic placement… (`Shuffle`), opens the automatic-placement confirmation.
     6. Separator.
     7. Check types docs (`BookOpen`), link to `/docs/features/check-types`. Remove `docsHref` from the `PageHeader` (`:1577`).
   - Import and Export are no longer hidden below `sm` (`:1534`, `:1543`): the menu makes them reachable on mobile too.

2. **Split `AutoPlacementBulkButton`** (`components/checks/auto-placement-bulk.tsx`) into a controlled `AutoPlacementBulkDialog({ org, open, onOpenChange })`. Keep the existing preview query, mutation, toasts and "none" message unchanged. The page holds `autoPlacementOpen` state; the menu item sets it. Mount the dialog outside the `DropdownMenu` so closing the menu doesn't unmount it (same pattern as the import dialog). Delete the old button export if nothing else uses it.

3. **Filter bar layout** (`:1608-1712`). One row, `flex flex-wrap items-center gap-2`. Every control is 36px high (`h-9`) with `text-sm`.
   - **Left, filters:** search (unchanged behaviour and testid, `min-w-[200px] max-w-sm flex-1`), Status, Type, Labels, the admin Scope, then Reset.
   - **Right, view** (`ml-auto`): result count, group-by toggle, Refresh.
   - Remove the "Group by:" (`:1618-1620`) and "Labels:" (`:1683`) text prefixes.

4. **`FacetedFilter` trigger** (`components/shared/faceted-filter.tsx:62-74`):
   - Inactive: `variant="outline"`, default size, no fixed width, dashed border, `CirclePlus` icon, the dimension name ("Status", "Type").
   - Active: solid border, the name, a 1px vertical separator, then up to two value badges (accent background, `text-xs font-semibold`), or a single "{{count}} selected" badge above two. Below `sm`, always the count badge.
   - Replace the "All statuses" style label: change `facetedFilterTriggerLabel` (`lib/faceted-filter.ts:55`) or replace it with a `title` prop plus the selected option labels. Its callers are `checks.index.tsx:1070,1090`.
   - Keep `data-testid={testId}` on the trigger and the popover content unchanged.

5. **`LabelFilter` trigger** (`components/shared/label-filter.tsx:115-123`): same look as the facet trigger. Dashed "⊕ Labels" when empty, solid with a count badge when labels are selected. Keep `data-testid="label-filter-trigger"`. The removable label chips (`:87-104`, `data-testid="label-chips"`) stay right after the trigger, restyled to the same accent value-badge look. They can't go inside the trigger (a button inside a button).

6. **Admin scope** (`:1636-1647`, only when `user?.isSuperAdmin`): replace the `Select` with a facet-styled trigger labelled "Scope". It is dashed while on the default ("User checks", value `false`) and solid with a value badge ("Internal only" / "All checks") otherwise. It opens a `DropdownMenuRadioGroup` with the same three options and the same `setInternalFilter`.

7. **Reset.** A ghost `Reset ×` button, shown only when status, type, labels or scope is non-default. It clears all four in one `navigate(..., { replace: true })`, leaving search and `groupBy` untouched. It replaces the labels-only "Clear filters" button (`:1693-1707`, `data-testid="clear-label-filters"`). New testid: `reset-filters`.

8. **Result count** (right group, `text-sm text-muted-foreground`, hidden below `sm`): "{{total}} checks" when nothing is filtered, "{{shown}} of {{total}} checks" when any filter or search is active. Hide it while `checksStreaming` rather than showing a number that is still climbing. See To verify for where `total` comes from.

9. **Group-by toggle** (`:1621-1633`): options become "By group" (`Folder` icon) and "By host" (`Server` icon), icon-only below `sm` with the label kept as aria-label/tooltip. Keep `aria-label={t("groupBy.label")}` on the control, the testids `group-by-groups` / `group-by-host`, and the host tooltip. The control must render at the same 36px height as the buttons (see To verify).

10. **Refresh** (`:1670-1681`): icon-only `Button variant="outline" size="icon"` with a tooltip and the existing aria-label `common:refresh`, last in the right group. Same `handleRefresh` (`:1518`) and spin while `isRefetching`.

11. **Mobile** (< `sm`):
    - Search takes the first row at full width.
    - The second row holds the facet triggers in a non-wrapping `overflow-x-auto` strip, then the icon-only toggle and Refresh.
    - The page must never scroll horizontally.

12. **Translations.** New keys in `web/dash0/src/locales/{en,fr,de,es}/checks.json`:
    - `importChecks`, `exportChecks`, `checkTypesDocs`;
    - `filters.status`, `filters.type`, `filters.labels`, `filters.scope`, `filters.selected` (plural);
    - `resetFilters`;
    - `resultCount` and `resultCountFiltered` (plural);
    - `groupBy.byGroup`, `groupBy.byHost`.

    Remove keys that become unused (`labelFilterLabel`, the "All statuses"/"All types" labels) from all four files. `bun run test:unit` enforces parity.

13. **Design reference** (`web/dash0/src/routes/orgs/$org/design-reference.tsx`):
    - `FacetedFilterSection` (`:6362`) and `LabelFilterSection` (`:6274`) render the new inactive and active triggers.
    - The "Button placement" section (`:1266-1345`, snippet at `:1266`) documents the list-page rule. PageHeader actions hold the primary "New X", at most one secondary create action, and `⋯` for org-level tools (import, export, bulk operations, docs). The filter toolbar puts filters on the left and view controls on the right (result count, grouping, icon-only Refresh).
    - Adopting this on other list pages is out of scope.

## Tests

Unit:
- `web/dash0/src/lib/faceted-filter.test.ts`: the new trigger output. No selection gives the dimension name only. One or two selections give the value labels. Three or more give "3 selected". Unknown values are ignored (negative case).

E2E (update every file that touches the moved controls):
- `e2e/checks-import-sources.spec.ts`: open "More actions" before `import-button`.
- `e2e/check-scheduling.spec.ts`: open "More actions" before `scheduling-link`.
- `e2e/check-placement.spec.ts`: automatic placement goes through the menu item. The confirmation still opens, confirms, and shows the "none" message when nothing can be switched.
- `e2e/checks-index-status-type-filters.spec.ts`: the trigger reads "Status" / "Type" with no selection and shows the value badges after selecting. The URL params are unchanged.
- `e2e/check-label-filter.spec.ts`: the trigger shows a count badge once a label is picked. The chip remove button still works. `reset-filters` clears labels; the old `clear-label-filters` testid is gone.
- `e2e/checks-index-host-view.spec.ts`: the toggle still switches via `group-by-host` / `group-by-groups`, and its accessible name is still "Group by".
- New cases in `e2e/checks-index-status-type-filters.spec.ts`:
  - With no filter, the count reads "{{total}} checks" and there is no Reset button (negative case).
  - Filtering by status shows "N of M checks" and Reset. Clicking Reset clears status, type and labels, keeps the search text and `groupBy`, and removes itself.
- New mobile case (390px):
  - New check, New group and More actions are visible icon-only.
  - `document.documentElement.scrollWidth <= window.innerWidth`.
  - Import is reachable through the menu (it was hidden on mobile before).
- Run the rest unchanged, since they reference the moved testids: `checks-index-filter-hides-empty-groups`, `check-groups`, `check-group-discoverability`, `checks-search-url-param`, `degraded-detection`, `check-fail-quorum`, `entitlements-usage`, `control-surface-elevation`. Find any others with `grep -rln "export-button\|import-button\|scheduling-link\|new-group-button\|status-filter\|type-filter\|clear-label-filters\|label-filter-trigger\|group-by-" web/dash0/e2e`.
- `bun run test:unit`, `bun run lint:e2e`, `bun run typecheck:e2e`, then the full dash0 suite as the batch gate.

## To verify

- **Source of the total for the result count.** In groups mode each group carries an unfiltered `checkCount` (`:824`, `:920-927`); summing them plus the ungrouped bucket may give the org total. Check what host mode has. If there is no reliable total without a new API call, show only "{{shown}} checks" when filtering and hide the count otherwise. Don't add a backend endpoint for this.
- **`SegmentedControl` height** (`components/ui/segmented-control.tsx`). If it doesn't support a 36px size, add a `size` prop rather than overriding it with classes at the call site, and show it in the design reference.
- **Scope select test hooks.** Whether the super-admin scope `Select` has a testid or e2e coverage today. If it does, keep that handle on the new trigger.
- **Other `AutoPlacementBulkButton` callers.** Whether anything besides `checks.index.tsx:1550` uses it before deleting the export.

## Decisions

Decided by the user on 2026-09-30 (proposal A of the UI review):

1. **Proposal A**: frequent actions visible, org tools in `⋯`, one filter bar at one height. Proposal B (labelled "Manage ▾" menu, toolbar attached to the list card) and proposal C (single Filter button with active-filter chips) are not pursued.
2. **New group stays visible**, icon-only below `sm`, rather than moving into `⋯` on mobile as the mockup drew it. Three icon buttons fit at 390px, and `check-group-discoverability` exists to keep it findable.
3. **Refresh stays, icon-only.** The list already polls every 10s (`CHECKS_LIST_POLL_MS`, `contexts/LiveEventsContext.tsx:60`), but the design reference's button-placement rule keeps a Refresh on every list toolbar, and removing it here alone would break that consistency. The check detail page drops it (spec 2026-09-30-01) because detail pages have no such rule.
4. **Automatic placement keeps its current behaviour**: always listed, and the dialog explains when there is nothing to switch. Hiding it when the preview count is 0 is not in scope.
