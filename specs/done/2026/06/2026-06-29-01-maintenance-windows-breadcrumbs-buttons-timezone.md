# Maintenance Windows: breadcrumbs, collapsing "+ New" button, and UTC/local timezone clarity

## Context

The **Maintenance Windows** area
(`/dash0/orgs/$org/maintenance-windows`) has three UI/UX gaps that make it
inconsistent with the rest of dash0 and ambiguous about time:

1. **No breadcrumb on any of its routes.** The shared, route-driven
   `Breadcrumbs` component in
   [`web/dash0/src/routes/orgs/$org.tsx`](../../web/dash0/src/routes/orgs/$org.tsx)
   has a per-section branch for every other area (checks, incidents, status
   pages, jobs, discovery, organization, …) but **none** for
   `/orgs/$org/maintenance-windows`. With no matching branch the function falls
   through to its final `return null`, so all four routes render an empty
   breadcrumb bar:
   - `…/maintenance-windows` — list
     ([`maintenance-windows.index.tsx`](../../web/dash0/src/routes/orgs/$org/maintenance-windows.index.tsx))
   - `…/maintenance-windows/new` — create
     ([`maintenance-windows.new.tsx`](../../web/dash0/src/routes/orgs/$org/maintenance-windows.new.tsx))
   - `…/maintenance-windows/$uid` — detail
     ([`maintenance-windows.$maintenanceWindowUid.index.tsx`](../../web/dash0/src/routes/orgs/$org/maintenance-windows.$maintenanceWindowUid.index.tsx))
   - `…/maintenance-windows/$uid/edit` — edit
     ([`maintenance-windows.$maintenanceWindowUid.edit.tsx`](../../web/dash0/src/routes/orgs/$org/maintenance-windows.$maintenanceWindowUid.edit.tsx))

2. **The "+ New Window" button does not collapse on narrow screens.** The
   project convention (documented in the design reference) is that header action
   buttons pair an icon with a one-word label, and **below the `sm` breakpoint
   the label collapses so only the icon remains** (`<Plus />` only), with an
   `aria-label` kept for screen readers. The Maintenance Windows **Refresh**
   button right next to it already follows this pattern, but the **+ New
   Window** button does not — it always shows its full label, pushing the
   header to wrap awkwardly on mobile.
   [`maintenance-windows.index.tsx:191-196`](../../web/dash0/src/routes/orgs/$org/maintenance-windows.index.tsx):

   ```tsx
   <Link to="/orgs/$org/maintenance-windows/new" params={{ org }}>
     <Button data-testid="mw-new-button">
       <Plus className="mr-2 h-4 w-4" />
       {t("maintenanceWindows:new")}
     </Button>
   </Link>
   ```

3. **Times never show their timezone, and "set" vs "render" zones are
   inconsistent.** Today the create/edit form interprets every date/time input
   in the **browser's local timezone** (`isoToLocalInput`, `localInputToIso`,
   `isoToLocalTime`, `isoToLocalDate`, `composeLocalDateTime`, `snapAnchor` — all
   use local `getHours()/getDay()/getDate()` etc. in
   [`maintenance-window-form.tsx`](../../web/dash0/src/components/shared/maintenance-window-form.tsx)),
   while the **backend expands recurring occurrences entirely in UTC** — the
   weekly weekday, monthly day-of-month, and daily time-of-day are all derived
   from the UTC components of `startAt`
   ([`server/internal/db/models/maintenance_window.go`](../../server/internal/db/models/maintenance_window.go),
   `NextOccurrences`/`addWeeks`/`addMonths`/`addDays`). There is **no per-window
   or per-org timezone column** — `start_at`/`end_at`/`recurrence_end` are UTC
   `timestamptz` instants.

   This mismatch is a **latent bug**: a user not in UTC who picks "weekly on
   Monday 22:00" can have the backend recur on **Tuesday** (because 22:00 local
   crosses midnight in UTC), and the rendered weekday can disagree with what was
   entered. The display side is itself inconsistent — `describeSchedule` already
   reads the monthly day with `getUTCDate()` but renders the weekly weekday and
   all clock times in local time
   ([`maintenance-window-schedule.ts:152,162`](../../web/dash0/src/lib/maintenance-window-schedule.ts)).
   On top of that, **no rendered time states its zone**, so an operator can't
   tell whether "22:00" means their time or the server's.

   The desired rule (from the request): **enter/store in UTC when setting, show
   the viewer's local time when rendering a concrete instant — and always make
   the zone visible.**

This spec is purely the dash0 frontend: the breadcrumb bar, the list header
button, and the form/display timezone handling. No API or DB changes — the
backend is already UTC-correct and is the source of truth this work aligns to.

## Goals

### 1. Breadcrumbs for every Maintenance Windows route

Add a `maintenance-windows` branch to `Breadcrumbs` in `$org.tsx`, mirroring the
existing `isChecks` branch (list → new → detail → edit), using the **`Wrench`**
icon and the existing **`nav:maintenanceWindows`** label ("Maintenance", already
present in `en/de/es/fr`, same icon+label the sidebar uses —
[`AppSidebar.tsx:104-106`](../../web/dash0/src/components/layout/AppSidebar.tsx)):

| Route | Breadcrumb |
|---|---|
| list (`/maintenance-windows`) | `Maintenance` (active, non-link) |
| new (`/maintenance-windows/new`) | `Maintenance › New` |
| detail (`/maintenance-windows/$uid`) | `Maintenance › <window title>` |
| edit (`/maintenance-windows/$uid/edit`) | `Maintenance › <window title> › Edit` |

- On detail/edit, **`Maintenance`** is a `<Link>` back to the list, and the
  window-title crumb on the edit page is a `<Link>` back to the detail page
  (exactly like `Checks › <name> › Edit`).
- The leaf title comes from `useMaintenanceWindow(org, uid)`; fall back to the
  first 8 chars of the uid until it loads (no blank crumb, no crash).
- Reuse the existing `nav` keys **`new`** ("New") and **`edit`** ("Edit") already
  used by the checks/account branches — no new nav strings.

This is only the shared breadcrumb bar (rendered once by the org layout). The
in-page back-arrow buttons on the detail/edit pages are separate and stay as-is.

### 2. "+ New Window" collapses to icon-only on mobile

Make the list-header **+ New Window** button follow the canonical responsive
pattern (icon always visible, label hidden below `sm`, `aria-label` retained),
matching the Refresh button beside it and the design reference. Target JSX:

```tsx
<Link to="/orgs/$org/maintenance-windows/new" params={{ org }}>
  <Button data-testid="mw-new-button" aria-label={t("maintenanceWindows:new")}>
    <Plus className="sm:mr-2 h-4 w-4" />
    <span className="hidden sm:inline">{t("maintenanceWindows:new")}</span>
  </Button>
</Link>
```

(`mr-2` → `sm:mr-2` so the gap only appears with the label; wrap the label in
`hidden sm:inline`; keep `data-testid="mw-new-button"`; add `aria-label`.)

### 3. Set in UTC, render in local — and always show the zone

**3a. Setting (create/edit form) → UTC.** Interpret and compose **every** date/
time input in `maintenance-window-form.tsx` as UTC, so the weekday / day-of-month
/ time-of-day the user chooses match the backend's UTC recurrence expansion
exactly (fixing the latent drift bug), and add a **visible "all times are in
UTC" affordance** near the schedule fields.

**3b. Rendering → local for instants, UTC for the recurrence pattern, zone
always shown.**
- **Concrete instants** (one-time start/end, each "Next occurrences" line, the
  one-time summary) render in the **browser's local timezone with the zone
  abbreviation shown** (e.g. `23:00 GMT+1`).
- **The recurrence pattern wall-clock** (weekly weekday, monthly day-of-month,
  the repeating `HH:mm` start/end, and the "until" boundary date) renders in
  **UTC with an explicit "UTC" label**, because those fields are UTC-defined and
  must read back identically to what the form shows. See *Design decisions*.

## Out of scope

- Any backend / API / DB change. The server is already UTC-correct; this work
  aligns the client to it. (No new `timezone` column, no per-org zone setting.)
- Changing the page bodies beyond the timezone formatting and the one header
  button — no new fields/cards, no layout redesign.
- The sidebar entry, the `PageHeader`, the admin layout wrappers
  (`maintenance-windows.tsx`, `maintenance-windows.$maintenanceWindowUid.tsx`),
  and the in-page back-arrow buttons.
- A user-selectable display timezone / "show in UTC" toggle — out of scope here;
  the rule is fixed (UTC in, local out, zone labelled).

## Design decisions

- **Why set in UTC instead of teaching the backend the browser zone?** The
  backend stores only UTC instants and expands recurrence from their UTC
  components, with no zone column. Composing the form in UTC is the minimal
  change that makes "Monday 22:00" mean the same Monday 22:00 everywhere, with no
  schema change. The alternative (persist a zone and expand in it) is a much
  larger, cross-stack change and is explicitly not wanted here.
- **Why render the recurrence *pattern* in UTC but concrete *occurrences* in
  local?** The pattern's weekday/day-of-month/time are UTC-defined. If we
  converted the pattern to the viewer's local zone, the displayed weekday or
  day-of-month could shift away from what the form shows (e.g. set "Monday",
  view "Sunday"), breaking the set↔view round-trip. So the **pattern** stays in
  UTC (clearly labelled "UTC"), while the **concrete next-occurrence
  timestamps** — which are absolute moments — are shown in the viewer's local
  zone (with its abbreviation) so they know when it actually fires for them. A
  recurring window therefore reads e.g.
  *"Every Monday, 22:00 – 23:00 UTC (1h) · no end date"* with
  *"Next: Jan 7, 23:00 GMT+1 · …"* beneath it. One-time windows are a single
  instant and render fully in local time.

## Implementation

### Part 1 — Breadcrumbs (`web/dash0/src/routes/orgs/$org.tsx`)

All in the `Breadcrumbs` component. Mirror the `isJobs` precedent from
[`specs/done/2026/06/2026-06-16-02-jobs-section-breadcrumbs.md`](../done/2026/06/2026-06-16-02-jobs-section-breadcrumbs.md).

1. **Import** `Wrench` from `lucide-react` and `useMaintenanceWindow` from
   `@/api/hooks` (alongside the other section hooks already imported there).
2. **Section flags** near the other `is…` flags:
   ```ts
   const isMaintenance = matches.some((m) =>
     m.routeId.startsWith("/orgs/$org/maintenance-windows"),
   );
   const isMwNew = routeIds.has("/orgs/$org/maintenance-windows/new");
   const isMwDetail = routeIds.has(
     "/orgs/$org/maintenance-windows/$maintenanceWindowUid/",
   );
   const isMwEdit = routeIds.has(
     "/orgs/$org/maintenance-windows/$maintenanceWindowUid/edit",
   );
   ```
   `params.maintenanceWindowUid` is already exposed by the merged match params.
3. **Leaf fetch**, gated so it's disabled off-section (the hook is
   `enabled: !!org && !!uid`, so `""` short-circuits it):
   ```ts
   const { data: mw } = useMaintenanceWindow(
     org,
     isMwDetail || isMwEdit ? (params.maintenanceWindowUid ?? "") : "",
   );
   ```
4. **Render branch** (place near the other section branches, e.g. just after the
   `isJobs` block), using the shared `linkClass`/`activeClass`/`iconClass`/
   `BreadcrumbSeparator` helpers and the `nav`-namespace `t`:
   ```tsx
   if (isMaintenance) {
     const isDetailOrEdit = isMwDetail || isMwEdit;
     const title =
       mw?.title || params.maintenanceWindowUid?.slice(0, 8);
     return (
       <>
         {isMwNew || isDetailOrEdit ? (
           <Link
             to="/orgs/$org/maintenance-windows"
             params={{ org }}
             className={linkClass}
           >
             <Wrench className={iconClass} />
             {t("maintenanceWindows")}
           </Link>
         ) : (
           <span className={activeClass}>
             <Wrench className={iconClass} />
             {t("maintenanceWindows")}
           </span>
         )}
         {isMwNew && (
           <>
             <BreadcrumbSeparator />
             <span className={activeClass}>{t("new")}</span>
           </>
         )}
         {isDetailOrEdit && (
           <>
             <BreadcrumbSeparator />
             {isMwEdit ? (
               <Link
                 to="/orgs/$org/maintenance-windows/$maintenanceWindowUid"
                 params={{ org, maintenanceWindowUid: params.maintenanceWindowUid! }}
                 className={linkClass}
               >
                 {title}
               </Link>
             ) : (
               <span className={activeClass}>{title}</span>
             )}
           </>
         )}
         {isMwEdit && (
           <>
             <BreadcrumbSeparator />
             <span className={activeClass}>{t("edit")}</span>
           </>
         )}
       </>
     );
   }
   ```

### Part 2 — Collapsing "+ New Window" button (`maintenance-windows.index.tsx`)

Replace the button at
[`maintenance-windows.index.tsx:191-196`](../../web/dash0/src/routes/orgs/$org/maintenance-windows.index.tsx)
with the responsive form shown in **Goal 2** (`sm:mr-2`, `hidden sm:inline`
label, `aria-label`, same `data-testid`). No other header change — the Refresh
button already collapses correctly.

### Part 3 — Timezone

**3a. Form sets in UTC**
([`maintenance-window-form.tsx`](../../web/dash0/src/components/shared/maintenance-window-form.tsx)).
Convert each conversion helper from local to UTC; this is mechanical but must be
complete or occurrences will silently drift:

| Helper (current, local) | Change to UTC |
|---|---|
| `isoToLocalInput` (`getFullYear/Month/Date/Hours/Minutes`) | use `getUTCFullYear/UTCMonth/UTCDate/UTCHours/UTCMinutes` |
| `localInputToIso` (`new Date(local)`) | parse as UTC: append a `Z` (and `:00` seconds), e.g. `new Date(\`${local}:00Z\`)` |
| `isoToLocalTime` | `getUTCHours/UTCMinutes` |
| `isoToLocalDate` | `getUTCFullYear/UTCMonth/UTCDate` |
| `composeLocalDateTime(date,time)` | `new Date(\`${date}T${time}:00Z\`)` |
| `snapAnchor` (`getDay/getDate/getHours/getMinutes`, `setDate`, `new Date(y,m,d,…)`) | `getUTCDay/getUTCDate/getUTCHours/getUTCMinutes`, `setUTCDate`, and `new Date(Date.UTC(y,m,d,h,min))` |
| "until" end-of-day (`new Date(\`${until}T23:59:59\`)`) | `new Date(\`${until}T23:59:59Z\`)` |
| initial `weekday` (`getDay()` / `new Date().getDay()`) | `getUTCDay()` |
| initial `dayOfMonth` (`getDate()`) | `getUTCDate()` |
| initial `firstDay` (`isoToLocalDate`) | UTC variant above |

Rename the helpers (`isoToLocalInput` → `isoToUtcInput`, etc.) and update their
doc-comments so they no longer claim "browser's local zone". The Monday-first
`weekdayLabel` chips are pure locale labels (names for weekday N) and are
unaffected — the chip *values* now mean UTC weekdays, which is the point.

Add a **visible UTC affordance**: a muted caption under the Schedule card (e.g.
beneath the recurrence selector), using a new i18n key
`maintenanceWindows:form.utcNote` = **"All times are entered and stored in
UTC."** Add the key to `en/de/es/fr`
(`web/dash0/src/locales/*/maintenanceWindows.json`).

**3b. Rendering shows the zone**
([`maintenance-window-schedule.ts`](../../web/dash0/src/lib/maintenance-window-schedule.ts)
and
[`maintenance-windows.$maintenanceWindowUid.index.tsx`](../../web/dash0/src/routes/orgs/$org/maintenance-windows.$maintenanceWindowUid.index.tsx)):

- **Local + zone abbreviation for instants.** Add `timeZoneName: "short"` to the
  `Intl` options of the instant formatters so the zone is printed:
  - detail-page `formatDateTime` (`…index.tsx:55`) — used for one-time
    start/end (note: change `recurrenceEnd` display to the UTC pattern rule
    below, since it's a recurrence boundary, not an instant).
  - `fmtTime` / `fmtDateTime` in `maintenance-window-schedule.ts` — these drive
    `formatOccurrenceDate` (next-occurrence lines) and the **one-time**
    `summary.once`, which stay local.
- **UTC + "UTC" label for the recurrence pattern**, in `describeSchedule`:
  - weekly weekday: add `timeZone: "UTC"` to the
    `start.toLocaleDateString(..., { weekday: "long" })` call (line 152) so it
    matches the UTC weekday the form set.
  - the repeating `start`/`end` clock times in the `daily`/`weekly`/`monthly`
    branches: format with `timeZone: "UTC"` **and** `timeZoneName: "short"` so
    they read `22:00 UTC`.
  - monthly day: already `getUTCDate()` — keep.
  - the `until` tail date (`fmtDate(new Date(w.recurrenceEnd))`): render with
    `timeZone: "UTC"` so it matches the UTC date the form set (no zone label
    needed on a bare date).

  Concretely, give the file two small clock formatters — one local
  (`timeZoneName: "short"`) for instants, one UTC
  (`timeZone: "UTC", timeZoneName: "short"`) for the pattern — and route each
  call site to the right one. The summary i18n templates
  (`summary.once/daily/weekly/monthly`) are unchanged: the zone text is baked
  into the interpolated `{{start}}`/`{{end}}` values.

No change needed to `MaintenanceScheduleSummary` or the list-row schedule cell
themselves — they call `describeSchedule` / `formatOccurrenceDate`, which now
carry the zones.

## Verification

Run against a backend (`make dev-test`) signed in as an admin, **and** with the
browser in a non-UTC zone so timezone regressions are observable (e.g. Playwright
`use: { timezoneId: "America/New_York" }`, or your OS set to a non-UTC zone).

1. **Breadcrumbs**
   - `…/maintenance-windows` → `Maintenance` (Wrench, active, non-link).
   - `…/maintenance-windows/new` → `Maintenance › New`; `Maintenance` links back.
   - `…/maintenance-windows/$uid` → `Maintenance › <title>`; leaf shows the
     window title (8-char uid until it loads), `Maintenance` links back.
   - `…/maintenance-windows/$uid/edit` → `Maintenance › <title> › Edit`;
     `Maintenance` and `<title>` both link back to their pages.
2. **Responsive button** — narrow the viewport below `sm`: the New button shows
   only `+` (no label), with the Refresh button; both keep working. Above `sm`
   the full "New Window" label returns. Screen reader announces it via
   `aria-label`.
3. **Timezone — round-trip correctness (the core check).** In a non-UTC browser:
   - Create a **weekly** window "Monday, 22:00, 1h". Save, reopen the detail:
     the pattern reads **"Every Monday, 22:00 – 23:00 UTC"**, and the form on
     edit still shows Monday + 22:00. Confirm the backend-computed **Next
     occurrences** land on the correct Monday and render in **local time with a
     zone suffix** (e.g. `Jan 7, 23:00 GMT+1`).
   - Create a **one-time** window: detail shows start/end in **local time with a
     zone suffix**; the edit form's `datetime-local` shows the same UTC wall
     clock you entered.
   - Create a **monthly** "day 15" window near a month boundary and confirm the
     rendered day and the next occurrences agree with the UTC day-of-month.
   - The "All times are in UTC" caption is visible in the create/edit form.
4. **E2E** — update and re-run
   [`web/dash0/e2e/maintenance-windows.spec.ts`](../../web/dash0/e2e/maintenance-windows.spec.ts):
   it round-trips `mw-start-time-input` = `"22:00"` (line ~283) and uses
   weekday-sensitive `mw-first-day-input` dates (e.g. `2030-01-07 // a Monday`,
   lines ~208, ~269). These pass trivially when the test browser is UTC but only
   meaningfully guard the fix when the Playwright project pins a **non-UTC**
   `timezoneId` — add that and assert the rendered pattern shows `UTC` and the
   occurrence lines show the local zone. The `mw-new-button` testid is unchanged,
   so the existing click/visibility assertions keep working.
5. `bun run lint` and `bun run build` (tsc gate) pass in `web/dash0`;
   `make lint test` green.

## Files referenced

- `web/dash0/src/routes/orgs/$org.tsx` — `Breadcrumbs` (Part 1).
- `web/dash0/src/routes/orgs/$org/maintenance-windows.index.tsx` — New button
  (Part 2).
- `web/dash0/src/components/shared/maintenance-window-form.tsx` — UTC conversion
  helpers + UTC caption (Part 3a).
- `web/dash0/src/lib/maintenance-window-schedule.ts` — instant-vs-pattern
  formatters (Part 3b).
- `web/dash0/src/routes/orgs/$org/maintenance-windows.$maintenanceWindowUid.index.tsx`
  — `formatDateTime` zone label + `recurrenceEnd` rendering (Part 3b).
- `web/dash0/src/locales/{en,de,es,fr}/maintenanceWindows.json` — new
  `form.utcNote` key.
- `web/dash0/src/locales/{en,de,es,fr}/nav.json` — existing
  `maintenanceWindows`/`new`/`edit` keys (read-only, no change).
- `web/dash0/src/components/layout/AppSidebar.tsx` — reference for the `Wrench`
  icon + `nav:maintenanceWindows` label (no change).
- `server/internal/db/models/maintenance_window.go` — confirms the backend
  expands recurrence in UTC (read-only; the contract this work aligns to).
- `web/dash0/e2e/maintenance-windows.spec.ts` — E2E to update (Part-4 verify).
- `web/dash0/src/routes/orgs/$org/design-reference.tsx` — canonical
  breadcrumb + responsive-button patterns (reference).

## Implementation Plan

Three independent commits, each verifiable on its own:

1. **Breadcrumbs** — single file (`$org.tsx`): import `Wrench` +
   `useMaintenanceWindow`, add the `isMaintenance`/`isMwNew`/`isMwDetail`/
   `isMwEdit` flags, the gated leaf fetch, and the render branch. Verify the four
   routes per Verification §1.
2. **Collapsing button** — single file (`maintenance-windows.index.tsx`): apply
   the responsive pattern to `mw-new-button`. Verify §2.
3. **Timezone** — `maintenance-window-form.tsx` (UTC helpers + caption),
   `maintenance-window-schedule.ts` (instant vs UTC-pattern formatters), the
   detail page (zone label + `recurrenceEnd`), the `maintenanceWindows.json`
   locales (`form.utcNote`), and the E2E (`timezoneId` + assertions). Verify
   §3–§4, paying attention to the non-UTC round-trip.

QA gate for each: `bun run lint` + `bun run build` in `web/dash0`, plus
`make lint test` and the dash0 Playwright suite.
