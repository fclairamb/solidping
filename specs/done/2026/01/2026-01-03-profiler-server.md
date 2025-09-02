# Profiler Server

## Overview

Implement an optional Go pprof profiler server that can be enabled via configuration. The profiler server runs on a separate port from the main application server to provide performance debugging capabilities without exposing profiling endpoints on the public API.

## Motivation

- **Performance debugging**: Enable CPU, memory, and goroutine profiling during development or production troubleshooting
- **Separation of concerns**: Run profiler on a separate port to avoid accidental exposure on public endpoints
- **Zero overhead when disabled**: No performance impact when profiling is not enabled

## Configuration

### Config File

Add a new `profiler` section to `config.yml`:

```yaml
profiler:
  enabled: false          # Enable/disable the profiler server (default: false)
  listen: "localhost:6060" # Listen address (default: localhost:6060)
```

### Environment Variables

Following the existing `SP_` prefix pattern:
- `SP_PROFILER_ENABLED=true` - Enable the profiler server
- `SP_PROFILER_LISTEN=:6060` - Set the listen address

### Default Behavior

- **Disabled by default**: The profiler must be explicitly enabled
- **Localhost binding**: Default to `localhost:6060` to prevent accidental external exposure
- **No authentication**: Since it binds to localhost by default, no auth is required (standard pprof behavior)

## Implementation

### Config Structure

Add `ProfilerConfig` to `back/internal/config/config.go`:

```go
// ProfilerConfig contains pprof profiler server configuration.
type ProfilerConfig struct {
    Enabled bool   `koanf:"enabled"` // Enable the profiler server
    Listen  string `koanf:"listen"`  // Listen address (e.g., "localhost:6060")
}
```

Add `Profiler` field to the existing `Config` struct (after `Node`):

```go
type Config struct {
    Server    ServerConfig         `koanf:"server"`
    Database  DatabaseConfig       `koanf:"db"`
    Auth      AuthConfig           `koanf:"auth"`
    Email     EmailConfig          `koanf:"email"`
    Slack     SlackConfig          `koanf:"slack"`
    Google    GoogleOAuthConfig    `koanf:"google"`
    GitHub    GitHubOAuthConfig    `koanf:"github"`
    Microsoft MicrosoftOAuthConfig `koanf:"microsoft"`
    GitLab    GitLabOAuthConfig    `koanf:"gitlab"`
    Node      NodeConfig           `koanf:"node"`
    Profiler  ProfilerConfig       `koanf:"profiler"`
    RunMode   string               `koanf:"runmode"`
    LogLevel  slog.Level           `koanf:"-"`
}
```

Add defaults in `Load()` function (inside the `defaults` struct):

```go
Profiler: ProfilerConfig{
    Enabled: false,
    Listen:  "localhost:6060",
},
```

### Profiler Server

Create `back/internal/profiler/profiler.go`:

```go
package profiler

import (
    "context"
    "errors"
    "log/slog"
    "net/http"
    "net/http/pprof"
    "time"

    "github.com/fclairamb/solidping/back/internal/config"
)

// Server is an optional pprof profiler server.
type Server struct {
    config *config.ProfilerConfig
    srv    *http.Server
}

// New creates a new profiler server.
func New(cfg *config.ProfilerConfig) *Server {
    return &Server{config: cfg}
}

// Start starts the profiler server if enabled.
// Returns immediately if profiler is disabled.
func (s *Server) Start(ctx context.Context) error {
    if !s.config.Enabled {
        slog.InfoContext(ctx, "Profiler server disabled")
        return nil
    }

    mux := http.NewServeMux()

    // Register pprof handlers
    mux.HandleFunc("/debug/pprof/", pprof.Index)
    mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
    mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
    mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
    mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

    // Health check for the profiler server
    mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte("ok"))
    })

    s.srv = &http.Server{
        Addr:              s.config.Listen,
        Handler:           mux,
        ReadHeaderTimeout: 10 * time.Second,
    }

    slog.InfoContext(ctx, "Starting profiler server", "listen", s.config.Listen)

    go func() {
        if err := s.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
            slog.ErrorContext(ctx, "Profiler server error", "error", err)
        }
    }()

    return nil
}

// Shutdown gracefully shuts down the profiler server.
func (s *Server) Shutdown(ctx context.Context) error {
    if s.srv == nil {
        return nil
    }
    return s.srv.Shutdown(ctx)
}
```

### Integration with App Server

Update `back/internal/app/server.go`:

Add `profilerSrv` field to the existing `Server` struct:

```go
type Server struct {
    dbService   db.Service
    jobSvc      jobsvc.Service
    services    *services.Registry
    router      *bunrouter.Router
    config      *config.Config
    authService *auth.Service
    mcpHandler  *mcp.Handler
    cancelCtx   context.CancelFunc
    workersWg   sync.WaitGroup
    profilerSrv *profiler.Server
}
```

Initialize profiler in `NewServer()` (after existing field assignments):

```go
server.profilerSrv = profiler.New(&cfg.Profiler)
```

Start profiler at the beginning of `Start()` (before worker/HTTP server startup):

```go
// Start profiler server (no-op if disabled)
if err := s.profilerSrv.Start(ctx); err != nil {
    return fmt.Errorf("failed to start profiler server: %w", err)
}
```

Shutdown profiler in `Close()` (before closing database):

```go
// Shutdown profiler
shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()
if err := s.profilerSrv.Shutdown(shutdownCtx); err != nil {
    slog.Error("Error shutting down profiler", "error", err)
}
```

## Usage

### Enable Profiler

```bash
# Via environment variable
SP_PROFILER_ENABLED=true ./solidping serve

# Or in config.yml
profiler:
  enabled: true
  listen: "localhost:6060"
```

### Access Profiler

```bash
# View available profiles
open http://localhost:6060/debug/pprof/

# CPU profile (30 seconds by default)
go tool pprof http://localhost:6060/debug/pprof/profile

# Memory profile
go tool pprof http://localhost:6060/debug/pprof/heap

# Goroutine dump
curl http://localhost:6060/debug/pprof/goroutine?debug=2

# Trace (5 seconds)
curl -o trace.out http://localhost:6060/debug/pprof/trace?seconds=5
go tool trace trace.out
```

### Available Endpoints

| Endpoint | Description |
|----------|-------------|
| `/debug/pprof/` | Index page with links to all profiles |
| `/debug/pprof/profile` | CPU profile (default 30s, configurable via `?seconds=N`) |
| `/debug/pprof/heap` | Memory allocation profile |
| `/debug/pprof/goroutine` | Goroutine stack traces |
| `/debug/pprof/threadcreate` | Thread creation profile |
| `/debug/pprof/block` | Blocking profile |
| `/debug/pprof/mutex` | Mutex contention profile |
| `/debug/pprof/trace` | Execution trace |
| `/debug/pprof/cmdline` | Command line arguments |
| `/debug/pprof/symbol` | Symbol lookup |
| `/health` | Simple health check |

## Security Considerations

1. **Localhost binding by default**: The profiler server binds to `localhost:6060` by default, preventing external access
2. **Explicit enablement**: Must be explicitly enabled via config or environment variable
3. **Separate port**: Runs on a different port from the main API, preventing accidental exposure
4. **No authentication**: Standard pprof endpoints don't require auth (rely on network-level access control)

### Production Usage

For production profiling, consider:
- Use SSH tunneling to access the profiler remotely: `ssh -L 6060:localhost:6060 server`
- Or bind to a specific internal interface: `SP_PROFILER_LISTEN=10.0.0.5:6060`
- Never expose the profiler port externally without additional access controls

## Testing

```go
func TestProfilerServer_Disabled(t *testing.T) {
    r := require.New(t)
    cfg := &config.ProfilerConfig{Enabled: false}
    srv := profiler.New(cfg)

    err := srv.Start(context.Background())
    r.NoError(err)

    // Should not be listening
    _, err = http.Get("http://localhost:6060/health")
    r.Error(err)
}

func TestProfilerServer_Enabled(t *testing.T) {
    r := require.New(t)
    cfg := &config.ProfilerConfig{
        Enabled: true,
        Listen:  "localhost:16060", // Use non-standard port for testing
    }
    srv := profiler.New(cfg)

    err := srv.Start(context.Background())
    r.NoError(err)

    defer srv.Shutdown(context.Background())

    // Wait for server to start
    time.Sleep(100 * time.Millisecond)

    // Should be listening
    resp, err := http.Get("http://localhost:16060/health")
    r.NoError(err)
    r.Equal(http.StatusOK, resp.StatusCode)

    // pprof index should be accessible
    resp, err = http.Get("http://localhost:16060/debug/pprof/")
    r.NoError(err)
    r.Equal(http.StatusOK, resp.StatusCode)
}
```

## Implementation Steps

### Step 1: Config
1.1. Add `ProfilerConfig` struct to `config.go`
1.2. Add `Profiler` field to `Config` struct (after `Node`, before `RunMode`)
1.3. Add defaults in `Load()` function

### Step 2: Profiler Package
2.1. Create `back/internal/profiler/profiler.go`
2.2. Implement `Server` struct with `Start()` and `Shutdown()` methods
2.3. Register standard pprof handlers

### Step 3: Integration
3.1. Add `profilerSrv` field to `app.Server` struct
3.2. Initialize profiler in `NewServer()` after existing field assignments
3.3. Start profiler at beginning of `Start()` method (before workers/HTTP)
3.4. Shutdown profiler in `Close()` method (before database close)

### Step 4: Tests
4.1. Test disabled profiler (no-op)
4.2. Test enabled profiler (endpoints accessible)
4.3. Test graceful shutdown

---

**Status**: In Progress | **Created**: 2026-01-03 | **Updated**: 2026-03-22

## Implementation Plan

### Step 1: Config
- Add ProfilerConfig struct and Profiler field to Config
- Add defaults in Load()

### Step 2: Profiler Package
- Create back/internal/profiler/profiler.go with Server, Start, Shutdown

### Step 3: Server Integration
- Add profilerSrv to app.Server, initialize, start, and shutdown

### Step 4: Tests
- Test disabled and enabled profiler
