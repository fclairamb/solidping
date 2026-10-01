---
model: sonnet
effort: medium
---

# The check detail header has no hierarchy: nine equal-weight controls, a loud Delete, and no useful facts

## Problem

The header of the check detail page (`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:1111-1417`) looks amateur:

1. **No hierarchy.** Nine controls with the same weight: back arrow, Edit, Disable, Clone, Badges, Publish on a status page, Refresh, Delete, Docs (`:1246-1379`). There is no primary action. The loudest element on the page is the filled red Delete (`:1363`), the action used least.
2. **Toolbar on its own row.** It sits right-aligned under the title (`flex-col` at `:1111`), leaving an empty band next to the title and separating actions from their subject.
3. **Back arrow inside the toolbar** (`:1247-1256`) instead of top-left. The docs icon (`:1379`) floats alone after Delete.
4. **Type shown twice by us:** `CheckTypeIcon` + `CheckTypeBadge` side by side (`:1126-1129`), often a third time in the user's own check name ("solidping.io (http)").
5. **Status is a 12px dot** (`StatusDot`, `:1113-1120`) vertically centered on the title+slug block, so it looks misaligned. "Up/Down" is the most important fact and the smallest element.
6. **Slug chip uses a 🔗 emoji** (`:1177`, `:1191`) while everything else is Lucide.
7. **Refresh is redundant** (`:1348-1362`): the page already refetches on an interval (`:855`) and has a live channel (`checkLiveError`, `:1141`).
8. **No facts in the header.** Target, interval, regions and last check only appear further down in cards.

## Proposal

Target layout (desktop, ≥ `md`):

```
← Checks  ›  solidping.io (http)
solidping.io (http)  (● Up) (🌐 HTTP) (SLO chip)                [⏻ Disable] [✎ Edit] [⋯]
https://solidping.io ↗ · ⏱ Every 1 min · 📍 3 regions · 🕒 Checked 12s ago · # http-solidping-io ✎
                                                                        ┌──────────────────────────┐
                                                                        │ ⧉  Clone                 │
                                                                        │ 🌐 Publish on a status page│
                                                                        │ ✓  Badges                │
                                                                        │ 🔗 Copy check link       │
                                                                        │ 📖 HTTP check docs       │
                                                                        │ ──────────────────────── │
                                                                        │ 🗑  Delete check  (red)  │
                                                                        └──────────────────────────┘
```

Mobile (< `md`):

```
← Checks
solidping.io (http)                 [✎] [⋯]
(● Up) (🌐 HTTP)
https://solidping.io ↗ · 🕒 12s ago · 📍 3 regions
```

(Rendered mockups from the UI review exist locally under `.bb/chats/thr_adjvkvew5v/artifacts/header-a*.png`, not tracked.)

Steps:

1. **Breadcrumb.** Replace the back `Button` (`:1247-1256`) with a breadcrumb line above the title: `ArrowLeft` + "Checks" link to `/orgs/$org/checks`, `ChevronRight`, then the check name (muted, truncated). No breadcrumb primitive exists in `web/dash0/src/components/`; add a small `Breadcrumb` to `web/dash0/src/components/ui/` and render it in `web/dash0/src/routes/orgs/$org/design-reference.tsx`. Keep the accessible name "Back to checks" on the link (existing key `checks:detail.backToChecks`, `en/checks.json:729`) so screen readers and e2e keep a stable handle.

2. **Title row.** Change the header root (`:1111`) from `flex-col` to a single row: title block `flex-1 min-w-0` on the left, actions on the right (`items-start`), wrapping below `md`.
   - Remove the `StatusDot` (`:1113-1120`). Add a status pill right after the `<h1>` using the existing `StatusBadge` (`@/components/shared/status-badge`, imported at `:88`) fed by `headerStatus` (`:1092`). A disabled check shows the existing "Disabled" badge instead (`checks:detail.disabled`).
   - Keep one type indicator: `CheckTypeBadge` with the type icon inside it, drop the separate `CheckTypeIcon` (`:1127`). If `CheckTypeBadge` cannot take an icon, render `CheckTypeIcon` inside the badge rather than beside it.
   - Keep `SloCoverageChip`, the pending-first-run badge and the live-error badge (`:1130-1160`) in the same row, unchanged.

3. **Meta line** under the title (`text-sm text-muted-foreground`, items separated by a small dot, `flex-wrap`), each item with a 14px Lucide icon:
   - **Target**: the check's target (URL for http, host for tcp/icmp/dns, etc.) as an external link with `ExternalLink` icon when it is a URL, plain text otherwise. Omit for types with no target (heartbeat/push). See To verify.
   - **Interval**: `Timer` + "Every {{period}}" from `check.period` (parsed by `parsePeriodMs`, `:378`; reuse the existing duration formatter).
   - **Regions**: `MapPin` + "{{count}} regions" (singular form for 1). Omit when the check has no region list.
   - **Last check**: `Clock` + "Checked {{timeAgo}}" using `components/ui/time-ago.tsx`. Hidden while `isPendingFirstRun` (`:842`), the pending badge already covers it.
   - **Slug**: the existing slug link and inline editor (`:1162-1224`) move into this line. Replace the 🔗 emoji (`:1177`, `:1191`) with the Lucide `Hash` icon, mono font. The uid-alias link (`:1225-1243`) stays, also in this line.
   - Below `md`, show only target, last check and regions (hide interval and slug), matching the mobile mockup. Today the slug is already `hidden sm:flex`.

4. **Actions.** Replace the inline toolbar (`:1258-1379`) with three controls:
   - `Disable` / `Enable` toggle: `variant="outline"`, `Power` icon kept, label visible at `md+`, icon-only below. Same `handleToggleEnabled` (`:1038`), same aria-label.
   - `Edit`: `variant="default"` (the primary gradient button), `Pencil` icon, label at `md+`, icon-only below. Same link as today (`:1267-1273`).
   - `⋯` overflow: `Button variant="outline" size="icon"` with `Ellipsis` icon and aria-label "More actions" (new key), opening a `DropdownMenu` (`components/ui/dropdown-menu.tsx`, already in the design reference) aligned `end`, containing in this order:
     1. Clone (`Copy`), `handleClone`, disabled while `cloneCheck.isPending`.
     2. Publish on a status page (`Globe`), opens the existing `PublishOnStatusPageDialog` (`setPublishOpen(true)`). Keep `data-testid="publish-status-page-link"` on the menu item.
     3. Badges (`BadgeCheck`), `asChild` `Link` to `/orgs/$org/checks/$checkUid/badges`.
     4. Copy check link (`Link2`), copies the canonical check URL (slug form when present) to the clipboard, success toast. New key.
     5. Docs (`BookOpen`), label "{{type}} check docs", opens `docsHrefForType(check.type)` (`:100`, `:1379`). Remove the standalone `DocsLink`.
     6. Separator, then Delete check (`Trash2`) with `text-destructive focus:text-destructive` (rule in `web/dash0/AGENTS.md`, "Delete is always red"), opening the existing controlled `AlertDialog` (`setDeleteOpen(true)`).
   - The controlled `AlertDialog` (`:1382-1406`) and `PublishOnStatusPageDialog` (`:1410-1415`) stay triggerless, mounted outside the menu so closing the menu doesn't unmount them.

5. **Drop Refresh** (`:1348-1362`). Keep `refetch` for the error retry (`:1074`). Remove the `checks:detail.refresh` key from all four locales only if nothing else uses it.

6. **Translations.** New keys (`moreActions`, `copyLink`, `linkCopied`, `typeDocs`, `every`, `regionCount` with plural forms, `checkedAgo`, `breadcrumb` if needed) in `web/dash0/src/locales/{en,fr,de,es}/checks.json`. `bun run test:unit` enforces parity.

7. **Design reference.** The "Page header" section of `web/dash0/src/routes/orgs/$org/design-reference.tsx` (starts `:904`) documents the layout this spec replaces, in two contradictory subsections:
   - "Detail & edit pages: collapse the action cluster into an overflow menu on mobile" (`:1080`), from spec 2026-06-15-05, since reverted.
   - "Detail & edit pages: stack the action toolbar on its own row (action-dense headers)" (`:1176`), from spec 2026-06-17-01. It cites the check detail page as its example and says "instead of … hiding them behind an overflow menu".

   Replace both with one subsection, "Detail pages with many actions: one primary, one secondary, the rest in ⋯". Show a live example: breadcrumb, title + status pill, meta line, then Edit (primary) + toggle (outline) + `⋯` menu with a red Delete last. State the rule: at most two visible actions beside the title, the primary one uses `variant="default"`, everything else goes in the overflow menu at every width, and Delete is always the last menu item after a separator. Also update the Buttons section description (`:1703`), which points to "a back button + action cluster".

   Adopting the pattern on other detail pages (incidents, status pages, integrations) is out of scope.

## Tests

- `web/dash0/e2e/check-detail.spec.ts:955-1000` (the toolbar test): rewrite it. It currently asserts the opposite design: all actions inline and no "More actions" trigger at 1280px and 390px. New assertions:
  - Desktop 1280px: `Edit` and `Disable` show their labels; "More actions" is visible; Clone, Badges, Delete, Refresh are **not** visible before opening the menu; there is no "Refresh" anywhere.
  - Opening "More actions" shows Clone, Publish on a status page, Badges (href matches `/orgs/test/checks/[^/]+/badges$`), Copy check link, docs item, Delete check; the Delete item has the destructive color class.
  - Mobile 390px: Edit and "More actions" are visible icon-only (labels hidden); the breadcrumb "Back to checks" link is visible and navigates to the checks list.
- `check-detail.spec.ts:1015` (delete flow), `:1052-1053`, `:1058`, `:1113-1126` (enable/disable): update the Clone/Badges/Delete steps to open the menu first; the Disable/Enable toggle stays inline, so those selectors should pass unchanged. Assert the delete confirmation still opens and cancelling it keeps the check.
- `e2e/status-page-from-check.spec.ts:68`, `e2e/status-page-auto-include.spec.ts:152,227`: open "More actions" before clicking `publish-status-page-link`.
- `e2e/docs-links.spec.ts:85`: open the menu before clicking Badges; add a case that the docs menu item points at `docsHrefForType` for the check's type.
- `check-detail.spec.ts` new case: meta line shows the target URL as a link, "Every …", and "Checked …" for a check with a result; a freshly created check shows the pending badge and no "Checked …" item (negative case).
- `check-detail.spec.ts` new case: "Copy check link" writes the URL to the clipboard (grant `clipboard-read`) and shows the toast.
- Slug inline edit (existing tests using the pencil next to the slug): still passes from its new position in the meta line; the 🔗 emoji is gone.
- Run the whole list of files that `grep -rln "getByLabel(\"\(Clone\|Delete\|Badges\|Refresh\)\")\|Back to checks" web/dash0/e2e` returns, not just the ones above, and the full suite as the batch gate.
- `bun run test:unit` (locale parity), `bun run lint:e2e`, `bun run typecheck:e2e`.

## To verify

- Whether the refresh-control example in the design reference (`:1989`, "The canonical list/detail header refresh control") cites the check detail page. If it does, point it at a list page instead. List pages keep their Refresh buttons.
- Where the check target comes from per type (`check.config.url`, `config.host`, …) and whether a display helper already exists (the checks list shows a target column). Reuse it instead of writing a new per-type switch.
- Field name for the check's regions on the check object, and whether an empty list means "all regions" (then show "All regions", not "0 regions").
- Field for the last result time on `check.lastResult` (the summary cards use it, `components/checks/check-summary-cards.tsx:74`).
- Whether `CheckTypeBadge` accepts an icon or children.
- Other callers of `checks:detail.refresh` and `checks:detail.backToChecks` (two `backToChecks` keys exist, `en/checks.json:729` and `:927`).

## Decisions

Decided by the user on 2026-09-30.

1. **The overflow menu comes back, at every width.** This deliberately reverses three June specs:
   - 2026-06-15-05 added a `⋯` menu below `md`.
   - 2026-06-15-07 removed it: "buttons should never collapse into a hidden menu", icon-only instead.
   - 2026-06-17-01 moved the toolbar onto its own row and listed "re-collapsing actions into a `MoreVertical` overflow menu" as out of scope.

   That approach kept every action one click away, but it produced the nine-equal-controls header this spec fixes. The new rule: only the two actions used daily (Edit, Disable/Enable) stay visible, everything else goes behind `⋯`. Publish on a status page is the only frequent action in the menu, and the post-create flow deep-links it anyway (`?publish=true`). Implementers must not keep a "no overflow menu" assertion anywhere in e2e.
2. **The toggle stays "Disable" / "Enable".** The mockups said "Pause", but "Disable" is the term used in the checks list, the API (`enabled`) and existing e2e. Keep the `Power` icon.
3. **Refresh is dropped from the check detail header.** The page refetches on an interval (`:855`) and listens to the live channel. The live-error badge (`:1141`) already tells the user when data is stale. Keep `refetch` for the error-state retry (`:1074`).
