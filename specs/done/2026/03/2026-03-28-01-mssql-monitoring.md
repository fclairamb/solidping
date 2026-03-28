# Microsoft SQL Server Monitoring

## Overview

Add a Microsoft SQL Server health check that verifies the database is reachable, authenticates correctly, and can execute queries. Follows the same pattern as the existing PostgreSQL and MySQL checkers, adapted for SQL Server's connection string format and authentication modes.

**Use cases:**
- Verify SQL Server is accepting connections (TCP or named pipes)
- Run a lightweight query (`SELECT 1`) to confirm the server can process requests
- Run custom health queries (replication status, AG health, blocking queries)
- Monitor connection and query latency
- Validate Windows Authentication or SQL Authentication connectivity

## Check Type
Type: `mssql`

---

## Backend

### Package: `server/internal/checkers/checkmssql/`

| File | Description |
|------|-------------|
| `config.go` | `MSSQLConfig` struct with `FromMap()` / `GetConfig()` |
| `checker.go` | `MSSQLChecker` implementing `Checker` interface |
| `errors.go` | Package-level error sentinel values |
| `samples.go` | `GetSampleConfigs()` with example configurations |
| `checker_test.go` | Table-driven tests with testcontainers |

### Configuration (`MSSQLConfig`)

```go
type MSSQLConfig struct {
    Host     string        `json:"host"`
    Port     int           `json:"port,omitempty"`
    Username string        `json:"username"`
    Password string        `json:"password,omitempty"`
    Database string        `json:"database,omitempty"`
    Encrypt  string        `json:"encrypt,omitempty"`
    Timeout  time.Duration `json:"timeout,omitempty"`
    Query    string        `json:"query,omitempty"`
}
```

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `host` | string | **yes** | — | SQL Server hostname or IP |
| `port` | int | no | `1433` | SQL Server TCP port |
| `username` | string | **yes** | — | SQL Authentication username |
| `password` | string | no | — | SQL Authentication password |
| `database` | string | no | `master` | Database name to connect to |
| `encrypt` | string | no | `false` | Encryption: `true`, `false`, `disable` |
| `timeout` | duration | no | `10s` | Connection + query timeout |
| `query` | string | no | `SELECT 1` | Health check query to execute |

### Validation Rules

- `host` is required and must be non-empty
- `username` is required and must be non-empty
- `port` must be between 1 and 65535
- `timeout` must be > 0 and ≤ 60s
- `encrypt` if provided must be one of: `true`, `false`, `disable`
- `query` if provided must start with `SELECT` (case-insensitive)
- Auto-generate `spec.Name` as `host:port/database` if empty
- Auto-generate `spec.Slug` as `mssql-{host}` if empty

### Execution Behavior

1. Parse and apply defaults (port 1433, database `master`, timeout 10s, query `SELECT 1`)
2. Build connection URL: `sqlserver://user:password@host:port?database=db&encrypt=mode&dial+timeout=N`
3. Create context with timeout
4. Record `t0` — open connection with `sql.Open("sqlserver", connURL)` then `db.PingContext(ctx)`
5. Record `t1` (connection established) — compute `connection_time_ms`
6. Execute query with `db.QueryContext(ctx, query)`
7. Read first row result (scan into string)
8. Record `t2` (query complete) — compute `query_time_ms`
9. Close connection
10. Return result with metrics and output

**Status mapping:**

| Condition | Status |
|-----------|--------|
| Connection + query succeed | `StatusUp` |
| Authentication failure | `StatusDown` |
| Connection refused / host unreachable | `StatusDown` |
| TLS negotiation failure | `StatusDown` |
| Query execution error | `StatusDown` |
| Context deadline exceeded | `StatusTimeout` |
| Invalid configuration | `StatusError` |

### Metrics

| Key | Type | Description |
|-----|------|-------------|
| `connection_time_ms` | float64 | Time to establish connection and authenticate |
| `query_time_ms` | float64 | Time to execute the health check query |
| `total_time_ms` | float64 | Total check duration |

### Output

| Key | Type | Description |
|-----|------|-------------|
| `host` | string | Target hostname |
| `port` | int | Target port |
| `query` | string | Query executed |
| `result` | string | First row/column result |
| `error` | string | Error message if check failed |

### Go Driver

Use `github.com/microsoft/go-mssqldb` (official Microsoft driver for Go, pure Go, no CGo). Add to `server/go.mod`.

```go
import (
    _ "github.com/microsoft/go-mssqldb"
)

func (c *MSSQLConfig) buildConnURL() string {
    port := c.Port
    if port == 0 { port = defaultPort }
    database := c.Database
    if database == "" { database = defaultDatabase }
    encrypt := c.Encrypt
    if encrypt == "" { encrypt = "false" }

    query := url.Values{}
    query.Set("database", database)
    query.Set("encrypt", encrypt)
    query.Set("dial timeout", fmt.Sprintf("%d", int(c.Timeout.Seconds())))

    u := &url.URL{
        Scheme:   "sqlserver",
        User:     url.UserPassword(c.Username, c.Password),
        Host:     net.JoinHostPort(c.Host, strconv.Itoa(port)),
        RawQuery: query.Encode(),
    }
    return u.String()
}
```

### Testing

Use **testcontainers** with the official SQL Server Linux container:

```go
func setupMSSQL(t *testing.T) (string, int) {
    req := testcontainers.ContainerRequest{
        Image:        "mcr.microsoft.com/mssql/server:2022-latest",
        ExposedPorts: []string{"1433/tcp"},
        Env: map[string]string{
            "ACCEPT_EULA":    "Y",
            "MSSQL_SA_PASSWORD": "YourStrong!Passw0rd",
        },
        WaitingFor: wait.ForListeningPort("1433/tcp").WithStartupTimeout(60 * time.Second),
    }
    // ... standard testcontainer setup
}
```

**Test cases** (table-driven):
1. **Happy path** — connect, `SELECT 1`, expect `StatusUp`
2. **Custom query** — `SELECT @@VERSION`, expect `StatusUp`, verify result
3. **Wrong password** — expect `StatusDown`
4. **Wrong database** — expect `StatusDown`
5. **Connection refused** — wrong port, expect `StatusDown`
6. **Timeout** — tiny timeout, expect `StatusTimeout`
7. **Non-SELECT query rejected** — validation error
8. **Missing host** — validation error
9. **Missing username** — validation error

### Limitations

- Only SQL Authentication supported (no Windows/Kerberos auth)
- Named instances not supported (use port number instead)
- Always Encrypted not supported
- Password stored in cleartext in config
- Azure AD authentication not supported in initial implementation

---

## Frontend

### Form Fields

Same layout as PostgreSQL/MySQL with different defaults:
- Port placeholder: `1433`
- Username placeholder: `sa`
- Database placeholder: `master`

---

## Key Files

| File | Action |
|------|--------|
| `server/internal/checkers/checkmssql/config.go` | New |
| `server/internal/checkers/checkmssql/checker.go` | New |
| `server/internal/checkers/checkmssql/errors.go` | New |
| `server/internal/checkers/checkmssql/samples.go` | New |
| `server/internal/checkers/checkmssql/checker_test.go` | New |
| `server/internal/checkers/checkerdef/types.go` | Modify |
| `server/internal/checkers/registry/registry.go` | Modify |
| `server/go.mod` | Modify (add `github.com/microsoft/go-mssqldb`) |
| `web/dash0/src/components/shared/check-form.tsx` | Modify |
| `web/dash0/src/locales/en/checks.json` | Modify |
| `web/dash0/src/locales/fr/checks.json` | Modify |

## Verification

- [ ] `make lint` passes
- [ ] `make test` passes (including testcontainer-based SQL Server tests)
- [ ] Create a MSSQL check via the UI
- [ ] Verify check executes and shows `StatusUp`
- [ ] Verify wrong credentials show `StatusDown`
- [ ] Verify custom query returns result
- [ ] Verify encryption modes work
- [ ] E2E tests pass

**Status**: Draft | **Created**: 2026-03-28

## Implementation Plan

1. Add `CheckTypeMSSQL` constant to `checkerdef/types.go` and `ListCheckTypes()`
2. Create `checkmssql/` package: `errors.go`, `config.go`, `checker.go`, `samples.go`
3. Register in `registry/registry.go` (both `GetChecker` and `ParseConfig`)
4. Add `go-mssqldb` dependency
5. Update frontend: `check-form.tsx` (type union, checkTypes array, config builder, form fields, submit handler)
6. Update locale files: `en/checks.json`, `fr/checks.json`
7. Run QA: `make fmt`, `make build-backend`, `make lint-back`, `make test`
