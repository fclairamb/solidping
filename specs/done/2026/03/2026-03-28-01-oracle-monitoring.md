# Oracle Database Monitoring

## Overview

Add an Oracle Database health check that verifies the database is reachable, authenticates correctly, and can execute queries. Follows the same pattern as the existing PostgreSQL, MySQL, and MSSQL checkers, adapted for Oracle's connection string formats (Easy Connect, TNS, and service name conventions).

**Use cases:**
- Verify Oracle Database is accepting connections
- Run a lightweight query (`SELECT 1 FROM DUAL`) to confirm the server is responsive
- Run custom health queries (tablespace usage, session counts, alert log checks)
- Monitor connection and query latency
- Validate Oracle Wallet or password-based authentication

## Check Type
Type: `oracle`

---

## Backend

### Package: `server/internal/checkers/checkoracle/`

| File | Description |
|------|-------------|
| `config.go` | `OracleConfig` struct with `FromMap()` / `GetConfig()` |
| `checker.go` | `OracleChecker` implementing `Checker` interface |
| `errors.go` | Package-level error sentinel values |
| `samples.go` | `GetSampleConfigs()` with example configurations |
| `checker_test.go` | Table-driven tests with testcontainers |

### Configuration (`OracleConfig`)

```go
type OracleConfig struct {
    Host        string        `json:"host"`
    Port        int           `json:"port,omitempty"`
    Username    string        `json:"username"`
    Password    string        `json:"password,omitempty"`
    ServiceName string        `json:"service_name,omitempty"`
    SID         string        `json:"sid,omitempty"`
    Timeout     time.Duration `json:"timeout,omitempty"`
    Query       string        `json:"query,omitempty"`
}
```

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `host` | string | **yes** | — | Oracle server hostname or IP |
| `port` | int | no | `1521` | Oracle listener port |
| `username` | string | **yes** | — | Database username |
| `password` | string | no | — | Database password |
| `service_name` | string | no | `ORCL` | Oracle service name (preferred over SID) |
| `sid` | string | no | — | Oracle SID (legacy, use service_name instead) |
| `timeout` | duration | no | `10s` | Connection + query timeout |
| `query` | string | no | `SELECT 1 FROM DUAL` | Health check query |

### Validation Rules

- `host` is required
- `username` is required
- `port` must be between 1 and 65535
- At most one of `service_name` or `sid` should be provided (not both)
- `timeout` must be > 0 and ≤ 60s
- `query` if provided must start with `SELECT` (case-insensitive)
- Auto-generate `spec.Name` as `host:port/service_name` if empty
- Auto-generate `spec.Slug` as `oracle-{host}` if empty

### Execution Behavior

1. Parse and apply defaults (port 1521, service_name `ORCL`, timeout 10s, query `SELECT 1 FROM DUAL`)
2. Build connection URL using Easy Connect format:
   - Service name: `oracle://user:pass@host:port/service_name`
   - SID: `oracle://user:pass@host:port?SID=sid`
3. Create context with timeout
4. Record `t0` — open connection with `sql.Open("oracle", connURL)` then `db.PingContext(ctx)`
5. Record `t1` — compute `connection_time_ms`
6. Execute query with `db.QueryContext(ctx, query)`
7. Read first row result
8. Record `t2` — compute `query_time_ms`
9. Close connection
10. Return result with metrics

**Status mapping:**

| Condition | Status |
|-----------|--------|
| Connection + query succeed | `StatusUp` |
| ORA-01017: invalid username/password | `StatusDown` |
| ORA-12541: TNS no listener | `StatusDown` |
| ORA-12514: service name not found | `StatusDown` |
| ORA-12170: TNS connect timeout | `StatusTimeout` |
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
| `service_name` | string | Oracle service name used |
| `query` | string | Query executed |
| `result` | string | First row/column result |
| `error` | string | Error message if check failed |

### Go Driver

Use `github.com/sijms/go-ora/v2` (pure Go Oracle driver, no CGo, no Oracle Instant Client required). This is critical for easy self-hosting — no external dependencies to install.

Add to `server/go.mod`.

```go
import (
    _ "github.com/sijms/go-ora/v2"
)

func (c *OracleConfig) buildConnURL() string {
    port := c.Port
    if port == 0 { port = defaultPort }

    u := &url.URL{
        Scheme: "oracle",
        User:   url.UserPassword(c.Username, c.Password),
        Host:   net.JoinHostPort(c.Host, strconv.Itoa(port)),
    }

    if c.SID != "" {
        q := u.Query()
        q.Set("SID", c.SID)
        u.RawQuery = q.Encode()
    } else {
        serviceName := c.ServiceName
        if serviceName == "" { serviceName = defaultServiceName }
        u.Path = "/" + serviceName
    }

    return u.String()
}
```

### Why `go-ora` over `godror`?

| | `go-ora` | `godror` |
|--|---------|---------|
| **CGo** | No (pure Go) | Yes (requires Oracle Instant Client) |
| **Install** | `go get` only | Requires downloading and configuring Oracle libs |
| **Docker** | Works out of the box | Needs multi-stage build with Oracle libs |
| **Performance** | Adequate for health checks | Better for high-throughput |
| **Compatibility** | Oracle 11g+ | Oracle 11g+ |

For a health check that runs `SELECT 1 FROM DUAL`, `go-ora` is the clear choice.

### Testing

Use **testcontainers** with `gvenzl/oracle-xe:21-slim`:

```go
func setupOracle(t *testing.T) (string, int) {
    req := testcontainers.ContainerRequest{
        Image:        "gvenzl/oracle-xe:21-slim",
        ExposedPorts: []string{"1521/tcp"},
        Env: map[string]string{
            "ORACLE_PASSWORD": "testpass",
        },
        WaitingFor: wait.ForLog("DATABASE IS READY TO USE!").WithStartupTimeout(5 * time.Minute),
    }
    // Oracle XE takes a while to start — set generous timeout
}
```

**Test cases** (table-driven):
1. **Happy path** — connect, `SELECT 1 FROM DUAL`, expect `StatusUp`
2. **Custom query** — `SELECT SYSDATE FROM DUAL`, expect `StatusUp`, verify result
3. **Service name** — connect via service name, expect `StatusUp`
4. **Wrong password** — expect `StatusDown`, error contains "ORA-01017"
5. **Wrong service name** — expect `StatusDown`, error contains "ORA-12514"
6. **Connection refused** — wrong port, expect `StatusDown`
7. **Timeout** — tiny timeout, expect `StatusTimeout`
8. **Non-SELECT query** — `INSERT INTO ...` rejected at validation
9. **Missing host** — validation error
10. **Missing username** — validation error

### Limitations

- Only basic username/password authentication (no Oracle Wallet, Kerberos, or OS auth)
- No RAC-aware connection (no load balancing across RAC nodes)
- `go-ora` may not support all Oracle-specific data types in query results
- Oracle XE container is ~2GB and takes 2-3 minutes to start (slow CI)
- No support for Oracle Cloud Autonomous Database connection strings
- PDB (pluggable database) connect via service name works, but CDB$ROOT needs explicit config

---

## Frontend

### Form Fields

Same layout as PostgreSQL/MySQL with Oracle-specific defaults:
- Port placeholder: `1521`
- Username placeholder: `system`
- Service name field instead of generic "database":
  ```tsx
  <Label htmlFor="serviceName">Service Name (optional)</Label>
  <Input id="serviceName" type="text" placeholder="ORCL" ... />
  ```
- Query placeholder: `SELECT 1 FROM DUAL`

---

## Key Files

| File | Action |
|------|--------|
| `server/internal/checkers/checkoracle/config.go` | New |
| `server/internal/checkers/checkoracle/checker.go` | New |
| `server/internal/checkers/checkoracle/errors.go` | New |
| `server/internal/checkers/checkoracle/samples.go` | New |
| `server/internal/checkers/checkoracle/checker_test.go` | New |
| `server/internal/checkers/checkerdef/types.go` | Modify |
| `server/internal/checkers/registry/registry.go` | Modify |
| `server/go.mod` | Modify (add `github.com/sijms/go-ora/v2`) |
| `web/dash0/src/components/shared/check-form.tsx` | Modify |
| `web/dash0/src/locales/en/checks.json` | Modify |
| `web/dash0/src/locales/fr/checks.json` | Modify |

## Verification

- [ ] `make lint` passes
- [ ] `make test` passes (Oracle container tests may be slow)
- [ ] Create an Oracle check via the UI
- [ ] Verify connection with service name works
- [ ] Verify connection with SID works
- [ ] Verify wrong credentials show `StatusDown` with ORA error
- [ ] Verify custom query returns result
- [ ] E2E tests pass

**Status**: Draft | **Created**: 2026-03-28
