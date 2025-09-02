# Dash0 - Public Status Dashboard

A React-based public status page for displaying SolidPing monitoring status. This is a read-only dashboard meant to be embedded or shared publicly.

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
│   │   ├── shared/           # Business logic components
│   │   │   ├── status-dashboard.tsx  # Main dashboard
│   │   │   └── status-timeline.tsx   # Timeline visualization
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

The dashboard fetches data from these public-facing endpoints:

- `GET /api/v1/orgs/{org}/checks` - List all checks with status
- `GET /api/v1/orgs/{org}/results?checkUid={uid}&limit=48` - Recent results for timeline

## Features

### Status Dashboard
- Overall system status (ok/warning/error)
- Service cards with current status
- 48-point status timeline per service
- Auto-refresh every 30 seconds (checks) / 60 seconds (results)

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
