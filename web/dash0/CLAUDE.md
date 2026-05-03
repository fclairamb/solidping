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
