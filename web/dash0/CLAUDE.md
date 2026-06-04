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
```

### Development with Backend

For hot reload development, use the redirect proxy:

```bash
# Terminal 1: Start dash0 dev server
cd web/dash0 && bun run dev

# Terminal 2: Start backend with redirect
SP_REDIRECTS="/dash0:localhost:5174/dash0" make dev-backend

# Or use air for Go hot reload
cd /path/to/solidping && air
```

Access at `http://localhost:4000/dash0/`

## Configuration

### Base URL

The app is served at `/dash0/` by default. Override with `VITE_BASE_URL`:

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
- 4 KPI tiles: monitored checks, currently down, active incidents, 24h availability
- Two-column body: Needs attention + Active incidents
- Recent activity feed (last 8 events)
- Per-card error boundaries — one failed query does not blank the page
- Polls at 30s (checks/incidents) and 60s (results/events)

### Public-side status (handled elsewhere)
The subscriber-facing public status page lives in `web/status0`. dash0 only
renders the operator UI — when working on subscriber-facing UX, switch repos.

### Theming
- Light/dark mode support via CSS variables
- Blue-based color scheme for monitoring context
- Status colors: green (ok), yellow (warning), red (error)

## Design Reference

Before building or modifying any UI, consult the live design reference at
`http://localhost:4000/dash0/orgs/default/design-reference` (source:
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
4. **Serve**: Backend serves at `/dash0/` with SPA fallback

## Scripts Reference

| Script | Description |
|--------|-------------|
| `dev` | Start dev server on port 5174 |
| `build` | Build for production (with type check) |
| `build:no-check` | Build without type checking |
| `lint` | Run ESLint |
| `preview` | Preview production build |
