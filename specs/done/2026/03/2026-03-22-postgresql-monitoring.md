# PostgreSQL Monitoring

## Overview

Add a PostgreSQL health check that allows ops and developers to verify their PostgreSQL server is reachable, authenticates correctly, and can execute queries. This goes beyond a simple TCP port check — it validates the database is actually functional and responsive.

**Use cases:**
- Verify PostgreSQL is accepting connections and authenticating
- Run a lightweight query (`SELECT 1`) to confirm the server can process requests
- Run a custom query to check application-specific health (replication lag, table accessibility, row counts)
- Monitor connection and query latency over time
- Validate SSL/TLS connectivity

## Check Type
Type: `postgresql`

---

## Backend

### Package: `back/internal/checkers/checkpostgres/`

| File | Description |
|------|-------------|
| `config.go` | `PostgreSQLConfig` struct with `FromMap()` / `GetConfig()` |
| `checker.go` | `PostgreSQLChecker` implementing `Checker` interface |
| `errors.go` | Package-level error sentinel values |
| `samples.go` | `GetSampleConfigs()` with example configurations |
| `checker_test.go` | Table-driven tests with testcontainers |

### Configuration (`PostgreSQLConfig`)

```go
type PostgreSQLConfig struct {
    Host     string        `json:"host"`
    Port     int           `json:"port"`
    Username string        `json:"username"`
    Password string        `json:"password"`
    Database string        `json:"database"`
    SSLMode  string        `json:"ssl_mode"`
    Timeout  time.Duration `json:"timeout"`
    Query    string        `json:"query"`
}
```

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `host` | string | **yes** | — | PostgreSQL server hostname or IP |
| `port` | int | no | `5432` | PostgreSQL server port |
| `username` | string | **yes** | — | Database user |
| `password` | string | no | — | Database password (empty for trust auth) |
| `database` | string | no | `postgres` | Database name to connect to |
| `ssl_mode` | string | no | `prefer` | SSL mode: `disable`, `require`, `verify-ca`, `verify-full` |
| `timeout` | duration | no | `10s` | Connection + query timeout |
| `query` | string | no | `SELECT 1` | Health check query to execute |

### Validation Rules

- `host` is required and must be non-empty
- `port` must be between 1 and 65535
- `username` is required and must be non-empty
- `timeout` must be > 0 and ≤ 60s
- `ssl_mode` must be one of: `disable`, `require`, `verify-ca`, `verify-full`, or empty (defaults to `prefer`)
- `query` if provided must start with `SELECT` (case-insensitive) — only read queries allowed for safety
- Auto-generate `spec.Name` as `host:port/database` if empty
- Auto-generate `spec.Slug` from host if empty

### Execution Behavior

1. Parse and apply defaults (port 5432, database `postgres`, timeout 10s, query `SELECT 1`)
2. Build connection string: `host=X port=Y user=Z password=W dbname=D sslmode=M`
3. Create context with timeout
4. Record `t0` — open connection with `sql.Open("postgres", connStr)` then `db.PingContext(ctx)`
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
| Authentication failure (invalid password/user) | `StatusDown` |
| Connection refused / host unreachable | `StatusDown` |
| SSL negotiation failure | `StatusDown` |
| Query execution error | `StatusDown` |
| Context deadline exceeded | `StatusTimeout` |
| DNS resolution failure | `StatusError` |
| Invalid configuration | `StatusError` |

### Metrics

| Key | Type | Description |
|-----|------|-------------|
| `connection_time_ms` | float64 | Time to establish connection and authenticate |
| `query_time_ms` | float64 | Time to execute the health check query |

### Output

| Key | Type | Description |
|-----|------|-------------|
| `host` | string | Target hostname |
| `port` | int | Target port |
| `database` | string | Database connected to |
| `ssl_mode` | string | SSL mode used |
| `server_version` | string | PostgreSQL server version (from `SHOW server_version`) if available |
| `query_result` | string | First row/column result from the query (truncated to 1KB) |
| `error` | string | Error message if check failed |

### Registration

**`back/internal/checkers/checkerdef/types.go`**:
```go
const (
    // ... existing types ...
    CheckTypePostgreSQL CheckType = "postgresql"
)
```

Add to `ListCheckTypes()`.

**`back/internal/checkers/registry/registry.go`**:
```go
case checkerdef.CheckTypePostgreSQL:
    return &checkpostgres.PostgreSQLChecker{}, true
```

Add to both `GetChecker()` and `ParseConfig()`.

### Go Driver

Use `github.com/lib/pq` (pure Go, no CGo, widely used). Add to `back/go.mod`.

**Connection string construction** — build using `fmt.Sprintf` with proper escaping to prevent injection:
```go
func buildConnStr(cfg *PostgreSQLConfig) string {
    params := make([]string, 0, 6)
    params = append(params, fmt.Sprintf("host=%s", escapePQParam(cfg.Host)))
    params = append(params, fmt.Sprintf("port=%d", cfg.Port))
    params = append(params, fmt.Sprintf("user=%s", escapePQParam(cfg.Username)))
    if cfg.Password != "" {
        params = append(params, fmt.Sprintf("password=%s", escapePQParam(cfg.Password)))
    }
    params = append(params, fmt.Sprintf("dbname=%s", escapePQParam(cfg.Database)))
    params = append(params, fmt.Sprintf("sslmode=%s", cfg.SSLMode))
    params = append(params, fmt.Sprintf("connect_timeout=%d", max(int(cfg.Timeout.Seconds()), 1)))
    return strings.Join(params, " ")
}

// escapePQParam escapes single quotes and backslashes for libpq connection strings
func escapePQParam(s string) string {
    s = strings.ReplaceAll(s, `\`, `\\`)
    s = strings.ReplaceAll(s, `'`, `\'`)
    if strings.ContainsAny(s, " '\\") {
        return "'" + s + "'"
    }
    return s
}
```

### Sample Configs

```go
func (c *PostgreSQLChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
    return []checkerdef.CheckSpec{
        {
            Name:   "Local PostgreSQL",
            Slug:   "pg-local",
            Period: 1 * time.Minute,
            Config: (&PostgreSQLConfig{
                Host:     "localhost",
                Port:     defaultPort,
                Username: "postgres",
                Database: "postgres",
            }).GetConfig(),
        },
    }
}
```

### Testing

Use **testcontainers** to spin up a real PostgreSQL instance:

```go
func setupPostgres(t *testing.T) (string, int) {
    ctx := context.Background()
    req := testcontainers.ContainerRequest{
        Image:        "postgres:16-alpine",
        ExposedPorts: []string{"5432/tcp"},
        Env: map[string]string{
            "POSTGRES_USER":     "testuser",
            "POSTGRES_PASSWORD": "testpass",
            "POSTGRES_DB":       "testdb",
        },
        WaitingFor: wait.ForListeningPort("5432/tcp").WithStartupTimeout(30 * time.Second),
    }
    container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
        ContainerRequest: req,
        Started:          true,
    })
    require.NoError(t, err)
    t.Cleanup(func() { _ = container.Terminate(ctx) })

    host, _ := container.Host(ctx)
    port, _ := container.MappedPort(ctx, "5432")
    return host, port.Int()
}
```

**Test cases** (table-driven):
1. **Happy path** — connect, `SELECT 1`, expect `StatusUp`
2. **Custom query** — `SELECT current_database()`, expect `StatusUp`, verify `query_result`
3. **Wrong password** — expect `StatusDown`, error contains "password"
4. **Wrong database** — expect `StatusDown`, error contains "does not exist"
5. **Connection refused** — wrong port, expect `StatusDown`
6. **Timeout** — tiny timeout with slow query, expect `StatusTimeout`
7. **Non-SELECT query rejected** — `INSERT INTO ...` in validation, expect error
8. **Missing host** — validation error
9. **Missing username** — validation error

### Limitations

- Only the first column of the first row is captured as `query_result`
- No support for client certificate authentication (can be added later)
- Password is stored in cleartext in config (same as other checkers with credentials)
- `server_version` retrieval is best-effort (separate query, failure doesn't affect status)

---

## Frontend

### Dashboard (`apps/dash0/src/components/shared/check-form.tsx`)

#### Type Registration

Add to `CheckType` union:
```typescript
type CheckType = "http" | "tcp" | "icmp" | "dns" | "ssl" | "heartbeat" | "domain" | "smtp" | "udp" | "ssh" | "postgresql";
```

Add to `checkTypes` array:
```typescript
{ value: "postgresql", label: "PostgreSQL", description: "Check PostgreSQL database connectivity and health" },
```

#### Default Period

PostgreSQL checks default to `"00:01:00"` (1 minute) — falls into the "others" category.

#### State Variables

Add new state variables:
```typescript
const [username, setUsername] = useState(getConfigField(initialData?.config, "username"));
const [password, setPassword] = useState(getConfigField(initialData?.config, "password"));
const [database, setDatabase] = useState(getConfigField(initialData?.config, "database"));
const [sslMode, setSslMode] = useState(getConfigField(initialData?.config, "ssl_mode"));
const [query, setQuery] = useState(getConfigField(initialData?.config, "query"));
```

#### Form Fields

```tsx
case "postgresql":
  return (
    <>
      <div className="space-y-2">
        <Label>Host</Label>
        <div className="flex gap-2">
          <Input
            id="host"
            type="text"
            placeholder="db.example.com"
            value={host}
            onChange={(e) => setHost(e.target.value)}
            className="flex-1"
            data-testid="check-host-input"
          />
          <Input
            id="port"
            type="number"
            placeholder="5432"
            value={port}
            onChange={(e) => setPort(e.target.value)}
            className="w-24"
            data-testid="check-port-input"
          />
        </div>
      </div>
      <div className="grid grid-cols-2 gap-2">
        <div className="space-y-2">
          <Label htmlFor="username">Username</Label>
          <Input
            id="username"
            type="text"
            placeholder="postgres"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            data-testid="check-username-input"
          />
        </div>
        <div className="space-y-2">
          <Label htmlFor="password">Password</Label>
          <Input
            id="password"
            type="password"
            placeholder="••••••••"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            data-testid="check-password-input"
          />
        </div>
      </div>
      <div className="grid grid-cols-2 gap-2">
        <div className="space-y-2">
          <Label htmlFor="database">Database</Label>
          <Input
            id="database"
            type="text"
            placeholder="postgres"
            value={database}
            onChange={(e) => setDatabase(e.target.value)}
            data-testid="check-database-input"
          />
        </div>
        <div className="space-y-2">
          <Label htmlFor="sslMode">SSL Mode</Label>
          <Select value={sslMode || "prefer"} onValueChange={setSslMode}>
            <SelectTrigger data-testid="check-sslmode-select">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="disable">Disable</SelectItem>
              <SelectItem value="prefer">Prefer</SelectItem>
              <SelectItem value="require">Require</SelectItem>
              <SelectItem value="verify-ca">Verify CA</SelectItem>
              <SelectItem value="verify-full">Verify Full</SelectItem>
            </SelectContent>
          </Select>
        </div>
      </div>
      <div className="space-y-2">
        <Label htmlFor="query">Health Check Query (optional)</Label>
        <Input
          id="query"
          type="text"
          placeholder="SELECT 1"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          data-testid="check-query-input"
        />
      </div>
    </>
  );
```

#### Config Submission

```typescript
case "postgresql":
  if (!host) {
    setError("Host is required");
    return;
  }
  if (!username) {
    setError("Username is required");
    return;
  }
  config.host = host;
  if (port) config.port = parseInt(port, 10);
  config.username = username;
  if (password) config.password = password;
  if (database) config.database = database;
  if (sslMode && sslMode !== "prefer") config.ssl_mode = sslMode;
  if (query) config.query = query;
  break;
```

---

## E2E Tests (`apps/dash0/e2e/checks.spec.ts`)

### Test: Create a PostgreSQL check

```typescript
test("should create a new PostgreSQL check", async ({ authenticatedPage }) => {
  const page = authenticatedPage;

  await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
  await page.waitForURL(/\/checks/);
  await page.waitForLoadState("networkidle");
  await page.getByTestId("new-check-button").click();
  await page.waitForURL(/\/checks\/new/);
  await page.waitForLoadState("networkidle");
  await expect(page.getByTestId("check-name-input")).toBeVisible();

  await page.getByTestId("check-type-select").click();
  await page.getByRole("option", { name: /PostgreSQL/i }).click();

  const checkName = `E2E PostgreSQL ${Date.now()}`;
  await page.getByTestId("check-name-input").fill(checkName);
  await page.getByTestId("check-host-input").fill("localhost");
  await page.getByTestId("check-port-input").fill("5432");
  await page.getByTestId("check-username-input").fill("postgres");
  await page.getByTestId("check-password-input").fill("postgres");
  await page.getByTestId("check-database-input").fill("postgres");

  await page.getByTestId("check-submit-button").click();

  await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
  await page.waitForLoadState("networkidle");
  await expect(page.getByRole("heading", { name: checkName })).toBeVisible();

  await page.screenshot({
    path: "test-results/screenshots/checks-postgresql-created.png",
    fullPage: true,
  });
});
```

### Test: Validation error

```typescript
test("should show validation error for PostgreSQL when host or username is empty", async ({ authenticatedPage }) => {
  const page = authenticatedPage;

  await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
  await page.waitForURL(/\/checks/);
  await page.waitForLoadState("networkidle");
  await page.getByTestId("new-check-button").click();
  await page.waitForURL(/\/checks\/new/);
  await page.waitForLoadState("networkidle");
  await expect(page.getByTestId("check-name-input")).toBeVisible();

  await page.getByTestId("check-type-select").click();
  await page.getByRole("option", { name: /PostgreSQL/i }).click();

  await page.getByTestId("check-name-input").fill("PG Missing Config");
  await page.getByTestId("check-submit-button").click();

  await page.waitForTimeout(500);
  expect(page.url()).toContain("/checks/new");
  await expect(page.getByText(/Host is required/i)).toBeVisible();
});
```

---

## Key Files

| File | Action |
|------|--------|
| `back/internal/checkers/checkpostgres/config.go` | New |
| `back/internal/checkers/checkpostgres/checker.go` | New |
| `back/internal/checkers/checkpostgres/errors.go` | New |
| `back/internal/checkers/checkpostgres/samples.go` | New |
| `back/internal/checkers/checkpostgres/checker_test.go` | New |
| `back/internal/checkers/checkerdef/types.go` | Modify |
| `back/internal/checkers/registry/registry.go` | Modify |
| `back/go.mod` | Modify (add `github.com/lib/pq`) |
| `apps/dash0/src/components/shared/check-form.tsx` | Modify |
| `apps/dash0/e2e/checks.spec.ts` | Modify |

## Verification

- [ ] `make lint` passes
- [ ] `make test` passes (including new testcontainer-based PostgreSQL tests)
- [ ] Create a PostgreSQL check via the UI against a local PostgreSQL instance
- [ ] Verify check executes and shows `StatusUp` with connection/query metrics
- [ ] Verify wrong credentials show `StatusDown` with clear error message
- [ ] Verify SSL mode options work (at minimum `disable` and `require`)
- [ ] Verify custom query returns result in output
- [ ] E2E tests pass: `make test-dash`
- [ ] Non-SELECT queries are rejected at validation time

**Status**: Draft | **Created**: 2026-03-22
