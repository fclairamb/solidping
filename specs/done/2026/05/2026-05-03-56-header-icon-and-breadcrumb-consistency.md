# Unify h1-with-icon and breadcrumb coverage across top-level pages

## Context

dash0 has six top-level navigable sections in the sidebar (see
`web/dash0/src/components/layout/AppSidebar.tsx` `navItems` at L44–75):

| titleKey      | path                          | icon            |
|---------------|-------------------------------|-----------------|
| dashboard     | /orgs/$org                    | LayoutDashboard |
| checks        | /orgs/$org/checks             | ListChecks      |
| incidents     | /orgs/$org/incidents          | AlertTriangle   |
| events        | /orgs/$org/events             | Calendar        |
| statusPages   | /orgs/$org/status-pages       | Globe           |
| badges        | /orgs/$org/badges             | BadgeCheck      |

Plus a test-mode-only section:

| testTools     | /orgs/$org/test               | Bug             |

Two consistency gaps follow from looking at
`web/dash0/src/routes/orgs/$org.tsx` `Breadcrumbs` (L73–264) and the
six route files:

1. **Breadcrumb coverage.** `Breadcrumbs` has branches for checks,
   incidents, events, status-pages, account, organization, server.
   It is **missing** branches for `dashboard`, `badges`, and `test`.
   Spec 53 covers `dashboard`. This spec covers `badges` and `test`.
2. **Icon-on-h1.** Every section's *breadcrumb* renders the sidebar
   icon next to the section name. The corresponding *page header*
   (h1 / page title) does not — there's no visual continuity between
   the sidebar click, the breadcrumb, and the page title. Spec 53
   adds icon-on-h1 for the dashboard. This spec applies the same
   treatment to the other five list pages.

The goal of this spec is one mechanical sweep so the chrome stays
consistent — when a user clicks "Checks" in the sidebar, they see
a `<ListChecks>` icon in the breadcrumb AND in the page h1.

## Honest opinion

1. **Don't extract a shared `<PageH1>` component yet.** Six call sites,
   each just `<icon /> + <h1>`. JSX inlining is fine. A shared helper
   becomes worth it once we add a third axis (subtitle, badge, action
   slot) — that's the bigger `<PageHeader>` refactor mentioned in the
   plan, scoped separately.
2. **Source the icon from the same module the sidebar uses.**
   `lucide-react`. Don't try to `import { ListChecks } from "../layout/AppSidebar"`
   — that creates an artificial coupling. Direct lucide imports stay
   trivially greppable.
3. **Match the icon's color to `text-muted-foreground`.** Otherwise
   the icon competes with the title text. Same treatment as spec 53.
4. **Don't change the title TEXT.** Some pages currently render a
   bespoke header (e.g. with a count badge). Only insert the icon —
   leave existing structure alone unless it actively conflicts.
5. **Some list pages may not have an explicit `<h1>` today** — the
   audit (see "Per-page changes" below) found `<h1>` only in events
   and badges from a quick grep. The implementer should locate the
   topmost text that visually serves as the page title (it may be
   `<h2>` or unstyled `<div>`) and apply the icon there. If there is
   no title at all, **add a proper `<h1>`** as part of this spec.
6. **Test mode breadcrumb branch is small**, but `testTools` is
   gated behind `runMode === "test"` in the sidebar — the breadcrumb
   should be unconditionally present (it doesn't render outside of
   test mode anyway since the route doesn't exist), so no role-gating
   needed in the breadcrumb logic.

## Scope

**In scope**

- `Breadcrumbs` in `$org.tsx`: add branches for `badges` and `test`
  (mirroring the existing pattern).
- Page h1s on the five list pages (checks, incidents, events,
  status-pages, badges): add the matching sidebar icon to the page
  title.

**Out of scope**

- Dashboard breadcrumb / h1 — spec 53.
- Status-page detail header — spec 52.
- Detail-page h1s (check detail, incident detail, status-page
  detail) — those already have specific headers; revisit later if
  the user wants.
- Any `<PageHeader>` component refactor.
- Translation changes — all keys reused from `nav.json`.

## Per-page changes

### 1. `Breadcrumbs` — `$org.tsx` (L73–264)

Add two new branches. Place them after the existing `isStatusPages`
branch (L236–262). Imports needed at the top (BadgeCheck and Bug
already exist in `AppSidebar.tsx`; copy the pattern):

```tsx
import {
  /* … existing icons … */
  BadgeCheck,
  Bug,
} from "lucide-react";
```

Detection:

```tsx
const isBadges = routeIds.has("/orgs/$org/badges");
const isTest = matches.some((m) => m.routeId.startsWith("/orgs/$org/test"));
```

Branches:

```tsx
if (isBadges) {
  return (
    <span className={activeClass}>
      <BadgeCheck className={iconClass} />
      {t("badges")}
    </span>
  );
}

if (isTest) {
  return (
    <span className={activeClass}>
      <Bug className={iconClass} />
      {t("testTools")}
    </span>
  );
}
```

Verify the two translation keys exist in `web/dash0/src/locales/{en,fr}/nav.json`
— `badges` and `testTools` both present in `AppSidebar.tsx`'s
`navItems`/`testNavItems`, so they should already be in the JSON.

### 2. Checks list page h1 — `routes/orgs/$org/checks.tsx`

Locate the topmost title element in `web/dash0/src/routes/orgs/$org/checks.tsx`
(grep showed no `<h1>`, so it likely uses `<h2>` or a styled `<div>`).
Convert to:

```tsx
<h1 className="text-3xl font-bold tracking-tight flex items-center gap-2">
  <ListChecks className="h-7 w-7 text-muted-foreground" />
  {t("nav:checks")}
</h1>
```

If a count or subtitle currently lives next to the title, keep it
adjacent:

```tsx
<div className="flex items-center justify-between">
  <h1 className="text-3xl font-bold tracking-tight flex items-center gap-2">
    <ListChecks className="h-7 w-7 text-muted-foreground" />
    {t("nav:checks")}
    {count !== undefined && (
      <span className="text-muted-foreground text-base font-normal">
        ({count})
      </span>
    )}
  </h1>
  {/* existing right-side actions */}
</div>
```

### 3. Incidents list page h1 — `routes/orgs/$org/incidents.tsx`

Same pattern:

```tsx
<h1 className="text-3xl font-bold tracking-tight flex items-center gap-2">
  <AlertTriangle className="h-7 w-7 text-muted-foreground" />
  {t("nav:incidents")}
</h1>
```

### 4. Events list page h1 — `routes/orgs/$org/events.tsx`

Existing `<h1>` is present (grep confirmed). Add icon:

```tsx
<h1 className="text-3xl font-bold tracking-tight flex items-center gap-2">
  <Calendar className="h-7 w-7 text-muted-foreground" />
  {t("nav:events")}
</h1>
```

### 5. Status pages list h1 — `routes/orgs/$org/status-pages.tsx`

(This is the LIST page — not the detail spec 52 covers.)

```tsx
<h1 className="text-3xl font-bold tracking-tight flex items-center gap-2">
  <Globe className="h-7 w-7 text-muted-foreground" />
  {t("nav:statusPages")}
</h1>
```

### 6. Badges page h1 — `routes/orgs/$org/badges.tsx`

Existing `<h1>` present (grep confirmed). Add icon:

```tsx
<h1 className="text-3xl font-bold tracking-tight flex items-center gap-2">
  <BadgeCheck className="h-7 w-7 text-muted-foreground" />
  {t("nav:badges")}
</h1>
```

## Files to modify

- `web/dash0/src/routes/orgs/$org.tsx` — Breadcrumbs (badges + test
  branches, icon imports).
- `web/dash0/src/routes/orgs/$org/checks.tsx`
- `web/dash0/src/routes/orgs/$org/incidents.tsx`
- `web/dash0/src/routes/orgs/$org/events.tsx`
- `web/dash0/src/routes/orgs/$org/status-pages.tsx`
- `web/dash0/src/routes/orgs/$org/badges.tsx`

No translation file edits.

For each list-page file, add the appropriate icon to its
`lucide-react` import. Use `useTranslation("nav")` if not already
imported (or extend an existing call with the `nav` namespace).

## Verification

Manual walk + Playwright smoke:

1. Login, sidebar visible. Click each of: Dashboard, Checks, Incidents,
   Events, Status Pages, Badges.
2. For each:
   - Breadcrumb area shows icon + section title (matching the sidebar
     icon).
   - Page h1 shows icon + section title (same icon, larger).
3. Switch to FR locale. Repeat — labels translate, icons unchanged.
4. Test mode (`SP_RUNMODE=test`):
   - Login as `test@test.com`, click "testTools" in sidebar.
   - Breadcrumb shows `Bug` icon + the testTools label.
5. No regressions on detail pages (visit a check detail, an incident
   detail, a status page detail — their own headers are untouched).

## Implementation plan

1. Add Breadcrumbs `badges` + `test` branches in `$org.tsx`.
2. Add icon to each list-page h1 (one commit per page or one bundled
   commit — six small JSX changes).
3. Run Playwright across the six routes.
4. `make fmt && make lint-dash`.

If the bundled commit grows past ~80 lines of diff, split per page —
otherwise keep it single.

## Critical files

- `web/dash0/src/components/layout/AppSidebar.tsx` L44–75 — canonical
  source of icon-per-section.
- `web/dash0/src/routes/orgs/$org.tsx` L73–264 — Breadcrumbs.
- The six list-page files listed under "Files to modify".
- `web/dash0/src/locales/en/nav.json`, `fr/nav.json` — verify all
  required keys present (dashboard, checks, incidents, events,
  statusPages, badges, testTools).
