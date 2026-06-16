# Standardize every page header on the boxed PageHeader (discovery) style

## Context

The **Network Discovery** page
(`https://solidping.k8xp.com/dash0/orgs/default/discovery`) renders its title
with the shared `PageHeader` component
([`web/dash0/src/components/shared/page-header.tsx`](../../web/dash0/src/components/shared/page-header.tsx)):
an **icon inside a rounded muted square tile** (`h-10 w-10 rounded-md bg-muted`),
a **`text-2xl font-semibold tracking-tight` title**, an optional **muted
subtitle** (`text-sm text-muted-foreground`), and right-aligned `actions`.

Almost every other top-level page instead hand-rolls an **inline** header:

```tsx
<h1 className="text-3xl font-bold tracking-tight flex items-center gap-2">
  <SomeIcon className="h-7 w-7 text-muted-foreground" />
  {t("title")}
</h1>
<p className="text-muted-foreground">{t("subtitle")}</p>
```

The user wants **all pages to look like discovery** — i.e. every page title
should be rendered through `PageHeader`.

### This reverses a documented decision — update the design reference

The design reference
([`web/dash0/src/routes/orgs/$org/design-reference.tsx`](../../web/dash0/src/routes/orgs/$org/design-reference.tsx),
the `PageHeaderSection`, ~lines 401–482) **currently documents the inline
`text-3xl` header as the canonical list-page pattern and explicitly labels the
boxed `PageHeader` as "Legacy — do not reach for it on new list pages."** That
guidance is now obsolete. This spec flips it: **`PageHeader` (the boxed style)
becomes the single canonical page header**, and the inline `text-3xl` style is
retired. The design reference must be rewritten to say so, otherwise the headers
will drift right back. (The section even carries a follow-up note recommending a
shared header component — `PageHeader` already is that component, so we adopt it
rather than build a new one.)

CLAUDE.md requires the design reference to stay the single source of truth, so
updating it is part of this change, not optional.

## Goals

- Every page below renders its title via
  `<PageHeader icon={…} title={…} description={…} actions={…} />` from
  `@/components/shared/page-header`, matching discovery exactly.
- The **primary action(s) that currently sit in the header row** (e.g. "+ New X",
  export/import, the refresh button on the dashboard) move into the `actions`
  prop. Secondary toolbars on their own row **below** the header (filter
  selects, search inputs, standalone refresh rows) stay where they are.
- Subtitles are preserved (same translation keys / strings).
- The reused icon stays the same per page (just rendered inside the tile now).
- The design reference is rewritten so `PageHeader` is canonical; the e2e
  style test is updated to match.

### Pages to migrate

| Page | Route file | Icon | Title / subtitle source | Header-row action(s) → `actions` |
|---|---|---|---|---|
| Dashboard | [`components/dashboard/dashboard-page.tsx`](../../web/dash0/src/components/dashboard/dashboard-page.tsx) (`OrgDashboardPage`, ~L307) | `LayoutDashboard` | `tNav("dashboard")` / rich: **orgName** + " — " + `t("subtitle")` | "updated …" label span + refresh icon button |
| Checks | [`checks.index.tsx`](../../web/dash0/src/routes/orgs/$org/checks.index.tsx) (~L725) | `ListChecks` | `t("title")` / `t("subtitle")` | Export, Import, Refresh, + New cluster |
| Incidents | [`incidents.index.tsx`](../../web/dash0/src/routes/orgs/$org/incidents.index.tsx) (~L118) | `AlertTriangle` | `t("title")` / `t("subtitle")` | none (filters are a row below) |
| Dependencies | [`dependencies.index.tsx`](../../web/dash0/src/routes/orgs/$org/dependencies.index.tsx) (~L76) | `GitBranch` | `t("dependencies:list.title")` / `…subtitle` | none |
| On-call | [`on-call.index.tsx`](../../web/dash0/src/routes/orgs/$org/on-call.index.tsx) (~L77) | `CalendarClock` | `t("oncall:list.title")` / `…subtitle` | + New (`Link`→`/on-call/new`) |
| Escalation policies | [`escalation-policies.index.tsx`](../../web/dash0/src/routes/orgs/$org/escalation-policies.index.tsx) (~L100) | `ArrowUpRight` | `t("escalation:list.title")` / `…subtitle` | + New (`Link`→`/escalation-policies/new`) |
| Events | [`events.tsx`](../../web/dash0/src/routes/orgs/$org/events.tsx) (~L73) | `Calendar` | `t("title")` / `t("subtitle")` | none |
| Integrations | [`integrations.index.tsx`](../../web/dash0/src/routes/orgs/$org/integrations.index.tsx) (~L91) | `Bell` | `t("title", "Integrations")` / `t("subtitle", …)` | + New (`Link`→`/integrations/new`) |
| Status Pages | [`status-pages.index.tsx`](../../web/dash0/src/routes/orgs/$org/status-pages.index.tsx) (~L170) | `Globe` | `t("statusPages:title")` / `…subtitle` | + New (`Link`→`/status-pages/new`) |
| Badges | [`badges.tsx`](../../web/dash0/src/routes/orgs/$org/badges.tsx) (~L421) | `BadgeCheck` | `t("title")` / `t("subtitle")` | none (keep the conditional back-link, see below) |
| My Pages | [`me.notifications.tsx`](../../web/dash0/src/routes/orgs/$org/me.notifications.tsx) (~L48) | `BellRing` | `"My pages"` / `"Incidents you were paged for…"` (hardcoded strings) | none |

Discovery, the jobs index, and the integration-detail page already use
`PageHeader` — they are the model and need no change.

## Out of scope

- Detail / edit / new pages with the back-arrow header cluster (check detail,
  status-page detail, on-call detail, etc.). Only the listed pages change.
- Wording of any title/subtitle, icon choices, the breadcrumb bar in
  `$org.tsx`, table/card bodies, filter/search rows, and routing.
- Extracting a brand-new header component — `PageHeader` already exists; reuse it.

## Implementation

### 1. Widen `PageHeader.description` to `ReactNode`

`PageHeader`'s `description` is typed `string`, but the **dashboard** subtitle is
rich JSX (bold org name + " — " + subtitle). In
[`page-header.tsx`](../../web/dash0/src/components/shared/page-header.tsx) change:

```ts
description?: string;
```
to
```ts
description?: ReactNode;
```

`ReactNode` is already imported. The render already wraps it in
`<p className="mt-1 text-sm text-muted-foreground">`, which is what the
dashboard wants. Widening `string`→`ReactNode` is backward-compatible with the
existing callers (discovery, jobs, integration detail).

### 2. Migrate each page

For every page in the table:

1. Add `import { PageHeader } from "@/components/shared/page-header";`.
2. Replace the header markup — the `<div className="flex items-center justify-between">…</div>`
   wrapper (or bare `<div>` for pages with no action) containing the `<h1>` and
   subtitle `<p>` — with a single:

   ```tsx
   <PageHeader
     icon={SomeIcon}
     title={t("title")}
     description={t("subtitle")}
     actions={/* the former header-row action cluster, or omit */}
     className="flex-wrap"   // keep actions from overflowing on mobile
   />
   ```

3. The icon import stays (it is now passed as `icon`, not rendered inline).
   Drop the inline `<Icon className="h-7 w-7 …" />` JSX.
4. Move whatever was in the right side of the old header row into `actions`
   (verbatim — same buttons/links). Leave rows that live **below** the header
   untouched. Pages with no header-row action omit `actions` entirely.
5. The outer `<div className="space-y-6">` page wrapper stays.

**Dashboard specifics** (`dashboard-page.tsx`): pass `icon={LayoutDashboard}`,
`title={tNav("dashboard")}`, and a JSX `description` reproducing the current
subtitle:

```tsx
description={
  <>
    <span className="font-medium text-foreground">{orgName}</span>
    {" — "}
    {t("subtitle")}
  </>
}
```

Put the "updated" label span + refresh button into `actions`. The body
(`isInitialLoading ? … : isEmptyOrg ? … : …`) is unchanged.

**Badges specifics** (`badges.tsx`): the conditional "← back to check" `Link`
that currently renders **above** the `<h1>` must be kept. Render it immediately
above `<PageHeader>` (either as a sibling before the component, or keep the
wrapping `<div>` and place the `Link` then the `PageHeader` inside it). Do not
fold it into the PageHeader title.

**My Pages** (`me.notifications.tsx`): titles are hardcoded — pass the same
literals (`title="My pages"`, the subtitle string) as props. Keep the
`data-testid="my-notifications-page"` on the outer wrapper.

### 3. Rewrite the design reference

In [`design-reference.tsx`](../../web/dash0/src/routes/orgs/$org/design-reference.tsx)
`PageHeaderSection` (~L401–482):

- Make the **live preview and the code snippet show `<PageHeader>`** (boxed icon
  tile, `text-2xl font-semibold`) as the canonical header.
- Rewrite the `Section` `description` and the notes so they state: *the boxed
  `PageHeader` component is the canonical page header for every list and section
  page; pass `icon`, `title`, optional `description`, and right-aligned
  `actions`.*
- Replace the "Legacy: the boxed PageHeader … do not reach for it" callout with
  the inverse: the **inline `text-3xl` header is the legacy pattern, retired —
  migrate any remaining inline headers to `PageHeader`.**
- Remove or update the now-satisfied "Follow-up … extract a shared
  ListPageHeader/DetailPageHeader" note (the shared component is `PageHeader`).

### 4. Update the e2e style test

[`e2e/listing-pages-style.spec.ts`](../../web/dash0/e2e/listing-pages-style.spec.ts)
asserts `await expect(h1).toHaveClass(/text-3xl/)` for the dependencies (L98),
on-call (L124), and channels/integrations (L180) pages. `PageHeader` renders the
title at `text-2xl`, so change all three assertions to `/text-2xl/`. The
`getByRole("heading", { level: 1 })` lookups still work (PageHeader renders an
`<h1>`), as do the name-based heading checks in `incidents.spec.ts`,
`badges.spec.ts`, `jobs.spec.ts`, etc. — no change needed there.

## Verification

1. `make dev-test` (or the dash0 dev server against the backend); sign in.
2. Visit each migrated page and confirm the header now matches discovery: a
   rounded muted **icon tile** on the left, a `text-2xl font-semibold` title,
   the subtitle below, and any primary action(s) right-aligned.
   - Dashboard: org name still bold in the subtitle; refresh button works.
   - Checks: Export / Import / Refresh / + New all present and functional.
   - On-call / Escalation / Integrations / Status Pages: "+ New X" still routes
     to the corresponding `/new` page.
   - Badges: the conditional "← back to check" link still appears above the
     header when a check is selected.
3. Mobile width (~375 px): headers wrap, actions don't overflow, titles
   truncate rather than push the action off-screen.
4. Open `/dash0/orgs/default/design-reference` → **Page header** section: the
   preview/snippet show `PageHeader` as canonical; the inline style is marked
   legacy.
5. `bun run lint` and `bun run build` pass in `web/dash0` (tsc is a hard gate).
6. `make test-dash` (Playwright) is green — in particular the updated
   `listing-pages-style.spec.ts` (`text-2xl`) and the heading-name assertions in
   `incidents`, `badges`, `jobs` specs.

## Files referenced

- `web/dash0/src/components/shared/page-header.tsx` — widen `description` to `ReactNode`.
- `web/dash0/src/components/dashboard/dashboard-page.tsx` — Dashboard header.
- `web/dash0/src/routes/orgs/$org/checks.index.tsx`
- `web/dash0/src/routes/orgs/$org/incidents.index.tsx`
- `web/dash0/src/routes/orgs/$org/dependencies.index.tsx`
- `web/dash0/src/routes/orgs/$org/on-call.index.tsx`
- `web/dash0/src/routes/orgs/$org/escalation-policies.index.tsx`
- `web/dash0/src/routes/orgs/$org/events.tsx`
- `web/dash0/src/routes/orgs/$org/integrations.index.tsx`
- `web/dash0/src/routes/orgs/$org/status-pages.index.tsx`
- `web/dash0/src/routes/orgs/$org/badges.tsx`
- `web/dash0/src/routes/orgs/$org/me.notifications.tsx`
- `web/dash0/src/routes/orgs/$org/design-reference.tsx` — flip canonical guidance.
- `web/dash0/e2e/listing-pages-style.spec.ts` — `text-3xl` → `text-2xl` (3 spots).
