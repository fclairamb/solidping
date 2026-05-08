# Dash0 design reference page

## Context

The dash0 dashboard has 22 shadcn-style UI primitives in `web/dash0/src/components/ui/` plus several established higher-level patterns (breadcrumbs, page headers with icons, tables with debounced search, empty/loading/error states, dialogs, toasts). Today these patterns are implemented inline across feature routes (`checks.index.tsx`, `incidents.$incidentUid.tsx`, `account.security.tsx`, etc.) and there is no central place a contributor can look at to see the canonical implementation.

That gap is the source of small drift: a new feature reinvents the search input wiring, picks the wrong status colour, omits the loading skeleton, or builds a one-off page header. We want a single dev-facing reference page that gathers all of it in one scrollable view, with live components, the matching `import` line next to each example, and a theme toggle to verify dark-mode parity.

The page is **for contributors, not end-users** — visible in the sidebar only when the backend reports `runMode === "test"` (same gate that hides the existing `/orgs/$org/test` tools). The route is reachable by URL in any environment for cross-env debugging.

## Approach

**One page, in-page anchored sections.** Not tabs, not sub-routes:

- A reference is a single logical surface — contributors scroll it or `Cmd-F` it.
- TanStack Router code-splits per route, so a multi-tab layout would *slow down* a doc page (4 navigations to see everything) for no benefit.
- Two new files (page header component + the route) instead of six (tabs + parent layout + sub-routes + nav-key wiring).

**`<PageHeader>` lives in `components/shared/`, not `components/ui/`.** The `ui/` folder is reserved for thin Radix wrappers (one Radix import per file — see `button.tsx`, `card.tsx`). `PageHeader` embeds a layout opinion (icon-on-left) and composes primitives, which is what `components/shared/` is for (`tab-nav.tsx`, `error-views.tsx`, `status-dashboard.tsx`).

**i18n exception — hardcoded English in the page body.** Only the sidebar entry is translated (one new `nav.json` key). A comment at the top of the route file makes the exception explicit so future contributors don't add keys "for consistency". This is a developer reference, not user-facing copy.

## Files to create

### `web/dash0/src/components/shared/page-header.tsx`

A reusable header component that establishes the icon-on-left + title + description + right-aligned actions pattern. The rest of the app should adopt it for new pages over time (no big-bang refactor in this spec).

Shape:

```ts
import type { LucideIcon } from "lucide-react";
import type { ReactNode } from "react";

type Props = {
  icon: LucideIcon;       // required — the convention
  title: string;
  description?: string;
  actions?: ReactNode;    // right-aligned (e.g. "New" button)
};
```

Stateless, presentational. Callers pass already-translated strings. Layout: `flex items-start gap-3` with the icon in a small rounded square (h-10 w-10, `bg-muted text-foreground`), title as `text-2xl font-semibold tracking-tight`, description as `text-sm text-muted-foreground mt-1`, actions slot pushed right with `ml-auto`.

### `web/dash0/src/routes/orgs/$org/design-reference.tsx`

Single file-based route at `/orgs/$org/design-reference`. Auth is automatic via the parent `$org.tsx` `beforeLoad` guard.

Top-of-file comment:

```tsx
// Dev-facing component reference. All in-page labels are intentionally
// hardcoded English — do not add i18n keys for the showcase content.
// Only the sidebar entry (nav:designReference) is translated.
```

Layout: a sticky top sub-nav (anchor pills under `<PageHeader>`, **not** a left rail — a left rail would fight the AppSidebar) with one anchor per section. Then the following sections, each with an `id` for in-page deep-linking:

1. **#overview** — purpose statement, on-page theme toggle (so contributors verify dark-mode parity without leaving the page), tip about opening the source file (`web/dash0/src/routes/orgs/$org/design-reference.tsx`) when copying.
2. **#page-header** — live `<PageHeader>` example, then the `import { PageHeader } from "@/components/shared/page-header"` line in a `<pre>` with a copy-to-clipboard button.
3. **#breadcrumbs** — short note that breadcrumbs are route-driven (not a drop-in component) and live in `Breadcrumbs()` inside `web/dash0/src/routes/orgs/$org.tsx`. Show a snippet illustrating how to add a section branch (with the same shape as the existing `isChecks`, `isIncidents` flags). The page itself has live breadcrumbs at the top of the viewport demonstrating the pattern in action.
4. **#color-tokens** — swatches for `--primary`, `--destructive`, `--status-ok`, `--status-warning`, `--status-error`, `--muted-foreground`, `--accent`, plus the chart palette `--chart-1`..`--chart-5`. Each labeled with its CSS variable name so contributors pick the **token**, not a hex.
5. **#buttons-badges** — Button (default / destructive / outline / secondary / ghost / link, plus all sizes including `icon`), Badge (default / success / warning / destructive / outline). Each example sits next to its import line with a copy button.
6. **#forms** — Input, Textarea, PasswordInput, Select, Checkbox, Switch, Label. Plus one assembled `<form>` example showing the canonical label / input / help-text / error spacing (`space-y-2` per field, `space-y-4` between fields).
7. **#data-display** — a small `<Table>` with debounced `<Input>` search and a status `<Badge>` per row, modeled directly on `checks.index.tsx` (reuse the existing `useDebounce` hook) but reduced to ~5 mock rows. Render **three side-by-side variants**: happy path, loading skeleton, empty state. That's where new features cut corners today, so make the canonical version visible.
8. **#feedback** — Alert (4 variants: default, destructive, warning, success), Dialog, AlertDialog, Sonner toast trigger, Tooltip, Popover, Dropdown-menu. Each rendered live with a button to open the dialog/toast.

Every showcased component has its `import { … } from "@/components/…"` line shown in a `<pre>` with a copy-to-clipboard button next to it — the single most-useful feature on the page.

## Files to edit

### `web/dash0/src/components/layout/AppSidebar.tsx`

Add the entry to `testNavItems` (the array currently at lines 101–107) so it only appears when `versionData?.runMode === "test"` (gate already in place at line 217):

```ts
const testNavItems = [
  { titleKey: "testTools",       path: "/orgs/$org/test"             as const, icon: Bug },
  { titleKey: "designReference", path: "/orgs/$org/design-reference" as const, icon: Palette },
];
```

Add `Palette` to the `lucide-react` import block at the top.

### `web/dash0/src/routes/orgs/$org.tsx`

Add a section flag and a small breadcrumb branch in `Breadcrumbs()`:

- Around lines 100–108 (alongside `isChecks`, `isIncidents`, etc.), add:
  ```ts
  const isDesignReference = matches.some((m) => m.routeId.startsWith("/orgs/$org/design-reference"));
  ```
- Before the final `return null` (currently around line 444), add:
  ```tsx
  if (isDesignReference) {
    return (
      <span className={activeClass}>
        <Palette className={iconClass} />
        Design Reference
      </span>
    );
  }
  ```
- Add `Palette` to the `lucide-react` import block at the top.

This is meta-appropriate for a reference page — its live breadcrumb in the header demonstrates the very pattern the page documents.

### `web/dash0/src/locales/{en,fr,de,es}/nav.json` (4 files, single commit)

Add a `designReference` key:

| Locale | Value |
|---|---|
| `en` | `Design Reference` |
| `fr` | `Référence design` |
| `de` | `Design-Referenz` |
| `es` | `Referencia de diseño` |

All four files must land in the same commit — otherwise the missing-key fallback shows the literal `nav:designReference` in untranslated locales.

## Patterns / utilities reused

- `cn()` from `@/lib/utils` (clsx + tailwind-merge).
- `useDebounce` hook (already used by `checks.index.tsx` for search) — reuse in the table demo.
- All 22 primitives in `web/dash0/src/components/ui/`.
- The `testNavItems` visibility pattern in `AppSidebar.tsx` (lines 217–242).
- The `Breadcrumbs()` `routeId.startsWith(...)` matching pattern in `$org.tsx`.
- `Sonner` for toast triggers (already wired via `<Toaster />` in the root layout).

No new dependencies.

## Out of scope

- **Charts (Recharts) showcase.** Useful but adds significant surface; defer until someone asks.
- **Extracting a reusable `<DataTable>` primitive.** This spec only *demonstrates* the existing table pattern; refactoring it into a shared component is a separate change.
- **Migrating existing pages to use `<PageHeader>`.** New component; existing pages keep their inline headers until touched for other reasons.
- **A public link from the dashboard footer or help menu.** Sidebar entry only.
- **Storybook / MDX-based docs tooling.** Plain React route; no new tooling.
- **Adding a `designReference` feature flag to `/api/v1/features`.** Visibility is purely client-side via the existing `runMode === "test"` gate.

## Verification

1. **Build & lint**: `cd web/dash0 && bun run lint && bun run build` — both pass clean.
2. **Run**: `make dev-test` from repo root (backend on `:4000`, dash0 on `:5174`, run mode `test`).
3. **Login** at `http://localhost:4000/dash0/orgs/test/login` as `test@test.com` / `test`.
4. **Sidebar visibility**: confirm "Design Reference" appears in the test-tools group of the sidebar (not in the main nav). Click it; URL becomes `/orgs/test/design-reference`.
5. **Page renders**: overview, page-header, breadcrumbs note, colour tokens, buttons & badges, forms, data display (3 states side-by-side), feedback sections all render without console errors.
6. **Theme parity**: toggle the on-page theme switch; every section must look correct in both light and dark. Single most important visual check.
7. **Live interactions**: open and dismiss each Dialog, AlertDialog, Tooltip, Dropdown-menu, Popover; trigger a Sonner toast. All must work.
8. **Copy buttons**: click a copy-to-clipboard button next to an import line; paste into a scratch file; the import string must be exact and complete.
9. **Anchor links**: click each pill in the sticky sub-nav; the page scrolls to the matching section. URL hash updates (`#buttons-badges`, etc.).
10. **Production gate**: temporarily run with a non-test run mode (or check against staging) and confirm the sidebar entry does **not** appear, while the URL is still reachable and renders correctly.
11. **Breadcrumb**: confirm the header shows `Design Reference` with the Palette icon when on this route.
12. **Translations**: switch to FR / DE / ES via the LanguageSwitcher in the sidebar footer; the sidebar entry label must be translated (no `nav:designReference` literal). The page body content stays in English (intentional, per the top-of-file comment).

## Implementation Plan

Concrete commit breakdown the implementer will follow:

1. **PageHeader component** — Create `web/dash0/src/components/shared/page-header.tsx`. Stateless presentational component with `icon` (LucideIcon, required), `title`, `description?`, `actions?`. Layout per spec: `flex items-start gap-3`, icon in `h-10 w-10` rounded square with `bg-muted text-foreground`, title `text-2xl font-semibold tracking-tight`, description `text-sm text-muted-foreground mt-1`, actions slot pushed right with `ml-auto`.

2. **Design reference route — overview + PageHeader sections** — Create `web/dash0/src/routes/orgs/$org/design-reference.tsx` with the file-level "no i18n in body" comment, sticky sub-nav of anchor pills, a small `<CodeSnippet>` helper component (a `<pre>` + clipboard copy button used throughout the page), then `#overview` (purpose + on-page theme toggle using existing theme infra) and `#page-header` (live PageHeader example + import line).

3. **Design reference route — breadcrumbs + color-tokens sections** — Add `#breadcrumbs` (note + a `<pre>` snippet showing the `routeId.startsWith` pattern from `$org.tsx`) and `#color-tokens` (swatches for the listed CSS variables + `--chart-1..5`).

4. **Design reference route — buttons & badges section** — `#buttons-badges` with all Button variants/sizes (default, destructive, outline, secondary, ghost, link; sizes default, sm, lg, icon) and Badge variants (default, success, warning, destructive, outline). Each example sits next to its import line.

5. **Design reference route — forms section** — `#forms` with Input, Textarea, PasswordInput, Select, Checkbox, Switch, Label, plus one assembled form showing canonical label/input/help/error spacing.

6. **Design reference route — data display section** — `#data-display`: Table + debounced Input search + status Badge per row, modeled on `checks.index.tsx` with ~5 mock rows. Render three side-by-side variants — happy, loading skeleton, empty.

7. **Design reference route — feedback section** — `#feedback`: Alert (4 variants), Dialog, AlertDialog, Sonner toast trigger button, Tooltip, Popover, Dropdown-menu — each rendered live.

8. **AppSidebar wiring** — Edit `web/dash0/src/components/layout/AppSidebar.tsx`: import `Palette` from `lucide-react`, add `{ titleKey: "designReference", path: "/orgs/$org/design-reference" as const, icon: Palette }` to `testNavItems`.

9. **Breadcrumb branch** — Edit `web/dash0/src/routes/orgs/$org.tsx`: import `Palette`, add `isDesignReference` flag alongside the other section flags, add the breadcrumb branch returning the Palette icon + "Design Reference" label.

10. **Translations (single commit)** — Add `designReference` to `web/dash0/src/locales/{en,fr,de,es}/nav.json` in the same commit so the literal key never shows in any locale.

11. **QA** — `make build-backend build-client lint-back test`. Iterate until clean.

12. **Completeness audit** — Spawn subagent to verify every spec requirement (component shape, route sections, sidebar gate, breadcrumb, all four locales, copy buttons, theme toggle).

13. **Archive + merge** — Move spec to `specs/done/2026/05/` and merge `feat/dash0-design-reference-page` into `main`.
