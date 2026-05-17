# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Core technologies
- **Backend**: Go 1.24+ (see `server/CLAUDE.md` for details)
- **Dashboard**: React + TanStack Router (see `web/dash0/CLAUDE.md` for details) — do not use `web/dash` for current development
- **Infrastructure**: Docker Compose with PostgreSQL for monitoring data storage
- **Monitoring**: Multi-protocol ping/health checking with distributed worker system

## Development workflow
If the server is running on port 4000, apply code changes directly — `make dev` / `make dev-test` hot-reloads both backend and frontend.

1. Start infrastructure: `docker-compose up -d`
2. Run everything: `make dev` (backend + dash0 + status0 with hot reload)
3. Test mode: `make dev-test` (same but with `SP_RUNMODE=test`)
4. Database changes: add migrations, then `make migrate`

### Key Makefile targets
| Target | Purpose |
|---|---|
| `make build` | Build everything |
| `make dev` | Hot-reload backend + dash0 + status0 |
| `make dev-test` | Same, with `SP_RUNMODE=test` |
| `make test` | Run backend tests |
| `make test-dash` | Run dash0 Playwright tests |
| `make lint` | Lint backend + dash |
| `make fmt` | Format all code |
| `make migrate` | Run database migrations |
| `make bench-checks` | Benchmark check throughput (SQLite + Postgres) |

## Default credentials

| Mode | Email | Password | Org |
|---|---|---|---|
| Normal | `admin@solidping.com` | `solidpass` | `default` |
| Test (`SP_RUNMODE=test`) | `test@test.com` | `test` | `test` |

## Frontend UI conventions

> **Before writing or modifying any UI**, check the live design reference at
> `http://localhost:4000/dash0/orgs/default/design-reference` — source:
> [`web/dash0/src/routes/orgs/$org/design-reference.tsx`](web/dash0/src/routes/orgs/$org/design-reference.tsx).
>
> It renders every shipped primitive (buttons, alerts, dialogs, tables, forms, name+slug pairs…) with the exact import line alongside it. **Reuse those components and patterns** — don't reach for a raw Radix primitive or a custom implementation if the design reference already ships what you need.

Additional frontend rules:
- **401** → redirect to login with `?returnTo={currentPath}`; **403** → show "Permission Denied", never redirect (causes loops). See `docs/conventions/frontend-errors.md`.
- Editing always navigates to a dedicated route (`/<resource>/new`, `/<resource>/$id`) — never in a modal dialog.
- Row actions: prefer two ghost icon buttons (`Pencil` / `Trash2`) over a `MoreVertical` menu.

## REST API conventions
- Wrap list responses in `{ "data": [...] }`, never return a bare array.
- Use `$uid` in URL paths (not `$id`).
- Use `PATCH` for updates, `q` for search, `limit` for page-size.
- camelCase for all JSON properties and query parameters.
- Multi-value query params use the singular form, comma-separated (e.g. `?checkUid=a,b`).
- Full endpoint list: `docs/api-specification.md`.

### Error shape
```json
{ "title": "Human message", "code": "MACHINE_CODE", "detail": "More detail" }
```
Key codes: `INTERNAL_ERROR`, `VALIDATION_ERROR`, `NOT_FOUND`, `UNAUTHORIZED`, `FORBIDDEN`, `CONFLICT`. See `server/internal/handlers/base/` for the full list.

### Quick API test
```bash
TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"org":"default","email":"admin@solidping.com","password":"solidpass"}' \
  'http://localhost:4000/api/v1/auth/login' | jq -r '.accessToken')
curl -s -H "Authorization: Bearer $TOKEN" 'http://localhost:4000/api/v1/orgs/default/checks'
```

## Testing
- **Backend**: table-driven tests + testcontainers for integration (see `server/CLAUDE.md`)
- **Dash0**: Playwright E2E in `web/dash0/e2e/` (see `web/dash0/CLAUDE.md`)
- Comprehensive coverage expected for new features

## Specs
- Filename format: `YYYY-MM-DD-NN-title.md` (`NN` unique per day across `specs/todos/` and `specs/done/YYYY/MM/`)
- Active: `specs/todos/`, Done: `specs/done/YYYY/MM/`, Backlog: `specs/backlog/`, Cancelled: `specs/cancelled/`
