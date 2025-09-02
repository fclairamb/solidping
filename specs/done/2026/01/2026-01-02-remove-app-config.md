# Spec: Remove app.Config and Consolidate on config.Config

## Problem Statement

The codebase has two redundant configuration structures:
1. **`config.Config`** (`internal/config/config.go:33-41`) - Primary config loaded from files/env via koanf
2. **`app.Config`** (`internal/app/server.go:80-92`) - Simplified version used by Server

This creates:
- **Conversion overhead**: `main.go` converts `config.Config` → `app.Config`, then `server.go` converts it back to `config.Config` for workers
- **Duplicate types**: Both packages define their own `DatabaseConfig` with identical fields
- **Maintenance burden**: Changes require updating both structs
- **Inconsistency**: `app.Config.Listen` is top-level, but `config.Config.Server.Listen` is nested

## Current Architecture

### config.Config (The Keeper)
```go
// internal/config/config.go:33-41
type Config struct {
    Server   ServerConfig   `koanf:"server"`   // Contains Listen, JobWorker, CheckWorker, ShutdownTimeout, Redirects
    Database DatabaseConfig `koanf:"db"`       // Type, URL, Dir
    Auth     AuthConfig     `koanf:"auth"`
    Email    EmailConfig    `koanf:"email"`
    Slack    SlackConfig    `koanf:"slack"`
    RunMode  string         `koanf:"runmode"`
    LogLevel slog.Level     `koanf:"-"`
}
```

### app.Config (To Be Removed)
```go
// internal/app/server.go:80-92
type Config struct {
    Listen    string                    // Flattened from Server.Listen
    Database  DatabaseConfig            // app.DatabaseConfig (separate type!)
    Redirects []config.RedirectRule     // Flattened from Server.Redirects
    Auth      config.AuthConfig
    Email     config.EmailConfig
    RunMode   string
    Server    struct {                  // Partial copy
        JobWorker       config.JobWorkerConfig
        CheckWorker     config.CheckWorkerConfig
        ShutdownTimeout time.Duration
    }
}

// internal/app/server.go:95-99
type DatabaseConfig struct {
    Type string
    URL  string
    Dir  string
}
```

### Current Flow
```
config.Load() → config.Config
       ↓
main.go converts → app.Config (wasteful)
       ↓
app.NewServer(appCfg)
       ↓
Server stores app.Config
       ↓
Workers: app.Config → config.Config (wasteful round-trip!)
```

## Target Architecture

```
config.Load() → *config.Config
       ↓
app.NewServer(cfg *config.Config) directly
       ↓
Server stores *config.Config
       ↓
Workers receive *config.Config directly (no conversion)
```

## Implementation Plan

### Step 1: Update Server struct to use `*config.Config`

**File**: `back/internal/app/server.go`

Change:
```go
type Server struct {
    // ...
    config      Config  // app.Config
    // ...
}
```
To:
```go
type Server struct {
    // ...
    config      *config.Config
    // ...
}
```

### Step 2: Update `NewServer` signature

**File**: `back/internal/app/server.go:104`

Change:
```go
func NewServer(ctx context.Context, cfg Config) (*Server, error) {
```
To:
```go
func NewServer(ctx context.Context, cfg *config.Config) (*Server, error) {
```

### Step 3: Update all `s.config.*` references in server.go

Update all field accesses to use the new structure:

| Old Access | New Access |
|------------|------------|
| `s.config.Listen` | `s.config.Server.Listen` |
| `s.config.Database.Type` | `s.config.Database.Type` (same) |
| `s.config.Database.URL` | `s.config.Database.URL` (same) |
| `s.config.Database.Dir` | `s.config.Database.Dir` (same) |
| `s.config.Redirects` | `s.config.Server.Redirects` |
| `s.config.Auth` | `s.config.Auth` (same) |
| `s.config.Email` | `s.config.Email` (same) |
| `s.config.RunMode` | `s.config.RunMode` (same) |
| `s.config.Server.JobWorker` | `s.config.Server.JobWorker` (same) |
| `s.config.Server.CheckWorker` | `s.config.Server.CheckWorker` (same) |
| `s.config.Server.ShutdownTimeout` | `s.config.Server.ShutdownTimeout` (same) |

### Step 4: Remove conversion in main.go

**File**: `back/main.go:87-106`

Remove:
```go
// Convert config to app.Config
appCfg := app.Config{
    Listen: cfg.Server.Listen,
    Database: app.DatabaseConfig{
        Type: cfg.Database.Type,
        URL:  cfg.Database.URL,
        Dir:  cfg.Database.Dir,
    },
    Redirects: cfg.Server.Redirects,
    RunMode:   cfg.RunMode,
}
appCfg.Server.JobWorker = cfg.Server.JobWorker
appCfg.Server.CheckWorker = cfg.Server.CheckWorker
appCfg.Server.ShutdownTimeout = cfg.Server.ShutdownTimeout
appCfg.Auth = cfg.Auth
appCfg.Email = cfg.Email

server, err := app.NewServer(ctx, appCfg)
```

Replace with:
```go
server, err := app.NewServer(ctx, cfg)
```

### Step 5: Simplify setupRoutes handler initialization

**File**: `back/internal/app/server.go:215-217`

Remove partial config creation:
```go
cfg := &config.Config{Auth: s.config.Auth}
authHandler := auth.NewHandler(s.authService, cfg)
authMiddleware := middleware.NewAuthMiddleware(s.authService, s.dbService, cfg)
```

Replace with direct use:
```go
authHandler := auth.NewHandler(s.authService, s.config)
authMiddleware := middleware.NewAuthMiddleware(s.authService, s.dbService, s.config)
```

And update all other handler initializations to use `s.config` directly instead of `cfg`.

### Step 6: Simplify worker initialization

**File**: `back/internal/app/server.go:620-631` (startJobWorker)

Remove the conversion block:
```go
appConfig := &config.Config{
    Server: config.ServerConfig{
        Listen:    s.config.Listen,
        JobWorker: s.config.Server.JobWorker,
    },
    Database: config.DatabaseConfig{
        Type: s.config.Database.Type,
        URL:  s.config.Database.URL,
        Dir:  s.config.Database.Dir,
    },
}
```

Replace with:
```go
// s.config is already *config.Config, pass directly to workers
```

**File**: `back/internal/app/server.go:660-671` (startCheckWorker)

Same simplification - remove conversion and pass `s.config` directly.

### Step 7: Update test helper

**File**: `back/test/integration/testhelper.go:41-53`

Change:
```go
cfg := app.Config{
    Listen: ":0",
    Database: app.DatabaseConfig{
        Type: "sqlite-memory",
    },
    Auth: config.AuthConfig{...},
}
```

To:
```go
cfg := &config.Config{
    Server: config.ServerConfig{
        Listen: ":0",
    },
    Database: config.DatabaseConfig{
        Type: "sqlite-memory",
    },
    Auth: config.AuthConfig{...},
}
```

### Step 8: Remove app.Config and app.DatabaseConfig types

**File**: `back/internal/app/server.go`

Delete lines 80-99:
```go
// Config holds the server configuration.
type Config struct {
    // ... entire struct
}

// DatabaseConfig holds the database configuration.
type DatabaseConfig struct {
    // ... entire struct
}
```

## Files to Modify

| File | Changes |
|------|---------|
| `back/internal/app/server.go` | Remove Config/DatabaseConfig types, update Server struct, update all s.config.* accesses |
| `back/main.go` | Remove conversion block, pass cfg directly |
| `back/test/integration/testhelper.go` | Update test config creation |

## Verification Steps

1. `make build-backend` - Ensure compilation succeeds
2. `make lint-back` - Ensure no linting errors
3. `make test` - Run backend tests
4. Manual test: `make dev-backend` + login flow

## Benefits

1. **Eliminates redundancy** - Single source of truth for configuration
2. **Removes conversion overhead** - No more config → app.Config → config round-trips
3. **Simpler mental model** - One config type to understand
4. **Less code** - ~40 lines removed
5. **Better type safety** - No risk of conversion bugs
