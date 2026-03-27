# RabbitMQ Monitoring

## Overview

Add a RabbitMQ health check that verifies a RabbitMQ broker is reachable and operational. The check supports two modes: AMQP protocol-level connectivity (connect, open channel, declare passive queue) and HTTP Management API health checks. The management API mode is preferred for production as it provides richer health data without affecting broker state.

**Use cases:**
- Verify RabbitMQ is accepting AMQP connections
- Check cluster health via the Management API (`/api/health/checks/alarms`)
- Validate authentication credentials
- Monitor connection latency
- Detect broker alarms (disk space, memory, file descriptors)
- Verify specific queues exist and are operational

## Check Type
Type: `rabbitmq`

---

## Backend

### Package: `server/internal/checkers/checkrabbitmq/`

| File | Description |
|------|-------------|
| `config.go` | `RabbitMQConfig` struct with `FromMap()` / `GetConfig()` |
| `checker.go` | `RabbitMQChecker` implementing `Checker` interface |
| `errors.go` | Package-level error sentinel values |
| `samples.go` | `GetSampleConfigs()` with example configurations |
| `checker_test.go` | Table-driven tests with testcontainers |

### Configuration (`RabbitMQConfig`)

```go
type RabbitMQConfig struct {
    Host        string        `json:"host"`
    Port        int           `json:"port,omitempty"`
    Username    string        `json:"username"`
    Password    string        `json:"password,omitempty"`
    Vhost       string        `json:"vhost,omitempty"`
    TLS         bool          `json:"tls,omitempty"`
    Mode        string        `json:"mode,omitempty"`
    ManagementPort int        `json:"management_port,omitempty"`
    Queue       string        `json:"queue,omitempty"`
    Timeout     time.Duration `json:"timeout,omitempty"`
}
```

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `host` | string | **yes** | — | RabbitMQ server hostname |
| `port` | int | no | `5672` | AMQP port |
| `username` | string | **yes** | — | RabbitMQ username |
| `password` | string | no | — | RabbitMQ password |
| `vhost` | string | no | `/` | Virtual host |
| `tls` | bool | no | `false` | Enable AMQPS (TLS) |
| `mode` | string | no | `amqp` | Check mode: `amqp` or `management` |
| `management_port` | int | no | `15672` | Management API port (only for management mode) |
| `queue` | string | no | — | Queue name to verify exists (AMQP mode only) |
| `timeout` | duration | no | `10s` | Connection timeout |

### Validation Rules

- `host` is required
- `username` is required
- `port` must be between 1 and 65535
- `mode` must be `amqp` or `management`
- `management_port` must be between 1 and 65535
- `timeout` must be > 0 and ≤ 60s
- Auto-generate `spec.Name` as `host:port` if empty
- Auto-generate `spec.Slug` as `rabbitmq-{host}` if empty

### Execution Behavior

**AMQP mode** (default):
1. Build AMQP URI: `amqp(s)://user:pass@host:port/vhost`
2. Create context with timeout
3. Record `t0` — connect via `amqp091.DialConfig(uri, config)`
4. Record `t1` — compute `connection_time_ms`
5. Open a channel
6. If `queue` is set, declare passive queue to verify existence
7. Close channel and connection
8. Return `StatusUp`

**Management API mode**:
1. Build management URL: `http(s)://host:management_port/api/health/checks/alarms`
2. Create HTTP request with Basic Auth
3. Record `t0` — send request
4. Check HTTP status code (200 = healthy)
5. Parse response body for alarm details
6. Record `t1` — compute `api_time_ms`
7. Return `StatusUp` if no alarms, `StatusDown` if alarms present

**Status mapping:**

| Condition | Status |
|-----------|--------|
| AMQP connection succeeds (+ queue exists if specified) | `StatusUp` |
| Management API returns 200 with no alarms | `StatusUp` |
| Authentication failure | `StatusDown` |
| Connection refused | `StatusDown` |
| Queue not found (AMQP mode) | `StatusDown` |
| Management API returns alarms | `StatusDown` |
| Management API returns non-200 | `StatusDown` |
| Context deadline exceeded | `StatusTimeout` |
| Invalid configuration | `StatusError` |

### Metrics

| Key | Type | Description |
|-----|------|-------------|
| `connection_time_ms` | float64 | Time to connect (AMQP) or API response time (management) |
| `total_time_ms` | float64 | Total check duration |

### Output

| Key | Type | Description |
|-----|------|-------------|
| `host` | string | Target hostname |
| `port` | int | Target port |
| `mode` | string | Check mode used |
| `vhost` | string | Virtual host |
| `queue_exists` | bool | Queue existence result (if checked) |
| `alarms` | string | Active alarms (management mode) |
| `error` | string | Error message if check failed |

### Go Driver

Use `github.com/rabbitmq/amqp091-go` (official RabbitMQ Go client). Add to `server/go.mod`.

For Management API mode, use `net/http` from the standard library.

```go
import amqp "github.com/rabbitmq/amqp091-go"

func (c *RabbitMQConfig) buildAMQPURI() string {
    scheme := "amqp"
    if c.TLS { scheme = "amqps" }
    vhost := c.Vhost
    if vhost == "" { vhost = "/" }
    return fmt.Sprintf("%s://%s:%s@%s/%s",
        scheme,
        url.PathEscape(c.Username),
        url.PathEscape(c.Password),
        net.JoinHostPort(c.Host, strconv.Itoa(c.resolvePort())),
        url.PathEscape(vhost),
    )
}
```

### Testing

Use **testcontainers** with `rabbitmq:3-management-alpine`:

**Test cases** (table-driven):
1. **AMQP happy path** — connect, open channel, expect `StatusUp`
2. **Queue exists** — declare queue then check it, expect `StatusUp`
3. **Queue not found** — non-existent queue, expect `StatusDown`
4. **Management API healthy** — expect `StatusUp`
5. **Wrong credentials** — expect `StatusDown`
6. **Connection refused** — wrong port, expect `StatusDown`
7. **Wrong vhost** — expect `StatusDown`
8. **Timeout** — unreachable host, expect `StatusTimeout`
9. **Missing host** — validation error
10. **Invalid mode** — validation error

### Limitations

- No cluster node enumeration (Management API mode doesn't walk all nodes)
- No queue depth monitoring (use Prometheus/Grafana for that)
- No consumer count or message rate metrics
- AMQP mode opens and immediately closes a channel (minimal broker impact)
- Client certificates (mTLS) not supported in initial implementation
- Shovel and Federation plugin health not checked

---

## Frontend

### Form Fields

```tsx
case "rabbitmq":
  return (
    <>
      <div className="space-y-2">
        <Label>Host</Label>
        <div className="flex gap-2">
          <Input id="host" type="text" placeholder="rabbitmq.example.com"
            value={host} onChange={(e) => setHost(e.target.value)} className="flex-1" />
          <Input id="port" type="number" placeholder="5672"
            value={port} onChange={(e) => setPort(e.target.value)} className="w-24" />
        </div>
      </div>
      <div className="flex gap-4">
        <div className="space-y-2 flex-1">
          <Label htmlFor="username">Username</Label>
          <Input id="username" type="text" placeholder="guest"
            value={username} onChange={(e) => setUsername(e.target.value)} />
        </div>
        <div className="space-y-2 flex-1">
          <Label htmlFor="password">Password</Label>
          <Input id="password" type="password"
            value={password} onChange={(e) => setPassword(e.target.value)} />
        </div>
      </div>
      <div className="space-y-2">
        <Label htmlFor="vhost">Virtual Host (optional)</Label>
        <Input id="vhost" type="text" placeholder="/"
          value={vhost} onChange={(e) => setVhost(e.target.value)} />
      </div>
      <div className="space-y-2">
        <Label htmlFor="queue">Queue Name (optional)</Label>
        <Input id="queue" type="text" placeholder="my-queue"
          value={queue} onChange={(e) => setQueue(e.target.value)} />
      </div>
    </>
  );
```

---

## Key Files

| File | Action |
|------|--------|
| `server/internal/checkers/checkrabbitmq/config.go` | New |
| `server/internal/checkers/checkrabbitmq/checker.go` | New |
| `server/internal/checkers/checkrabbitmq/errors.go` | New |
| `server/internal/checkers/checkrabbitmq/samples.go` | New |
| `server/internal/checkers/checkrabbitmq/checker_test.go` | New |
| `server/internal/checkers/checkerdef/types.go` | Modify |
| `server/internal/checkers/registry/registry.go` | Modify |
| `server/go.mod` | Modify (add `github.com/rabbitmq/amqp091-go`) |
| `web/dash0/src/components/shared/check-form.tsx` | Modify |
| `web/dash0/src/locales/en/checks.json` | Modify |
| `web/dash0/src/locales/fr/checks.json` | Modify |

## Verification

- [ ] `make lint` passes
- [ ] `make test` passes
- [ ] Create a RabbitMQ check in AMQP mode via the UI
- [ ] Create a RabbitMQ check in Management mode via the UI
- [ ] Verify AMQP mode detects unreachable broker
- [ ] Verify Management mode detects alarms
- [ ] Verify queue existence checking works
- [ ] E2E tests pass

**Status**: Draft | **Created**: 2026-03-28
