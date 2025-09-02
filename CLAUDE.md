# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Core technologies
- **Backend**: Go 1.24+ (see server/CLAUDE.md for details)
- **Dashboard**: (see web/dash0/CLAUDE.md for details), do not use `web/dash` for current developments
- **Infrastructure**: Docker Compose with PostgreSQL for monitoring data storage
- **Monitoring**: Multi-protocol ping/health checking with distributed worker system

## Common commands

### Infrastructure
- **Start services**: `docker-compose up -d` (PostgreSQL)
- **Build Docker image**: `make docker-build`

### Backend
See server/CLAUDE.md for backend-specific commands

### Dashboard
See web/dash0/CLAUDE.md for dashboard-specific commands

### Database
- **Reset SQLite database**: Delete the `solidping.db` file or use `SP_DB_RESET=true` environment variable to reset on startup

## Development Workflow
If the server is currently running on port 4000, you can just apply code changes,
wait 5s for it to build and then test your changes.

1. Start infrastructure: `docker-compose up -d`
2. Backend: `make dev-backend` for development server
3. Dashboard: `make dev-dash0` for development server
4. Database changes: Add migrations, run `make migrate`

## Common Makefile Targets
- **Build**:
  - `make build` - Build complete application (dash + dash0 + backend)
  - `make build-dash` - Build dash only (using bun)
  - `make build-dash0` - Build dash0 status page only (using bun)
  - `make build-backend` - Build backend only (Go binary)
  - `make docker-build` - Build Docker image
- **Development**:
  - `make dev-backend` - Start backend development server
  - `make dev-dash` - Start dash development server
  - `make dev-dash0` - Start dash0 development server
  - `make dev-test` - Run backend and dash0 in development test mode
- **Testing**:
  - `make test` - Run backend tests
  - `make test-dash` - Run dash tests
- **Linting**:
  - `make lint` - Run all linters (backend + dash)
  - `make lint-back` - Run backend linter (golangci-lint)
  - `make lint-dash` - Run dash linter
  - `make fmt` - Format all code (backend + dash)
- **Other**:
  - `make deps` - Install all dependencies
  - `make migrate` - Run database migrations
  - `make clean` - Remove built binaries and artifacts
  - `make run` - Build and run the application

## Default credentials
- User: `admin@solidping.com`
- Pass: `solidpass`
- Org: `default`

## Test mode credentials (SP_RUNMODE=test)
- User: `test@test.com`
- Pass: `test`
- Org: `test`

## REST API choices
- Never return an array directly. It should always be inside another element like `data`.
- Always use $uid in paths
- Use `q` for search parameters
- Use `PATCH` for all APIs allowing updates
- Use camelCase consistently for both JSON properties and query parameters (e.g., `checkUid` in JSON and `?checkUid=abc` in URLs)
- When using query parameters that can contain multiple values, use them in their singular form, for example `checkUid` and not `checkUids`. If there are multiple values, separate them with `,`.

### API Endpoints
- GET /api/mgmt/version - Get the current version
- GET /api/mgmt/health - Health check endpoint
- POST /api/v1/auth/login - User/password authentication (org optional in body)
- POST /api/v1/auth/logout - Logout current session
- POST /api/v1/auth/refresh - Refresh access token
- GET /api/v1/auth/me - Get current user info
- POST /api/v1/auth/switch-org - Switch organization context
- GET /api/v1/auth/tokens - List all user's tokens across orgs
- DELETE /api/v1/auth/tokens/$tokenUid - Revoke a token
- GET /api/v1/orgs/$org/tokens - List user's tokens for an org
- POST /api/v1/orgs/$org/tokens - Create a Personal Access Token for an org

### Errors
All errors should return:
- `title`: The description as it could be presented to the user
- `code`: As it can be handled by the client code
- `detail`: A more detailed explanation

**Standard Error Codes** (defined in `base.HandlerBase`):
- `INTERNAL_ERROR` - Unexpected server error
- `VALIDATION_ERROR` - Input validation failed
- `NOT_FOUND` - Resource not found
- `UNAUTHORIZED` - Authentication required
- `FORBIDDEN` - Permission denied
- `CONFLICT` - Resource conflict (duplicate, etc.)
- `ORGANIZATION_NOT_FOUND` - Organization does not exist
- `USER_NOT_FOUND` - User does not exist
- `CHECK_NOT_FOUND` - Check does not exist

### API Testing
```bash
# Login and get JWT token (org is optional in body)
TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' -d '{"org":"default","email":"admin@solidping.com","password":"solidpass"}' 'http://localhost:4000/api/v1/auth/login' | jq -r '.accessToken')

# With JWT token
curl -s -H "Authorization: Bearer $TOKEN" 'http://localhost:4000/api/v1/orgs/default/checks'
```

### CLI Client
The CLI client is integrated into the main `solidping` binary as a `client` subcommand:
```bash
# Using the integrated client command
./solidping client checks list
./solidping client auth login
./solidping client results list

# Or build the standalone sp binary for convenience (shorter commands)
make build-cli
./bin/sp checks list
```

## Frontend Error Handling
See `docs/frontend_error_conventions.md` for full details.

**Key rules:**
- **401**: Redirect to login with `?returnTo={currentPath}` to preserve navigation
- **403**: Show "Permission Denied" - **never redirect to login** (causes loops)
- **500**: Show user-friendly error with retry button
- **502/503/504**: Auto-retry with exponential backoff (transient errors)

## Specs
- All spec files must be prefixed with a date: `YYYY-MM-DD-` (e.g., `2026-02-21-adaptive-incident-resolution.md`)
- `specs/done/` contains completed specs in `YYYY/MM/` subdirectories (e.g., `specs/done/2025/12/2025-12-07-auth.md`)
- `specs/backlog/` contains specs planned for future implementation
- `specs/cancelled/` contains abandoned specs (same `YYYY/MM/` structure)

## Testing
- **Backend**: Table-driven tests with testcontainers for integration tests (see server/CLAUDE.md)
- **Dash0**: Playwright for E2E testing (see web/dash0/CLAUDE.md)
- **Both**: Comprehensive test coverage expected for new features
