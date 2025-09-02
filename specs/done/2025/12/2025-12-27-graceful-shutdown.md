# Graceful Shutdown

## Overview
Implement a graceful shutdown mechanism to ensure the server completes in-flight operations before terminating, preventing data loss and incomplete operations.

## Components Requiring Graceful Shutdown

### 1. HTTP Server
- Stop accepting new HTTP requests
- Wait for all active HTTP requests to complete
- This includes all API endpoints (auth, checks, results, etc.)

### 2. Check Execution
- Allow currently running health checks to complete
- Do not start new scheduled checks after shutdown signal
- Ensure check results are persisted to database

### 3. Background Jobs
- Complete any active background jobs (cleanup, aggregation, etc.)
- Do not dequeue new jobs after shutdown signal
- Persist job state if partially complete

### 4. Database Connections
- Flush any pending database writes
- Close database connection pool cleanly
- Ensure all transactions are committed or rolled back

## Implementation Requirements

### Signal Handling
- Handle `SIGTERM` and `SIGINT` signals
- Log shutdown initiation with clear message
- Coordinate shutdown across all components

### Timeout Configuration
- **Default timeout**: 30 seconds for graceful shutdown
- **Configurable via**: Environment variable `SHUTDOWN_TIMEOUT`
- **Behavior on timeout**: Force shutdown if timeout exceeded
- Log warning if force shutdown occurs

### Shutdown Sequence
1. Receive shutdown signal
2. Stop accepting new requests/jobs
3. Wait for active operations to complete (with timeout)
4. Close database connections
5. Log successful shutdown
6. Exit process

## Implementation Details

### Architecture Overview
Use a centralized shutdown coordinator that manages the graceful shutdown of all components. The coordinator uses Go's context cancellation pattern to signal shutdown to all goroutines.

### Core Components

#### 1. Shutdown Coordinator
Create a `ShutdownCoordinator` type that:
- Listens for OS signals (SIGTERM, SIGINT)
- Manages a root context that gets canceled on shutdown
- Tracks all components that need graceful shutdown
- Enforces the shutdown timeout

```go
type ShutdownCoordinator struct {
    ctx        context.Context
    cancel     context.CancelFunc
    timeout    time.Duration
    logger     *slog.Logger
    components []ShutdownComponent
}

type ShutdownComponent interface {
    Shutdown(ctx context.Context) error
    Name() string
}
```

#### 2. HTTP Server Shutdown
Use `http.Server.Shutdown()` which:
- Stops accepting new connections
- Waits for active requests to complete
- Respects the context timeout

```go
func (s *HTTPServerComponent) Shutdown(ctx context.Context) error {
    s.logger.Info("shutting down HTTP server")
    return s.server.Shutdown(ctx)
}
```

#### 3. Check Scheduler Shutdown
Implement a check scheduler component that:
- Uses a context-aware ticker or time.After
- Stops scheduling new checks when context is canceled
- Waits for running checks using sync.WaitGroup

```go
func (s *CheckScheduler) Shutdown(ctx context.Context) error {
    s.logger.Info("shutting down check scheduler")
    s.stopScheduling() // Stop new checks

    // Wait for running checks with timeout
    done := make(chan struct{})
    go func() {
        s.wg.Wait() // Wait for all checks
        close(done)
    }()

    select {
    case <-done:
        s.logger.Info("all checks completed")
        return nil
    case <-ctx.Done():
        return fmt.Errorf("timeout waiting for checks: %w", ctx.Err())
    }
}
```

#### 4. Background Job Worker Shutdown
Similar pattern for background workers:
- Stop dequeuing new jobs
- Wait for active jobs to complete
- Use context to signal workers to stop

```go
func (w *JobWorker) Shutdown(ctx context.Context) error {
    s.logger.Info("shutting down job worker")
    close(w.stopChan) // Signal to stop dequeuing

    done := make(chan struct{})
    go func() {
        w.wg.Wait()
        close(done)
    }()

    select {
    case <-done:
        return nil
    case <-ctx.Done():
        return ctx.Err()
    }
}
```

#### 5. Database Connection Shutdown
Close database after all other components:
```go
func (d *DatabaseComponent) Shutdown(ctx context.Context) error {
    s.logger.Info("closing database connections")
    return d.db.Close()
}
```

### Main Function Integration

```go
func main() {
    // Parse shutdown timeout from env
    timeout := getShutdownTimeout() // Default 30s

    // Create shutdown coordinator
    coordinator := NewShutdownCoordinator(timeout, logger)

    // Create and register components
    httpServer := NewHTTPServer(coordinator.Context(), ...)
    checkScheduler := NewCheckScheduler(coordinator.Context(), ...)
    jobWorker := NewJobWorker(coordinator.Context(), ...)
    database := NewDatabase(...)

    coordinator.Register(httpServer)
    coordinator.Register(checkScheduler)
    coordinator.Register(jobWorker)
    coordinator.Register(database) // Last to shutdown

    // Start components
    httpServer.Start()
    checkScheduler.Start()
    jobWorker.Start()

    // Wait for shutdown signal
    coordinator.Wait()

    // Trigger graceful shutdown
    if err := coordinator.Shutdown(); err != nil {
        logger.Error("shutdown error", "error", err)
        os.Exit(1)
    }

    logger.Info("shutdown complete")
}
```

### Signal Handling

```go
func (c *ShutdownCoordinator) Wait() {
    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT)

    sig := <-sigChan
    c.logger.Info("received shutdown signal", "signal", sig)
}

func (c *ShutdownCoordinator) Shutdown() error {
    c.logger.Info("initiating graceful shutdown",
        "timeout", c.timeout)

    // Cancel root context
    c.cancel()

    // Create timeout context for shutdown
    ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
    defer cancel()

    // Shutdown components in order
    for _, component := range c.components {
        if err := component.Shutdown(ctx); err != nil {
            c.logger.Error("component shutdown error",
                "component", component.Name(),
                "error", err)
        }
    }

    return nil
}
```

### Context Propagation Pattern
All long-running operations should accept and respect context:

```go
// Check execution
func (e *CheckExecutor) Execute(ctx context.Context, check Check) error {
    select {
    case <-ctx.Done():
        return ctx.Err() // Abort if shutting down
    default:
    }

    // Perform check with context
    result, err := e.performCheck(ctx, check)
    if err != nil {
        return err
    }

    // Persist result before returning
    return e.db.SaveResult(ctx, result)
}
```

### WaitGroup Pattern for Tracking Goroutines
Every component that spawns goroutines should track them:

```go
type CheckScheduler struct {
    wg     sync.WaitGroup
    ctx    context.Context
    // ... other fields
}

func (s *CheckScheduler) scheduleCheck(check Check) {
    s.wg.Add(1)
    go func() {
        defer s.wg.Done()

        if err := s.executor.Execute(s.ctx, check); err != nil {
            s.logger.Error("check failed", "error", err)
        }
    }()
}
```

### Configuration
Read timeout from environment with fallback:
```go
func getShutdownTimeout() time.Duration {
    if timeoutStr := os.Getenv("SHUTDOWN_TIMEOUT"); timeoutStr != "" {
        if d, err := time.ParseDuration(timeoutStr); err == nil {
            return d
        }
    }
    return 30 * time.Second // Default
}
```

## Acceptance Criteria

- [ ] Active HTTP requests complete successfully during shutdown
- [ ] Running health checks finish and persist results
- [ ] Database connections close without errors
- [ ] No data loss occurs during graceful shutdown
- [ ] Shutdown completes within configured timeout
- [ ] Clear logging at each shutdown stage
- [ ] Server responds to SIGTERM and SIGINT
- [ ] Force shutdown after timeout with clear warning

## Testing

### Manual Testing
```bash
# Start server, make requests, send SIGTERM
./solidping server &
curl http://localhost:4000/api/mgmt/health &
kill -TERM $!
```

### Integration Tests
- Test shutdown with active HTTP requests
- Test shutdown with running checks
- Test timeout behavior
- Verify database state after shutdown
