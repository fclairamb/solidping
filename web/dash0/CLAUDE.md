# Dash0 - Operator Dashboard

A React-based multi-tenant authenticated admin app for SolidPing operators.
This is the primary operator UI: it manages checks, incidents, status pages,
organizations, members, tokens, and integrations. The public read-only status
page (subscriber view) lives in `web/status0`, not here — do not conflate the
two.

## Tech Stack

- **Framework**: React 19 with TypeScript
- **Build Tool**: Vite 7
- **Package Manager**: Bun
- **Routing**: TanStack Router (file-based routing)
- **Data Fetching**: TanStack Query (React Query)
- **Styling**: Tailwind CSS v4
- **UI Components**: Radix UI primitives + custom shadcn/ui-style components
- **Charts**: Recharts
- **Icons**: Lucide React

## Project Structure

```
web/dash0/
├── src/
│   ├── components/
│   │   ├── dashboard/        # Org dashboard (welcome page) and shared event display helpers
│   │   ├── shared/           # Cross-feature business logic components
│   │   ├── checks/           # Check list, form, summary cards, charts
│   │   ├── layout/           # AppSidebar, OrgLayout
│   │   └── ui/               # Reusable UI primitives
│   ├── routes/               # File-based routes
│   │   ├── __root.tsx        # Root layout
│   │   └── index.tsx         # Main status page
│   ├── lib/                  # Utility functions
│   ├── main.tsx              # Application entry point
│   └── index.css             # Global styles and Tailwind
├── vite.config.ts            # Vite configuration
├── package.json              # Dependencies and scripts
└── tsconfig.json             # TypeScript configuration
```

## Development

### Prerequisites

- **Bun** v1.0+
- **SolidPing backend** running on `http://localhost:4000`

### Commands

```bash
# Install dependencies
bun install

# Start development server (port 5174)
bun run dev

# Build for production
bun run build

# Build without type checking (faster)
bun run build:no-check

# Run linter
bun run lint

# Type-check + lint the Playwright e2e suites (what CI runs)
bun run typecheck:e2e
bun run lint:e2e
```

### Development with Backend

For hot reload development, use the redirect proxy:

```bash
# Terminal 1: Start dash0 dev server
cd web/dash0 && bun run dev

# Terminal 2: Start backend with redirect
SP_REDIRECTS="/d:localhost:5174/d" make dev-backend

# Or use air for Go hot reload
cd /path/to/solidping && air
```

Access at `http://localhost:4000/d/`

## Configuration

### Base URL

The app is served at `/d/` by default. Override with `VITE_BASE_URL`:

```bash
VITE_BASE_URL=/status/ bun run build
```

## API Endpoints Used

The operator app talks to the full authenticated API surface — see the
top-level `CLAUDE.md` for the canonical list. The most-used endpoints in this
client are:

- `GET /api/v1/orgs/{org}/checks` — list checks (`?with=last_result,last_status_change` for the dashboard / list views)
- `GET /api/v1/orgs/{org}/incidents` — incidents, filterable by `state`
- `GET /api/v1/orgs/{org}/events` — audit events
- `GET /api/v1/orgs/{org}/results` — raw and aggregated check results
- `POST/PATCH/DELETE` for the matching resource paths (auth handled by `apiFetch`)

## Features

### Org dashboard (`/orgs/$org`)
- Operator-facing welcome page composed from list endpoints
- Overall status banner (green / yellow / red) keyed off check + incident counts
- 4 KPI tiles (`KpiTile`): monitored checks, 24h availability (the page's hero tile), currently down, active incidents
- Two-column body: Needs attention + Active incidents
- Recent activity feed (last 8 events)
- Per-card error boundaries — one failed query does not blank the page
- Polls at 30s (checks/incidents) and 60s (results/events)

### Public-side status (handled elsewhere)
The subscriber-facing public status page lives in `web/status0`. dash0 only
renders the operator UI — when working on subscriber-facing UX, switch repos.

### Theming
- Light/dark mode support via CSS variables
- Electric-blue identity (spec 2026-09-24-01): `--primary` is the product color, crimson `--brand` is the logo only
- Gradients are rationed: `bg-primary-gradient` (anything with a label), `bg-accent-gradient` (decorative "on" states, no text), `bg-hero-gradient` (one hero tile). They set `background-image` only, so a flat `bg-*` override needs `bg-none` unless it goes through `cn()` (which is taught about them in `lib/utils.ts`). Rules: design reference → Buttons & badges → Gradients
- Always-dark navy sidebar (spec 2026-09-24-02): the `Sidebar` primitive puts `class="dark"` on its root (desktop, icon rail, mobile sheet), so anything nested reads the dark tokens in both themes. The `--sidebar-*` tokens live ONLY in `:root` / `:root.dark` in `index.css` — never in the generic `.dark` block, or the sidebar element would pin them to their dark-mode values in light mode
- Chrome pieces: `PageHeader` has `tone="brand"` (gradient tile, default) / `"neutral"` (flat tile for third-party logos); the org layout owns the page glow (`bg-page-glow`); `KpiTile` (`components/shared/kpi-tile.tsx`) has one `variant="hero"` per page, which crops `--hero-gradient` to its darker end so its 90% white small text stays >= 4.5:1
- Status colors: green (ok), yellow (warning), red (error)

## Design Reference

Before building or modifying any UI, consult the live design reference at
`http://localhost:4000/d/orgs/default/design-reference` (source:
`src/routes/orgs/$org/design-reference.tsx`). It renders every shipped
primitive (buttons, alerts, dialogs, tables, forms…) live in both light and
dark mode, alongside the exact import line. Reuse those components and
patterns rather than reinventing them — if something is missing, add it to
the reference page when you build it so the catalog stays canonical.

**This applies to _every_ frontend change**, not just new pages — always
refer to `src/routes/orgs/$org/design-reference.tsx` first. It is the single
source of truth for components and conventions; do not implement UI that
diverges from it without also updating it.

## UI Conventions

### Editing always changes the route

Editing an entity must navigate to a dedicated route, never open a modal
dialog. Mirror the create flow: `/<resource>/new` for creation,
`/<resource>/$id` (or `/<resource>/$id/edit` if a separate read view exists)
for editing. The edit route should render a full page with the same form
component used by `/new`.

**Why:** routes are bookmarkable, deep-linkable, browser-back works as
expected, and the URL is the source of truth for "what the user is doing."
Modal edits hide state, lose on accidental backdrop clicks, and don't survive
refreshes. Trivial single-field renames (e.g. inline rename a group label)
may stay inline, but anything with a multi-field form goes through a route.

**How to apply:** when adding a new editable resource, scaffold both
`<resource>.new.tsx` and `<resource>.$id.tsx` (or `.edit.tsx`). When
auditing existing pages, treat `<Dialog>` containing an edit form as a bug
to migrate.

### A page's core navigation belongs in the URL

The element that switches what a page is showing — its tabs, and the
scope/status filters that go with them — must live in the URL as search
params, not in `useState`. Like editing, this makes the view bookmarkable
and deep-linkable, makes browser back/forward move between views, and makes
the state survive a refresh. A page whose tabs are local React state is a
bug to migrate, even when the tabs are a Radix `<Tabs>` component.

**How to apply:** declare a `validateSearch` on the route (normalize each
param to a safe default), read it with `Route.useSearch()`, and write it
with `useNavigate({ to: ".", search: (prev) => ({ ...prev, ... }) })` —
mirror `incidents.index.tsx` and `jobs.index.tsx`. The primary navigation
(the tab) should push a history entry so back/forward cycles tabs; pass
`replace: true` only for incidental refinements (filters, scope toggles)
so they don't spam history.

### Row actions: icons, not menus

In list/table rows, prefer two ghost icon buttons (`Pencil` for edit,
`Trash2` for delete, with a `text-destructive` class on the latter) over a
`DropdownMenu` with a `MoreVertical` trigger. The Edit icon links to the
edit route; the Delete icon opens an `AlertDialog` confirmation. Other
per-row actions (toggle enabled, set default, etc.) live on the edit page,
not in the row.

### Delete is always red, always a trash bin

Every delete (or otherwise irreversible) action is rendered in the
destructive red and paired with the `Trash2` (trash bin) icon — no
exceptions. Use `Button variant="destructive"` for prominent/standalone
buttons, an icon button with `text-destructive` in row actions, and
`text-destructive focus:text-destructive` on the delete item inside a
`DropdownMenu`. All resolve to the `--destructive` token so dark mode stays
correct. Reserve destructive red for destructive actions — never use it for a
neutral or primary action, and never delete with a different icon or a muted
color.

## Translations (en, fr, de, es)

Every user-visible string goes through `t()` and exists in all four
`src/locales/<lang>/<namespace>.json` files. Three unit tests and one lint
rule guard this (spec 2026-09-26-01 for the last two):

- `locale-parity.test.ts`: every locale has exactly `en`'s keys.
- `translated-defaults.test.ts`: every `t("key", "default")` resolves to a real
  `en` key.
- `untranslated-values.test.ts`: a fr/de/es value identical to the `en` value
  fails, unless `src/locales/identical-allowlist.ts` names that key for that
  locale. It also requires each translation to keep `en`'s `{{placeholders}}`
  and Trans `<tags>`, fails on stale allowlist entries, and checks
  `web/status0/src/locales` too (status0 has no unit-test runner). Add an
  allowlist entry only for a value that is right as-is (brand, protocol, sample
  value, a word spelled the same); translate everything else.
- `i18n-local/no-untranslated-jsx-text` (`eslint-rules/`, RuleTester controls
  next to it, run by `bun run test:unit`): JSX text children, string literal
  children and `aria-label` / `title` / `alt` / `label` string attributes must
  go through `t()`. The rule's `allow` list in `eslint.config.js` is for exact
  brand, protocol and command strings only.

The rule does not look at `placeholder` (mostly sample values like
`example.com`) or at strings built outside JSX (toast messages, summary lines in
helpers). When touching a form, sweep those by hand: list the string-literal
`placeholder=` attributes and `toast*("…")` calls in the files you changed
(`grep -nE 'placeholder="[^"]*[a-z]{2}|toast(\.[a-z]+)?\("' <files>`) and move
anything a user reads as prose into the locale files.

Terminology already chosen per locale, reuse it: fr "clé d'accès" (passkey),
"contrôle" (check), "astreinte" (on-call), vous; de "Passkey", "Prüfung",
"Bereitschaft", Sie; es "llave de acceso", "comprobación", "guardia", tú.

## Adding New Features

### Adding a Route

Create a file in `src/routes/`:

```typescript
// src/routes/incidents.tsx
import { createFileRoute } from "@tanstack/react-router";

export const Route = createFileRoute("/incidents")({
  component: IncidentsPage,
});

function IncidentsPage() {
  return <div>Incidents</div>;
}
```

### Adding a Component

1. Add to `src/components/shared/` for business logic
2. Add to `src/components/ui/` for reusable primitives
3. Use Tailwind CSS for styling
4. Use Radix UI for accessible interactions

## Integration with Backend

The frontend is embedded in the Go backend:

1. **Build**: `make build-dash0` creates `dist/`
2. **Copy**: `make copy-dash0` copies to `server/internal/app/dash0res/`
3. **Embed**: Backend uses `go:embed dash0res/*`
4. **Serve**: Backend serves at `/d/` with SPA fallback

## Scripts Reference

| Script | Description |
|--------|-------------|
| `dev` | Start dev server on port 5174 |
| `build` | Build for production (with type check) |
| `build:no-check` | Build without type checking |
| `lint` | Run ESLint over the whole app (red on base — see "e2e type-checking and lint" below) |
| `lint:e2e` | Run ESLint over `e2e/` only — this is what CI enforces |
| `typecheck:e2e` | `tsc -p tsconfig.e2e.json` — type-check the Playwright suites |
| `preview` | Preview production build |

## e2e type-checking and lint (spec 2026-09-09-03)

`tsconfig.app.json` is `include: ["src"]` and `tsconfig.node.json` is
`include: ["vite.config.ts"]`, so until this spec **nothing type-checked
`e2e/`** — 159 spec and fixture files compiled nowhere, locally or in CI.

- **`tsconfig.e2e.json`** covers `e2e/`, `playwright.config.ts` and
  `playwright.dev.config.ts`, with `tsconfig.node.json`'s strictness plus
  `lib: ["ES2023", "DOM", "DOM.Iterable"]` (browser callbacks legitimately
  reference `window`/`document`/`navigator`) and `types: ["node"]`.
  It is **deliberately not referenced** from `tsconfig.json`: a type error in a
  Playwright spec must fail CI loudly but must never block the production bundle
  build. CI runs it as its own step (`bun run typecheck:e2e`) in both the `dash0`
  and `status0` jobs.
- **`bun run lint:e2e`** (`eslint e2e`) is the dash0 job's lint step. It is
  scoped on purpose: the unscoped `bun run lint` is red on base with ~39 errors
  and ~440 warnings, effectively all `react-hooks` findings under `src/`. Paying
  that debt down is its own spec — do not relax the config to make `lint` green,
  and do not widen the CI step until the debt is gone.

### The `page.evaluate` guard

Playwright **serializes** the callback passed to `evaluate`, `evaluateHandle`,
`$eval`, `$$eval`, `waitForFunction` and `addInitScript` and runs it **in the
browser**. Anything it closes over lives in the Node process and is not there at
runtime — the page throws `ReferenceError`. `tsc` cannot see this (closing over
an in-scope import is legal TypeScript) and neither can an esquery
`no-restricted-syntax` selector (it cannot do scope analysis).

A mechanical base-path sweep shipped exactly that bug green:

```ts
// WRONG — DASH_BASE is a Node-side import; the page has never heard of it.
await page.evaluate(() => navigator.serviceWorker.getRegistration(`${DASH_BASE}/sw.js`));

// RIGHT — hand the value in as the evaluate argument.
await page.evaluate((base) => navigator.serviceWorker.getRegistration(`${base}/sw.js`), DASH_BASE);
```

`eslint-rules/no-node-scope-in-browser-callback.js` (a local flat-config plugin,
no npm package, duplicated verbatim in `web/status0` — keep the copies in sync)
walks each callback's scope and reports any reference resolving to a binding
outside it. The callback's own parameters and locals, browser globals and
type-only references are all allowed. `e2e/eslint-guard-fixtures.ts` is the
positive control: correctly-written calls that must stay green, so the rule can
never degrade into "reject everything". It is not a `*.spec.ts`, so Playwright
never collects it.
