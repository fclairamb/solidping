# Mobile UX improvements across dash0

## Context

The dash0 operator app was built desktop-first. On mobile (≤ ~640 px) several
pages overflow horizontally, waste vertical space on labels meant for wide
viewports, or render controls (Export/Import, slug edit pill, etc.) that have
no place on a phone. This spec collects the smallest set of targeted fixes to
make the app pleasant to use on a handset, without rebuilding any layout.

The work is concentrated in five surfaces:

1. Status page detail (`/orgs/$org/status-pages/$uid`)
2. Checks list (`/orgs/$org/checks`)
3. Check detail (`/orgs/$org/checks/$uid`)
4. Global chrome — `OrgLayout` header + `AppSidebar` footer
5. Badges page (`/orgs/$org/badges`)

All changes are pure CSS/JSX rearrangement plus one role-gated filter. No
backend changes, no new endpoints, no new dependencies.

Tailwind breakpoint to use: `sm:` (640 px) is the desktop threshold for these
fixes — below `sm:`, we are on "mobile". For the lone exception (sidebar
collapse) we follow the existing `md:` boundary the layout already uses.

## Honest opinion

1. **Do this — it's all small, additive Tailwind tweaks.** No refactor warranted, no new component library. The rule of thumb is "hide on mobile or condense to icon"; nothing more clever.
2. **Don't introduce a `useIsMobile` hook for this.** Tailwind responsive classes (`hidden sm:inline`, `sm:block`, etc.) keep the change reviewable as a diff. A JS-driven breakpoint hook would force conditional rendering and re-renders for zero benefit.
3. **Keep the slug pill discoverable somewhere on mobile, even if hidden in the header.** Hiding it entirely means power-users on mobile can never edit the slug. Acceptable trade-off for v1 — the edit page still works — but call it out so we don't forget.
4. **The "internal/user checks" filter going super-admin-only is a permission fix, not just a mobile fix.** Ship it regardless of mobile context: regular users have no business seeing internal SolidPing checks at all.
5. **Don't redesign the response-time period switcher** beyond shrinking it on mobile. The user's "possibly a dropdown" is a suggestion; a horizontal segmented control collapsed to icon-style buttons fits the same row as the title with less ceremony.

## Scope

**In scope**

- Status page detail header: collapse "View" / "Edit" buttons to icon-only on mobile; move "public" / "enabled" badges below the title and shrink them on mobile.
- Checks list header: collapse "New group" / "New check" to icon-only on mobile; **hide** "Export" / "Import" entirely on mobile.
- Checks list filter: gate the `internalFilter` `<Select>` behind `user.isSuperAdmin`; non-admin users get the implicit `false` filter (user-only checks).
- Check detail: hide the slug pill + edit affordance on mobile; reformat response-time period switcher; reduce availability percentage precision; relabel availability rows to compact 1d/7d/30d/1y; hide the date portion of "Recent results" times when they fall on today.
- Reduce content padding on mobile (`p-4` → `p-3` or `p-2 sm:p-4`) inside `OrgLayout > SidebarInset`.
- Move `LanguageSwitcher` and `ThemeToggle` out of the top-right header into the bottom of the sidebar (above the user menu).
- Add a visible "search" trigger button in the top-right header that opens the existing `<CommandMenu />` (currently triggered only via ⌘K).
- Badges preview: shrink the dashed-border padding on mobile so the badge isn't dwarfed.

**Out of scope**

- Bottom-tab mobile navigation pattern (would require a layout rewrite).
- Replacing the desktop sidebar with a Sheet/Drawer on mobile beyond the existing `SidebarTrigger` behaviour.
- Touch-optimised drag handles for status-page section reordering.
- A dedicated mobile-only dashboard layout.
- Backend API changes (e.g. server-side filtering by `internal=false` for non-admins — handled client-side here, will be done server-side in a follow-up).
- Updating dash (legacy) — only dash0 is in scope (`web/CLAUDE.md` says use dash0 for new work).

## Per-page changes

### 1. Status page detail (`status-pages.$statusPageUid.index.tsx`, ~L472–523)

Current structure (top header row): `[← back] [Title + slug] | [Public badge] [Enabled badge] [View button] [Edit button]`. On a 360 px viewport the buttons overflow right.

Target structure on mobile:

```
[←]  Title
     /slug
     [public] [enabled]              ← smaller, below title
                       [👁] [✏]      ← icon-only View / Edit
```

Changes (around lines 472–523):

- Wrap the right-side cluster (`<div className="flex gap-2">`) so it sits **on a new row** below the title block on mobile: switch the outer container from `flex items-center justify-between` to `flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between`.
- Move the two `<Badge>`s (`public`/`restricted`, `enabled`/`disabled`) **out of the right cluster** and place them inside the title `<div>` immediately under the `/slug` paragraph. Use `text-xs` and `py-0` on mobile, `sm:text-sm` for desktop.
- "View" button (line 508): on mobile show only the `ExternalLink` icon. Implement by wrapping the label span with `<span className="hidden sm:inline">{t("statusPages:detail.view")}</span>` and removing the `mr-2` margin from the icon at mobile size: `className="sm:mr-2 h-4 w-4"`.
- Same treatment for the "Edit" button (line 517) — icon-only on mobile.

### 2. Checks list (`checks.index.tsx`, ~L739–794)

Header row currently: `[Title + subtitle] | [Export] [Import] [New group] [New check]`.

Target on mobile:

- **Hide** Export and Import buttons entirely with `className="hidden sm:inline-flex"`. They are not usable on a phone (no file picker for `.json` config exports in any meaningful way). Apply to lines 747–754 (export button + import button + the hidden file input itself can stay since it's already `hidden`).
- "New group" and "New check": collapse to icon-only on mobile via the same `<span className="hidden sm:inline">` pattern used in §1.
- The header outer flex stays as-is (already wraps with `gap-2`).

Filter row (~L785–794) — gate by role:

```tsx
{user?.isSuperAdmin && (
  <Select value={internalFilter} onValueChange={setInternalFilter}>…</Select>
)}
```

- Import `useAuth` from `@/contexts/AuthContext` at top of file.
- For non-super-admins, also force `internalFilter` to `"false"` initial state and never let it diverge — keep the existing `useState("false")` default, just hide the control.
- Verify the API call still passes `internal=false` for these users (this is the existing behaviour — it does).

### 3. Check detail (`checks.$checkUid.index.tsx` + components)

#### 3a. Hide slug pill on mobile (L423–474, 475–486)

The slug edit affordance (the `🔗 slug ✏` pill, lines 423–442 not-editing branch and 443–474 editing branch) and the `uid: …` link (lines 475–486) are noise on a phone. Hide the entire `<div className="flex items-center gap-1 mt-1">` blocks with a `hidden sm:flex` wrapper. Edit page (`/edit`) is still reachable via the Edit icon-button in the top-right.

> Note: this is the trade-off called out in "Honest opinion" #3 — accept it for v1.

#### 3b. Response-time chart (`response-time-chart.tsx`, ~L398–425)

Current: `[Title] | [Switch + "Full range"] [Hour] [Day] [Week] [Month]` — overflows on mobile.

Target on mobile: the four period buttons collapse to a tighter inline control (no dropdown — keep buttons but smaller and one-line). The user's screenshot shows the title, "Full range" toggle, and the buttons all on one row, wrapping clumsily.

Changes:

- Switch the `CardHeader` from `flex flex-row items-center justify-between` to `flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between` so the controls drop to a second row on mobile.
- Inside the controls cluster: change `flex items-center gap-3` to `flex flex-wrap items-center gap-2 sm:gap-3`.
- Each period button: shrink to `size="sm"` already; on mobile add `px-2 text-xs`. Drop the `capitalize` className in favour of explicit one-letter labels via i18n (`H` / `D` / `W` / `M`) only at `sm` and below: render `{range[0].toUpperCase()}` inside `<span className="sm:hidden">` and full `{range}` inside `<span className="hidden sm:inline capitalize">`.
- "Full range" label text: hide `Full range` text on mobile via `<span className="hidden sm:inline">Full range</span>`, keep the `<Switch>` visible.

#### 3c. Availability table (`availability-table.tsx`)

Two changes:

- **Percentage precision** (line 280): `${row.availability.toFixed(4)}%` → use a helper:
  ```tsx
  function formatAvailability(pct: number): string {
    if (pct >= 100) return "100%";
    if (pct >= 99.99) return `${pct.toFixed(2)}%`;
    if (pct >= 99) return `${pct.toFixed(2)}%`;
    return `${pct.toFixed(1)}%`;
  }
  ```
  So 100.0000% → "100%", 99.9876% → "99.99%", 84.7826% → "84.8%".
- **Compact period labels on mobile** (line 277): the `PERIODS[].label` strings are "Today", "Last 7 days", "Last 30 days", "Last 365 days". Add a sibling `shortLabel` field: `"1d"`, `"7d"`, `"30d"`, `"1y"`. Render `shortLabel` on mobile, `label` on desktop:
  ```tsx
  <TableCell className="font-medium">
    <span className="sm:hidden">{row.shortLabel}</span>
    <span className="hidden sm:inline">{row.label}</span>
  </TableCell>
  ```
  Keep both labels in the i18n file (English-only is fine for now since labels are currently hard-coded in English in this file — call it out in the PR; full i18n is a follow-up).

#### 3d. Recent results time formatting (`checks.$checkUid.index.tsx`, ~L828–832)

Replace `new Date(result.periodStart).toLocaleString()` with a helper that omits the date portion when the timestamp is on the current calendar day:

```tsx
function formatResultTime(iso: string): string {
  const d = new Date(iso);
  const now = new Date();
  const sameDay =
    d.getFullYear() === now.getFullYear() &&
    d.getMonth() === now.getMonth() &&
    d.getDate() === now.getDate();
  return sameDay ? d.toLocaleTimeString() : d.toLocaleString();
}
```

Apply this helper at L830. Apply the same helper at L890 (incidents `startedAt`) for consistency.

#### 3e. Reduce content padding on mobile

In `routes/orgs/$org.tsx:337` change `<div className="flex-1 overflow-auto p-4">` to `<div className="flex-1 overflow-auto p-3 sm:p-4">`. This affects every authenticated page — verify no existing layout depends on `p-4` exactly.

### 4. Global chrome (`routes/orgs/$org.tsx` + `components/layout/AppSidebar.tsx`)

Header (`$org.tsx:328–336`): currently `[Trigger] | [Breadcrumbs] | [Lang] [Theme]`.

Target: `[Trigger] | [Breadcrumbs] | [🔍 search]`.

Changes in `$org.tsx`:

- Remove `<LanguageSwitcher />` and `<ThemeToggle />` from the right cluster of the header.
- Add a small icon-only "search" button there that opens the command menu. The cleanest path: export a `CommandMenuTrigger` from `components/CommandMenu.tsx` that calls the same internal `setOpen(true)` the existing ⌘K shortcut calls. Then render `<CommandMenuTrigger />` in the header.
  - In `CommandMenu.tsx`, lift the `open` state to a small Zustand-free pattern: a module-level `useCommandMenu()` hook backed by `useState` + a context, OR (simpler) export a `commandMenuRef` ref-based imperative API. Given the size of the app, **prefer hoisting `open` into `OrgLayout` and passing it down to `<CommandMenu open={...} onOpenChange={...} />`**, then mounting the trigger button alongside.
- Trigger button visual: `<Button variant="outline" size="sm" className="h-9 gap-2 px-2 sm:px-3">` with a `Search` Lucide icon and an optional `<span className="hidden md:inline">⌘K</span>` hint at desktop.

Changes in `AppSidebar.tsx`:

- Inside `<SidebarFooter>` (line 220), **above** the existing user-menu `SidebarMenuItem`, add a new `SidebarMenuItem` that contains the language + theme controls in a compact horizontal row (e.g. a `<div className="flex items-center justify-around px-2 py-1">` with `<LanguageSwitcher />` and `<ThemeToggle />` side-by-side). Both components are already exported (`LanguageSwitcher` from `@/components/shared/language-switcher`, `ThemeToggle` from this same file).
- This shows when sidebar is expanded; it collapses cleanly when sidebar is iconified because both components render as buttons.

### 5. Badges page (`badges.tsx`, ~L207)

Preview wrapper currently: `<div className="flex items-center justify-center rounded-lg border border-dashed bg-muted/30 p-8">`.

Change `p-8` → `p-3 sm:p-8` so on mobile the badge has more relative size compared to its frame.

## Files to modify

- `web/dash0/src/routes/orgs/$org.tsx` — header rework, content padding.
- `web/dash0/src/components/layout/AppSidebar.tsx` — add language + theme to footer.
- `web/dash0/src/components/CommandMenu.tsx` — accept `open`/`onOpenChange` props from parent; expose trigger button (or accept controlled state).
- `web/dash0/src/routes/orgs/$org/status-pages.$statusPageUid.index.tsx` — header layout, badges relocation, icon-only buttons.
- `web/dash0/src/routes/orgs/$org/checks.index.tsx` — hide Export/Import on mobile, icon-only New buttons, gate `internalFilter` by `useAuth().user?.isSuperAdmin`.
- `web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx` — hide slug pill block on mobile, swap `toLocaleString` for `formatResultTime`.
- `web/dash0/src/components/checks/response-time-chart.tsx` — `CardHeader` flex direction + period button compaction.
- `web/dash0/src/components/checks/availability-table.tsx` — `formatAvailability` helper, `shortLabel` per period.
- `web/dash0/src/routes/orgs/$org/badges.tsx` — preview padding `p-3 sm:p-8`.

## Files NOT to touch

- `web/dash` (legacy app — see `web/CLAUDE.md`).
- Any backend code.
- Any i18n JSON beyond the new short labels for availability rows (and only if introducing them via i18n; English-hardcoded is acceptable for v1 since the surrounding labels in `availability-table.tsx` are also English-hardcoded — flag in PR description).

## Verification plan

Run the app via `make dev-test` (this rebuilds both backend and frontend on file change — see `web/CLAUDE.md`). Then with Chrome devtools:

- **Mobile-narrow (375 px / iPhone SE preset)** — visit each page and confirm:
  - Status page detail: View/Edit are icon-only; "public"/"enabled" badges sit under the title and are visibly smaller. No horizontal scroll.
  - Checks list: Export and Import are not visible at all; New group / New check are icon-only; the "User checks / Internal only / All" filter is **not visible** when logged in as the default org admin (non-super-admin); switch to super-admin and verify it reappears.
  - Check detail: slug pill row is gone; response-time card title and period buttons fit on two short rows; period buttons read `H D W M`; availability table shows `1d 7d 30d 1y` with availability `100%` (no decimals) for healthy rows and `84.8%` for the 365-day row that's currently `84.7826%`; recent-results column shows time-only for today's runs (e.g. `9:02:56 PM`) and full date for older runs.
  - Top-right header has a single search-icon button; clicking it opens the command palette identically to ⌘K. Language and theme toggles are gone from the header but visible at the bottom of the sidebar above the user menu.
  - Badges preview frame is only ~12 px of padding around the badge instead of dwarfing it.
- **Desktop (≥ 1280 px)** — confirm no regression: Export/Import buttons reappear; period buttons read full words; availability shows full long labels; etc.
- **Keyboard** — ⌘K still opens the command palette (not just the new icon button).
- **Permissions** — log in as `admin@solidping.com` (org admin, not super admin per `CLAUDE.md` defaults). The internal-checks filter must be hidden. Then switch to a super-admin user (`isSuperAdmin === true`); the filter must reappear. The API `internal` query parameter must still default to `false` for the non-super-admin user.
- **Lint + typecheck** — `cd web/dash0 && bun run lint && bun run build:no-check`.

## Implementation plan

Ship as one PR — the changes are small, mechanical, and span enough files that splitting would create more review noise than it saves. Commit at each numbered step so a bisect is precise.

1. **Global chrome** — relocate `LanguageSwitcher`/`ThemeToggle` into `SidebarFooter`; add controlled `open` state for `CommandMenu` lifted into `OrgLayout`; replace top-right cluster with the search trigger button.
2. **Content padding** — `p-4` → `p-3 sm:p-4` in `OrgLayout`.
3. **Status page detail** — header restructure, badge relocation, icon-only View/Edit.
4. **Checks list** — hide Export/Import on mobile, icon-only New buttons.
5. **Checks list permissions** — gate `internalFilter` Select by `user?.isSuperAdmin`.
6. **Check detail header** — hide slug pill on mobile.
7. **Response-time chart** — flex-direction shift + period-button compaction.
8. **Availability table** — `formatAvailability` helper + `shortLabel` per row.
9. **Recent results time** — `formatResultTime` helper applied to results + incidents.
10. **Badges preview padding** — `p-8` → `p-3 sm:p-8`.
11. **QA** — run the verification plan; commit any minor follow-ups; `bun run lint && bun run build:no-check`.

## Critical files (read these first)

- `web/dash0/src/routes/orgs/$org.tsx` — top-level layout where header + content padding live.
- `web/dash0/src/components/layout/AppSidebar.tsx` — sidebar footer to receive Language/Theme.
- `web/dash0/src/components/CommandMenu.tsx` — needs to accept controlled state.
- `web/dash0/src/routes/orgs/$org/status-pages.$statusPageUid.index.tsx:472-523` — status page header.
- `web/dash0/src/routes/orgs/$org/checks.index.tsx:739-794` — checks list header + filter.
- `web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:404-552, 800-867` — check detail header + recent results.
- `web/dash0/src/components/checks/response-time-chart.tsx:398-425` — chart period switcher.
- `web/dash0/src/components/checks/availability-table.tsx:34-59, 248-300` — availability rows.
- `web/dash0/src/routes/orgs/$org/badges.tsx:191-217` — badge preview frame.
- `web/dash0/src/contexts/AuthContext.tsx` — `isSuperAdmin` source for the filter gate.
