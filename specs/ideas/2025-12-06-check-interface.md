# Checker Interface Design

This document defines the Go interface that all protocol checkers must implement.

## Core Interface

```go
package checker

import (
    "context"
    "time"
)

// Status represents the outcome of a check execution
type Status int

const (
    StatusUp      Status = 1 // Check succeeded
    StatusDown    Status = 2 // Check failed (target unreachable or unhealthy)
    StatusTimeout Status = 3 // Check timed out
    StatusError   Status = 4 // Internal error during check execution
)

// Result represents the outcome of executing a check
type Result struct {
    Status     Status                 // The check status
    Duration   time.Duration          // Time taken to execute the check
    Metrics    map[string]interface{} // Numerical metrics that can be aggregated (e.g., ttfb, dns_time)
    Output     map[string]interface{} // Diagnostic output (error messages, status text, etc.)
}

// Config is the interface that all check configurations must implement
// Each checker defines its own config struct with protocol-specific fields
type Config interface {
    // Validate checks if the configuration is valid
    // Returns nil if valid, or an error describing what's wrong
    Validate() error
}

// Checker is the interface that all protocol checkers must implement
type Checker interface {
    // Type returns the check type identifier this checker handles (e.g., "http", "tcp")
    Type() string
    
    // Validate checks if the configuration is valid. 
    // It shall not perform any network operations.
    // Returns nil if valid, or an error describing what's wrong
    Validate(config Config) error

    // Execute performs the check and returns the result
    // The context should be used for cancellation and timeout control
    // The config is already validated before being passed to Execute
    Execute(ctx context.Context, config Config) Result
}
```

## Static Registry Pattern

```go
// GetChecker retrieves a checker by type.
// Returns the checker and true if found, nil and false otherwise.
func GetChecker(checkType string) (Checker, bool) {
    switch checkType {
    case "http":
        return &HTTPChecker{}, true
    case "tcp":
        return &TCPChecker{}, true
    case "ping":
        return &PingChecker{}, true
    case "dns":
        return &DNSChecker{}, true
    case "ssl":
        return &SSLChecker{}, true
    }

    return nil, false
}

// ParseConfig creates the appropriate config struct for a given check type.
// Returns the config interface and true if the type is known, nil and false otherwise.
func ParseConfig(checkType string) (Config, bool) {
    switch checkType {
    case "http":
        return &HTTPConfig{}, true
    case "tcp":
        return &TCPConfig{}, true
    case "ping":
        return &PingConfig{}, true
    case "dns":
        return &DNSConfig{}, true
    case "ssl":
        return &SSLConfig{}, true
    }

    return nil, false
}
```

## Example Implementations

### HTTP Checker

```go
// HTTPConfig holds the configuration for HTTP checks
type HTTPConfig struct {
    URL            string `json:"url"`
    Method         string `json:"method"`
    ExpectedStatus int    `json:"expected_status"`
    Headers        map[string]string `json:"headers"`
    Body           string `json:"body"`
}

// Validate implements the Config interface
func (c *HTTPConfig) Validate() error {
    if c.URL == "" {
        return errors.New("url is required")
    }
    if !strings.HasPrefix(c.URL, "http://") && !strings.HasPrefix(c.URL, "https://") {
        return errors.New("url must start with http:// or https://")
    }
    if c.Method != "" {
        validMethods := map[string]bool{"GET": true, "POST": true, "PUT": true, "DELETE": true, "HEAD": true, "OPTIONS": true, "PATCH": true}
        if !validMethods[strings.ToUpper(c.Method)] {
            return fmt.Errorf("invalid HTTP method: %s", c.Method)
        }
    }
    if c.ExpectedStatus != 0 && (c.ExpectedStatus < 100 || c.ExpectedStatus > 599) {
        return fmt.Errorf("expected_status must be between 100 and 599, got %d", c.ExpectedStatus)
    }
    return nil
}

type HTTPChecker struct{}

func (c *HTTPChecker) Type() string { return "http" }

func (c *HTTPChecker) Execute(ctx context.Context, config Config) Result {
    cfg := config.(*HTTPConfig)

    // Apply defaults
    method := cfg.Method
    if method == "" {
        method = "GET"
    }
    expectedStatus := cfg.ExpectedStatus
    if expectedStatus == 0 {
        expectedStatus = 200
    }

    start := time.Now()

    req, err := http.NewRequestWithContext(ctx, method, cfg.URL, nil)
    if err != nil {
        return Result{
            Status:   StatusError,
            Duration: time.Since(start),
            Output:   map[string]interface{}{"error": err.Error()},
        }
    }

    // Add custom headers
    for key, value := range cfg.Headers {
        req.Header.Set(key, value)
    }

    resp, err := http.DefaultClient.Do(req)
    duration := time.Since(start)

    if err != nil {
        if ctx.Err() == context.DeadlineExceeded {
            return Result{
                Status:   StatusTimeout,
                Duration: duration,
                Output:   map[string]interface{}{"error": "request timed out"},
            }
        }
        return Result{
            Status:   StatusDown,
            Duration: duration,
            Output:   map[string]interface{}{"error": err.Error()},
        }
    }
    defer resp.Body.Close()

    status := StatusUp
    if resp.StatusCode != expectedStatus {
        status = StatusDown
    }

    return Result{
        Status:     status,
        StatusCode: resp.StatusCode,
        Duration:   duration,
        Output: map[string]interface{}{
            "url":         cfg.URL,
            "status_code": resp.StatusCode,
        },
    }
}
```

### TCP Checker

```go
// TCPConfig holds the configuration for TCP checks
type TCPConfig struct {
    Host string `json:"host"`
    Port int    `json:"port"`
}

// Validate implements the Config interface
func (c *TCPConfig) Validate() error {
    if c.Host == "" {
        return errors.New("host is required")
    }
    if c.Port < 1 || c.Port > 65535 {
        return fmt.Errorf("port must be between 1 and 65535, got %d", c.Port)
    }
    return nil
}

type TCPChecker struct{}

func (c *TCPChecker) Type() string { return "tcp" }

func (c *TCPChecker) Execute(ctx context.Context, config Config) Result {
    cfg := config.(*TCPConfig)
    addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)

    start := time.Now()

    var d net.Dialer
    conn, err := d.DialContext(ctx, "tcp", addr)
    duration := time.Since(start)

    if err != nil {
        if ctx.Err() == context.DeadlineExceeded {
            return Result{
                Status:   StatusTimeout,
                Duration: duration,
                Output:   map[string]interface{}{"error": "connection timed out"},
            }
        }
        return Result{
            Status:   StatusDown,
            Duration: duration,
            Output:   map[string]interface{}{"error": err.Error()},
        }
    }
    conn.Close()

    return Result{
        Status:   StatusUp,
        Duration: duration,
        Output:   map[string]interface{}{"address": addr},
    }
}
```

### Ping (ICMP) Checker

```go
// PingConfig holds the configuration for ICMP ping checks
type PingConfig struct {
    Host  string `json:"host"`
    Count int    `json:"count"`
}

// Validate implements the Config interface
func (c *PingConfig) Validate() error {
    if c.Host == "" {
        return errors.New("host is required")
    }
    if c.Count != 0 && (c.Count < 1 || c.Count > 100) {
        return fmt.Errorf("count must be between 1 and 100, got %d", c.Count)
    }
    return nil
}

type PingChecker struct{}

func (c *PingChecker) Type() string { return "ping" }

func (c *PingChecker) Execute(ctx context.Context, config Config) Result {
    cfg := config.(*PingConfig)

    // Apply default
    count := cfg.Count
    if count == 0 {
        count = 3
    }

    start := time.Now()

    pinger, err := probing.NewPinger(cfg.Host)
    if err != nil {
        return Result{
            Status:   StatusError,
            Duration: time.Since(start),
            Output:   map[string]interface{}{"error": err.Error()},
        }
    }

    pinger.Count = count
    pinger.SetPrivileged(true) // May require root/capabilities

    done := make(chan error, 1)
    go func() { done <- pinger.Run() }()

    select {
    case <-ctx.Done():
        pinger.Stop()
        return Result{
            Status:   StatusTimeout,
            Duration: time.Since(start),
            Output:   map[string]interface{}{"error": "ping timed out"},
        }
    case err := <-done:
        duration := time.Since(start)
        if err != nil {
            return Result{
                Status:   StatusDown,
                Duration: duration,
                Output:   map[string]interface{}{"error": err.Error()},
            }
        }

        stats := pinger.Statistics()
        status := StatusUp
        if stats.PacketLoss == 100 {
            status = StatusDown
        }

        return Result{
            Status:   status,
            Duration: duration,
            Output: map[string]interface{}{
                "host":         cfg.Host,
                "packets_sent": stats.PacketsSent,
                "packets_recv": stats.PacketsRecv,
                "packet_loss":  stats.PacketLoss,
                "avg_rtt_ms":   stats.AvgRtt.Milliseconds(),
            },
        }
    }
}
```

## API Integration

Configuration validation is performed during check creation/update to provide immediate feedback to users:

```go
// In the checks handler (internal/handlers/checks/handler.go)
func (h *Handler) CreateCheck(w http.ResponseWriter, r *http.Request) {
    var req CreateCheckRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        h.WriteError(w, http.StatusBadRequest, base.ErrorCodeValidation, "invalid request body")
        return
    }

    // Parse the configuration based on check type using the static registry
    config, ok := checker.ParseConfig(req.Type)
    if !ok {
        h.WriteError(w, http.StatusBadRequest, base.ErrorCodeValidation, "unknown check type")
        return
    }

    // Unmarshal the config JSON into the appropriate struct
    if err := json.Unmarshal(req.Config, config); err != nil {
        h.WriteError(w, http.StatusBadRequest, base.ErrorCodeValidation, "invalid config format")
        return
    }

    // Validate the configuration
    if err := config.Validate(); err != nil {
        h.WriteError(w, http.StatusBadRequest, base.ErrorCodeValidation, err.Error())
        return
    }

    // Proceed with creating the check...
}
```

## Worker Integration

The worker uses the static registry to execute checks from the job queue (no validation needed since config was validated at creation time):

```go
func (w *Worker) executeJob(ctx context.Context, job *db.CheckJob) {
    // Create timeout context based on check config
    timeout := 30 * time.Second // default
    if job.Timeout > 0 {
        timeout = time.Duration(job.Timeout) * time.Second
    }
    ctx, cancel := context.WithTimeout(ctx, timeout)
    defer cancel()

    // Get the checker from the static registry
    chk, ok := checker.GetChecker(job.Type)
    if !ok {
        // Log error: unknown check type
        return
    }

    // Parse the configuration based on check type
    config, ok := checker.ParseConfig(job.Type)
    if !ok {
        // Log error: unknown check type
        return
    }

    // Unmarshal stored config into the appropriate struct
    if err := json.Unmarshal(job.ConfigJSON, config); err != nil {
        // Log error: invalid config format
        return
    }

    // Execute the check (config already validated at creation time)
    result := chk.Execute(ctx, config)

    // Store the result
    dbResult := db.NewResult(job.OrganizationUID, job.CheckUID, db.ResultStatus(result.Status), int(result.Duration.Milliseconds()))
    if result.StatusCode != 0 {
        dbResult.StatusCode = &result.StatusCode
    }
    dbResult.Output = result.Output
    w.db.CreateResult(ctx, dbResult)
}
```

## Supported Check Types

| Type   | Description                                  | Key Config Fields                          |
|--------|----------------------------------------------|-------------------------------------------|
| `http` | HTTP/HTTPS endpoint monitoring               | `url`, `method`, `expected_status`, `headers`, `body` |
| `tcp`  | TCP port connectivity                        | `host`, `port`                            |
| `ping` | ICMP ping                                    | `host`, `count`                           |
| `dns`  | DNS record resolution                        | `host`, `record_type`, `expected_value`   |
| `ssl`  | SSL/TLS certificate validation               | `host`, `port`, `warn_days`               |

## Design Decisions

1. **Simple interface**: `Type()` and `Execute()` methods keep checker implementations focused
2. **Config as interface**: Each checker defines its own strongly-typed config struct that implements the `Config` interface with a `Validate()` method
3. **Type removed from Config**: The check type is handled separately in the static registry, avoiding redundancy since the checker already knows its type
4. **API-time validation**: Config validation happens during check creation/update via the `Validate()` method to provide immediate UI feedback—not at execution time
5. **Context-based cancellation**: Standard Go pattern for timeouts and cancellation
6. **Static registry pattern**: Simple `GetChecker()` and `ParseConfig()` functions using switch statements—all check types known at compile time (matches the job registry pattern)
7. **Strongly-typed configs**: Each config struct has specific fields with proper types (no `map[string]interface{}` in checker code)
8. **JSON serialization**: Configs can be marshaled/unmarshaled to/from JSON for database storage
9. **Status codes**: Reuses existing `ResultStatus` constants from `db/models.go`
10. **Dual output fields**: `Metrics` for aggregatable numerical data, `Output` for non-aggregatable diagnostic information
11. **No dynamic registration**: All checkers are registered statically in the registry file, making dependencies explicit and easier to understand
