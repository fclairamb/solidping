# SQL Query Logging with slog

## Overview

Add configurable SQL query logging using Go's standard `slog` package instead of bun's default `bundebug` logger.

## Current State

- **SQLite** (`back/internal/db/sqlite/sqlite.go:88`): Has `bundebug.NewQueryHook(bundebug.WithVerbose(true))` hardcoded, which prints to stdout unconditionally
- **PostgreSQL** (`back/internal/db/postgres/postgres.go`): Has no query logging

## Requirements

1. SQL logging should be **configurable** via config file or environment variable
2. Use **slog** for consistent structured logging across the application
3. Apply to **both** PostgreSQL and SQLite implementations
4. Log at **debug level** by default (only visible when `LOG_LEVEL=debug`)
5. Include useful context: query, duration, error (if any)

## Configuration

### Config Structure

Add to `DatabaseConfig` in `back/internal/config/config.go`:

```go
type DatabaseConfig struct {
    Type    string `koanf:"type"`   // existing
    URL     string `koanf:"url"`    // existing
    Dir     string `koanf:"dir"`    // existing
    LogSQL  bool   `koanf:"logsql"` // NEW: enable SQL query logging
}
```

### Environment Variable

```bash
SP_DB_LOGSQL=true   # Enable SQL logging
LOG_LEVEL=debug     # Required to see debug-level logs
```

### Config File

```yaml
db:
  type: sqlite
  logsql: true
```

## Implementation

### 1. Create slog Query Hook

Create new file `back/internal/db/sloghook/hook.go`:

```go
// Package sloghook provides a bun query hook that logs queries using slog.
package sloghook

import (
    "context"
    "log/slog"
    "time"

    "github.com/uptrace/bun"
)

// QueryHook is a bun query hook that logs SQL queries using slog.
type QueryHook struct {
    // Verbose includes the full query in logs (may contain sensitive data)
    Verbose bool
}

// New creates a new slog query hook.
func New(verbose bool) *QueryHook {
    return &QueryHook{Verbose: verbose}
}

func (h *QueryHook) BeforeQuery(ctx context.Context, event *bun.QueryEvent) context.Context {
    return ctx
}

func (h *QueryHook) AfterQuery(ctx context.Context, event *bun.QueryEvent) {
    duration := time.Since(event.StartTime)

    attrs := []slog.Attr{
        slog.Duration("duration", duration),
        slog.String("operation", event.Operation()),
    }

    if h.Verbose {
        attrs = append(attrs, slog.String("query", event.Query))
    }

    if event.Err != nil {
        attrs = append(attrs, slog.String("error", event.Err.Error()))
        slog.LogAttrs(ctx, slog.LevelWarn, "SQL query failed", attrs...)
        return
    }

    slog.LogAttrs(ctx, slog.LevelDebug, "SQL query", attrs...)
}
```

### 2. Update PostgreSQL Service

In `back/internal/db/postgres/postgres.go`:

```go
// Config holds PostgreSQL connection configuration.
type Config struct {
    DSN         string
    Embedded    bool
    EmbeddedDir string
    Port        uint32
    LogSQL      bool  // NEW
}

// New creates a new PostgreSQL service with an external database.
func New(ctx context.Context, cfg Config) (*Service, error) {
    // ... existing code ...

    bunDB := bun.NewDB(sqldb, pgdialect.New())

    // Add SQL logging hook if enabled
    if cfg.LogSQL {
        bunDB.AddQueryHook(sloghook.New(true))
    }

    return &Service{db: bunDB}, nil
}
```

### 3. Update SQLite Service

In `back/internal/db/sqlite/sqlite.go`:

```go
// Config holds SQLite configuration.
type Config struct {
    DataDir  string
    InMemory bool
    LogSQL   bool  // NEW
}

// New creates a new SQLite service.
func New(ctx context.Context, cfg Config) (*Service, error) {
    // ... existing code ...

    bunDB := bun.NewDB(sqldb, sqlitedialect.New())

    // Add SQL logging hook if enabled (replaces hardcoded bundebug)
    if cfg.LogSQL {
        bunDB.AddQueryHook(sloghook.New(true))
    }

    // REMOVE: bunDB.AddQueryHook(bundebug.NewQueryHook(bundebug.WithVerbose(true)))

    return &Service{
        db:      bunDB,
        dataDir: cfg.DataDir,
    }, nil
}
```

### 4. Update Server Initialization

In `back/internal/app/server.go`, pass `LogSQL` config to DB services:

```go
case "postgres":
    dbService, err = postgres.New(ctx, postgres.Config{
        DSN:      cfg.Database.URL,
        Embedded: false,
        LogSQL:   cfg.Database.LogSQL,  // NEW
    })

case "sqlite":
    dbService, err = sqlite.New(ctx, sqlite.Config{
        DataDir:  cfg.Database.Dir,
        InMemory: false,
        LogSQL:   cfg.Database.LogSQL,  // NEW
    })
```

## Example Output

When `SP_DB_LOGSQL=true` and `LOG_LEVEL=debug`:

```
level=DEBUG msg="SQL query" duration=1.234ms operation=SELECT query="SELECT * FROM checks WHERE deleted_at IS NULL"
level=DEBUG msg="SQL query" duration=0.5ms operation=INSERT query="INSERT INTO results ..."
level=WARN msg="SQL query failed" duration=2.1ms operation=UPDATE error="UNIQUE constraint failed" query="UPDATE ..."
```

## Testing

1. Unit test for `sloghook`:
   - Verify `AfterQuery` logs at correct levels
   - Verify error queries log at WARN level
   - Verify duration is calculated correctly

2. Integration test:
   - Verify queries are logged when `LogSQL=true`
   - Verify no logging when `LogSQL=false`

## Migration Notes

- Remove the hardcoded `bundebug` import and usage from `sqlite.go`
- Default value for `LogSQL` is `false` (opt-in)
- Existing behavior: SQLite was always logging (verbose), PostgreSQL never logged
- New behavior: Both are consistent, only log when explicitly enabled

## Files to Modify

1. `back/internal/config/config.go` - Add `LogSQL` field to `DatabaseConfig`
2. `back/internal/db/sloghook/hook.go` - NEW: Create slog query hook
3. `back/internal/db/postgres/postgres.go` - Add `LogSQL` to Config, apply hook
4. `back/internal/db/sqlite/sqlite.go` - Add `LogSQL` to Config, replace bundebug with sloghook
5. `back/internal/app/server.go` - Pass `LogSQL` config to DB services
