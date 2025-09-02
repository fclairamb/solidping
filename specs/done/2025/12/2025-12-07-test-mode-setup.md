# Test Mode Setup

**Type**: chore
**Date**: 2025-12-07
**Status**: Ready for Implementation

## Quick Reference

**Key Actions**:
1. ✅ Add `SP_RUN_MODE`, `SP_DB_TYPE`, `SP_DB_URL` environment variables
2. ✅ Support 4 database types: `postgres`, `postgres-embedded`, `sqlite`, `sqlite-memory`
3. ✅ Create test data via startup job when `SP_RUN_MODE=test`
4. 🗑️ **DELETE all demo_data migration files (4 files - REQUIRED)**
5. ✅ Auto-create default organization on fresh database

**Critical Files to Delete**:
- `back/internal/db/postgres/migrations/20251207000002_demo_data.up.sql`
- `back/internal/db/postgres/migrations/20251207000002_demo_data.down.sql`
- `back/internal/db/sqlite/migrations/20251207000002_demo_data.up.sql`
- `back/internal/db/sqlite/migrations/20251207000002_demo_data.down.sql`

## Idea

Add a test mode triggered by `SP_RUN_MODE=test` that creates deterministic test data, remove the existing demo_data migration, and ensure a default organization is created on startup if none exists.

## Description

This change introduces a test mode for easier testing and development, while also ensuring the application always has at least one organization configured on startup.

### Requirements

1. **Database Type Configuration (`SP_DB_TYPE`)**:
   - `SP_DB_TYPE=postgres`: Regular PostgreSQL database (requires `SP_DB_URL`)
   - `SP_DB_TYPE=postgres-embedded`: Embedded PostgreSQL (mostly useful for test)
   - `SP_DB_TYPE=sqlite`: Regular SQLite database file
   - `SP_DB_TYPE=sqlite-memory`: In-memory SQLite database (`:memory:`)
   - When `SP_RUN_MODE=test` and no `SP_DB_TYPE` is specified, default to `sqlite-memory`

2. **Test Mode (`SP_RUN_MODE=test`)**:
   - Create a test organization with UUID `00000000-0000-0000-0000-000000000001` and slug `test`
   - Create a test user `test@test.com` with UUID `00000000-0000-0000-0000-000000000002` and password `test`
   - Create a PAT token with value `test` and UUID `00000000-0000-0000-0000-000000000003`
   - Use deterministic UUIDs for reproducible testing
   - Only activate when `SP_RUN_MODE=test` environment variable is set

3. **Remove Demo Data Migration** (REQUIRED):
   - **MUST DELETE** all existing `demo_data` migration files (see "Migrations to Delete" section)
   - Verify database schema remains valid after removal
   - Test data will instead be created via startup job when `SP_RUN_MODE=test`

4. **Default Organization on Startup**:
   - In the startup job, check if any organizations exist
   - If no organizations exist, create a "default" organization with "email-pass" authentication
   - This ensures the application is always usable after a fresh database setup

## Acceptance Criteria

- [ ] When `SP_DB_TYPE=postgres`, regular PostgreSQL with `SP_DB_URL` is used
- [ ] When `SP_DB_TYPE=postgres-embedded`, embedded PostgreSQL is used
- [ ] When `SP_DB_TYPE=sqlite`, regular SQLite database file is used
- [ ] When `SP_DB_TYPE=sqlite-memory`, in-memory SQLite database is used
- [ ] When `SP_RUN_MODE=test` and no `SP_DB_TYPE` is specified, defaults to `sqlite-memory`
- [ ] When `SP_RUN_MODE=test`, test data is created with the specified UUIDs
- [ ] Test user `test@test.com` can authenticate with password `test`
- [ ] PAT token `test` is valid and can be used for API authentication
- [ ] **CRITICAL**: All demo_data migration files are deleted (4 files total - 2 PostgreSQL, 2 SQLite)
- [ ] Verify no references to demo_data migrations remain in the codebase
- [ ] On fresh database (non-test mode), a "default" organization is automatically created
- [ ] Test mode does not interfere with normal operation when not enabled
- [ ] All existing tests pass
- [ ] Linting passes
- [ ] Build succeeds

## Technical Considerations

- Test data should be created as part of the startup jobs system
- Need to handle both PostgreSQL and SQLite migrations
- Embedded PostgreSQL will use `github.com/fergusstrange/embedded-postgres` library
- Embedded PostgreSQL should use PostgreSQL version 18.1
- Embedded PostgreSQL data should be stored in a temporary or test directory
- Should check for existing test data to avoid duplicates
- Default organization creation should be idempotent
- Consider security: test mode should only be used in development/testing environments
- Embedded PostgreSQL lifecycle (start/stop) needs to be managed by the server

## Implementation Notes

### Architecture Decisions

1. **Run Mode Detection**: Use `SP_RUN_MODE=test` environment variable for test mode
2. **Database Type Selection**: Use `SP_DB_TYPE` environment variable:
   - `postgres`: Regular PostgreSQL (requires `SP_DB_URL`)
   - `postgres-embedded`: Embedded PostgreSQL using library from `experiments/embedded-pg/`
   - `sqlite`: Regular SQLite database file
   - `sqlite-memory`: In-memory SQLite (`:memory:`)
   - Default when `SP_RUN_MODE=test`: `sqlite-memory`
3. **Embedded PostgreSQL**: Managed by server lifecycle (start on server start, stop on shutdown)
4. **Test Data Creation**: Use startup job to create test data instead of migrations
5. **Default Org Creation**: Also handled via startup job for consistency

### Files to Modify

#### Configuration Layer
- **`back/internal/config/config.go`**:
  - Add `RunMode string` field to Config struct (parse from `SP_RUN_MODE`)
  - Add `DBType string` field to Config struct (parse from `SP_DB_TYPE`)
  - Add `DBURL string` field to Config struct (parse from `SP_DB_URL`)
  - When `RunMode=test` and no `DBType` is specified, default to `sqlite-memory`
  - Validate that `DBType=postgres` requires `DBURL` to be set
  - Support four database types:
    - `postgres`: Regular PostgreSQL (requires `DBURL`)
    - `postgres-embedded`: Embedded PostgreSQL
    - `sqlite`: Regular SQLite file
    - `sqlite-memory`: In-memory SQLite

- **`back/main.go`**:
  - Pass run mode and database configuration to app
  - For `postgres-embedded`, initialize and manage the embedded postgres instance lifecycle

#### Database Layer
- **`back/internal/db/service.go`**:
  - Add AuthProvider CRUD operations to db.Service interface:
    - `CreateAuthProvider(ctx, *models.AuthProvider) error`
    - `GetAuthProviderByType(ctx, orgUID, type) (*models.AuthProvider, error)`
    - `ListAuthProviders(ctx, orgUID) ([]*models.AuthProvider, error)`

- **`back/internal/db/postgres/postgres.go`**:
  - Implement new AuthProvider methods

- **`back/internal/db/sqlite/sqlite.go`**:
  - Implement new AuthProvider methods

#### Embedded PostgreSQL Support
- **`back/internal/db/embedded/embedded.go`** (new file):
  - Create wrapper for embedded PostgreSQL using `github.com/fergusstrange/embedded-postgres`
  - Configure to use PostgreSQL version 18.1
  - Start() method to initialize and start embedded postgres
  - Stop() method to gracefully shutdown
  - GetConnectionString() to return the connection DSN
  - Use temporary directory for postgres data in test mode

- **`back/internal/app/server.go`**:
  - Add embedded postgres instance field (optional, only when `DBType=postgres-embedded`)
  - Initialize embedded postgres before creating database service when needed
  - Ensure embedded postgres is stopped during server shutdown

#### Startup Job
- **`back/internal/jobs/jobtypes/job_startup.go`**:
  - Add logic to check `SP_RUN_MODE` environment variable
  - If `SP_RUN_MODE=test`:
    - Create test organization with deterministic UUID
    - Create email-password auth provider
    - Create test user with deterministic UUID
    - Create test PAT token with deterministic UUID
  - If not test mode and no orgs exist:
    - Create "default" organization
    - Create email-password auth provider

#### Dependencies
- **`back/go.mod`**:
  - Add `github.com/fergusstrange/embedded-postgres` dependency (already in experiments/embedded-pg)

#### Migrations to Delete (REQUIRED STEP)

**IMPORTANT**: These files MUST be completely deleted from the codebase:

1. **PostgreSQL migrations**:
   - `back/internal/db/postgres/migrations/20251207000002_demo_data.up.sql`
   - `back/internal/db/postgres/migrations/20251207000002_demo_data.down.sql`

2. **SQLite migrations**:
   - `back/internal/db/sqlite/migrations/20251207000002_demo_data.up.sql`
   - `back/internal/db/sqlite/migrations/20251207000002_demo_data.down.sql`

**Verification after deletion**:
- Run `git grep -i "demo_data"` to ensure no remaining references
- Run `git grep -i "20251207000002"` to ensure migration number is not referenced elsewhere
- Ensure migrations still apply cleanly on a fresh database

### Database Changes

No migration changes needed - we're removing migrations, not adding them.

### API Changes

None - this is internal implementation only.

### Testing Strategy

1. **Manual Testing**:
   - **SQLite Memory Test Mode**:
     - Set `SP_RUN_MODE=test` (defaults to `sqlite-memory`)
     - Verify in-memory SQLite is used
     - Verify test data is created with correct UUIDs
     - Verify login with `test@test.com` / `test` works
     - Verify PAT token `test` works
   - **Embedded PostgreSQL Test Mode**:
     - Set `SP_RUN_MODE=test` and `SP_DB_TYPE=postgres-embedded`
     - Verify embedded PostgreSQL starts and runs
     - Verify test data is created with correct UUIDs
     - Verify login with `test@test.com` / `test` works
     - Verify PAT token `test` works
     - Verify embedded postgres shuts down cleanly
   - **Regular PostgreSQL**:
     - Set `SP_DB_TYPE=postgres` and `SP_DB_URL=postgresql://...`
     - Verify connection to external PostgreSQL works
   - **Regular SQLite**:
     - Set `SP_DB_TYPE=sqlite`
     - Verify SQLite file database is used
   - **Normal Mode (no test data)**:
     - Without `SP_RUN_MODE=test`, verify default org is created on fresh DB

2. **Automated Testing**:
   - Existing tests should continue to pass
   - Consider adding integration test for startup job behavior
   - Test all database types (postgres, postgres-embedded, sqlite, sqlite-memory)
   - Test test mode with different database types

### Risk Assessment

**Low Risk**:
- Adding new environment variables (`SP_RUN_MODE`, `SP_DB_TYPE`, `SP_DB_URL`) is non-breaking
- Removing demo_data migration won't affect existing databases (migrations only run once)
- Startup job logic is additive and conditional
- Test mode (`SP_RUN_MODE=test`) only activates when explicitly enabled
- Database types provide clear separation of concerns
- Embedded postgres library is well-tested and used in experiments

**Mitigation**:
- Test all database types and run modes thoroughly
- Ensure default org creation is idempotent (check before creating)
- Verify existing auth flows still work after AuthProvider methods added
- Ensure embedded postgres lifecycle is properly managed (cleanup on shutdown)
- Add proper error handling for embedded postgres startup failures
- Validate that `SP_DB_TYPE=postgres` requires `SP_DB_URL` at startup

### Implementation Order

1. Add AuthProvider methods to database layer (foundation)
2. Update config to parse `SP_RUN_MODE`, `SP_DB_TYPE`, and `SP_DB_URL`
3. Create embedded postgres wrapper (`back/internal/db/embedded/`)
4. Update main.go and server.go to support all database types
5. Implement startup job logic (test data creation and default org)
6. **DELETE all demo_data migration files** (see "Migrations to Delete" section):
   - Delete 4 files total (2 PostgreSQL, 2 SQLite)
   - Verify with `git grep -i "demo_data"` and `git grep -i "20251207000002"`
   - Test that migrations still work on fresh database
7. Test thoroughly in all modes:
   - `SP_RUN_MODE=test` (defaults to sqlite-memory)
   - `SP_RUN_MODE=test` with `SP_DB_TYPE=postgres-embedded`
   - `SP_DB_TYPE=postgres` with `SP_DB_URL`
   - `SP_DB_TYPE=sqlite`
   - Normal mode (no SP_RUN_MODE)
8. Update documentation/CLAUDE.md if needed

## Pre-Implementation Checklist

Before starting implementation, verify:
- [ ] You understand the 4 database types and their configuration
- [ ] You have identified all 4 demo_data migration files to delete
- [ ] You understand the startup job needs to handle both test mode AND default org creation
- [ ] You have reviewed the embedded-postgres code in `experiments/embedded-pg/`
- [ ] You understand test data must use deterministic UUIDs

## Post-Implementation Verification

After implementation is complete:
- [ ] Run `git status` to confirm 4 demo_data migration files are deleted
- [ ] Run `git grep -i "demo_data"` - should return no results
- [ ] Run `git grep -i "20251207000002"` - should return no results
- [ ] Test with `SP_RUN_MODE=test` - verify test data is created
- [ ] Test login with `test@test.com` / `test` works
- [ ] Test PAT token `test` works for API calls
- [ ] Test fresh database without test mode creates default org
- [ ] Run `make lint` - should pass
- [ ] Run `make test` - all tests should pass
- [ ] Run `make build` - should succeed
