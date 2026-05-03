# Dashboard page — add "Dashboard" breadcrumb and h1-with-icon

## Context

The org dashboard at `/dash0/orgs/$org` (component
`web/dash0/src/components/dashboard/dashboard-page.tsx`, header at
~L195–214) currently renders:

```
[☰ sidebar-trigger] |                    ← breadcrumb area is EMPTY
                    |
default                                  ← h1 = orgName ("default")
{subtitle from i18n}
```

The breadcrumb area is empty because `Breadcrumbs` in
`web/dash0/src/routes/orgs/$org.tsx` (lines 73–264) has explicit
branches for checks, incidents, events, status-pages, account,
organization, and server — but the dashboard route falls through all
of them and returns `null` at line 264.

The user wants:

1. The breadcrumb area to show **"Dashboard"** with the
   `LayoutDashboard` icon, mirroring every other section.
2. The h1 to show **"Dashboard"** with the same icon. The org name
   ("default") moves to a small muted subtitle so the org context is
   preserved.

## Honest opinion

1. **Don't drop the org name.** The current h1 (`{orgName}`) is the
   only place on this page that surfaces *which org you're viewing*.
   The sidebar org-switcher is collapsible. Keep the org name as a
   subtitle below the new h1.
2. **The dashboard breadcrumb gap is symptomatic.** Other top-level
   routes (`/badges`, `/test` in test mode) have the same problem.
   This spec covers only dashboard because that's what the user asked
   for — the sweep is in spec 56.
3. **Reuse existing translation keys.** `nav.json` already has
   `"dashboard": "Dashboard"` (en) / `"Tableau de bord"` (fr). No new
   keys.
4. **Reuse the same `LayoutDashboard` icon as the sidebar.** Visual
   anchoring with the sidebar nav item the user just clicked.

## Scope

**In scope**

- Add a `Breadcrumbs` branch for the dashboard route in
  `web/dash0/src/routes/orgs/$org.tsx`.
- Replace the dashboard h1 with "Dashboard" + icon; move org name to
  a subtitle in `web/dash0/src/components/dashboard/dashboard-page.tsx`.

**Out of scope**

- Other empty breadcrumb branches (badges, test) — spec 56.
- Adding icons to other list-page h1s — spec 56.
- Layout / data-fetching / KPI tile changes on the dashboard.
- Changing the `subtitle` translation copy — keep it.

## Per-element changes

### 1. `Breadcrumbs` component — add dashboard branch

File: `web/dash0/src/routes/orgs/$org.tsx`, function `Breadcrumbs`
(lines 73–264).

Add the import:

```tsx
import {
  /* … existing icons … */
  LayoutDashboard,
} from "lucide-react";
```

Detect the dashboard route. The TanStack file-based router emits
`/orgs/$org/` (with trailing slash) for `routes/orgs/$org/index.tsx`.
Verify in `routeTree.gen.ts` and adjust if the canonical id differs:

```tsx
const isDashboard = routeIds.has("/orgs/$org/");
```

Place the branch **first** (before the existing `isChecks` branch),
right after the variable declarations at ~L84:

```tsx
if (isDashboard) {
  return (
    <span className={activeClass}>
      <LayoutDashboard className={iconClass} />
      {t("dashboard")}
    </span>
  );
}
```

Leave all other branches untouched.

### 2. Dashboard h1 — replace org name with "Dashboard"

File: `web/dash0/src/components/dashboard/dashboard-page.tsx`,
~L195–214.

Add `LayoutDashboard` to the existing `lucide-react` import block at
the top (lines 7–16):

```tsx
import {
  Activity,
  AlertTriangle,
  ArrowRight,
  CheckCircle,
  Clock,
  LayoutDashboard,
  ListChecks,
  Plus,
  RefreshCw,
} from "lucide-react";
```

Add a second `useTranslation` call for the `nav` namespace (the file
already calls `useTranslation` for its own namespace; mirror the
pattern from `AppSidebar.tsx` line 124):

```tsx
const { t: tNav } = useTranslation("nav");
```

Current header (lines 197–214):

```tsx
<div className="flex items-center justify-between">
  <div>
    <h1 className="text-3xl font-bold tracking-tight">{orgName}</h1>
    <p className="text-muted-foreground">{t("subtitle")}</p>
  </div>
  <div className="flex items-center gap-3 text-sm text-muted-foreground">
    {updatedLabel ? <span>{updatedLabel}</span> : null}
    <Button
      variant="outline"
      size="icon"
      onClick={refreshAll}
      disabled={isRefetching}
      aria-label={t("refresh")}
    >
      <RefreshCw className={`h-4 w-4 ${isRefetching ? "animate-spin" : ""}`} />
    </Button>
  </div>
</div>
```

Target:

```tsx
<div className="flex items-center justify-between">
  <div>
    <h1 className="text-3xl font-bold tracking-tight flex items-center gap-2">
      <LayoutDashboard className="h-7 w-7 text-muted-foreground" />
      {tNav("dashboard")}
    </h1>
    <p className="text-sm text-muted-foreground">
      <span className="font-medium text-foreground">{orgName}</span>
      {" — "}
      {t("subtitle")}
    </p>
  </div>
  <div className="flex items-center gap-3 text-sm text-muted-foreground">
    {updatedLabel ? <span>{updatedLabel}</span> : null}
    <Button
      variant="outline"
      size="icon"
      onClick={refreshAll}
      disabled={isRefetching}
      aria-label={t("refresh")}
    >
      <RefreshCw className={`h-4 w-4 ${isRefetching ? "animate-spin" : ""}`} />
    </Button>
  </div>
</div>
```

Notes:

- `{orgName}` keeps the org name visible and slightly emphasized
  (`text-foreground`).
- Em-dash separator + existing `t("subtitle")` preserved.
- Icon size `h-7 w-7` roughly matches `text-3xl` weight. If it looks
  heavy in dark mode drop to `h-6 w-6`.

## Files to modify

- `web/dash0/src/routes/orgs/$org.tsx` — Breadcrumbs branch + import.
- `web/dash0/src/components/dashboard/dashboard-page.tsx` — h1 + subtitle
  + import + extra `useTranslation` call.

No translation file edits. No new icon imports beyond
`LayoutDashboard`.

## Verification

Playwright smoke (use `web/dash0/e2e/`):

1. Login as `admin@solidping.com`, navigate to `/dash0/orgs/default`.
2. Assert the breadcrumb area in the top header contains a
   `LayoutDashboard` SVG and the text "Dashboard" (or "Tableau de
   bord" with FR locale).
3. Assert the page h1 contains:
   - A `LayoutDashboard` icon
   - The text "Dashboard" / "Tableau de bord"
4. Assert the subtitle line contains `default` (the org name) AND the
   existing subtitle copy.
5. Switch language to French via the sidebar language switcher → the
   breadcrumb and h1 say "Tableau de bord".
6. Manual: navigate to `/dash0/orgs/default/checks` and back —
   existing breadcrumb branches unchanged.

## Implementation plan

1. Add `Breadcrumbs` dashboard branch in `$org.tsx`.
2. Update dashboard h1 + subtitle in `dashboard-page.tsx`.
3. Run Playwright dashboard smoke.
4. `make fmt && make lint-dash`.

## Critical files

- `web/dash0/src/routes/orgs/$org.tsx` — Breadcrumbs (L73–264).
- `web/dash0/src/components/dashboard/dashboard-page.tsx` — h1
  (L195–214), imports (L7–16).
- `web/dash0/src/components/layout/AppSidebar.tsx` — canonical
  `LayoutDashboard` import (L5) and nav-item structure (L44–49).
- `web/dash0/src/locales/{en,fr}/nav.json` — `"dashboard"` key
  already present.
