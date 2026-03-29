# MQTT Broker Monitoring

## Overview

Add an MQTT health check that verifies an MQTT broker is reachable and can handle pub/sub operations. The check connects to the broker, subscribes to a topic, publishes a test message, and verifies the message is received back — confirming end-to-end broker functionality. A simpler mode just validates the connection and CONNACK response.

**Use cases:**
- Verify an MQTT broker (Mosquitto, HiveMQ, EMQX, etc.) is accepting connections
- Validate authentication credentials
- Test pub/sub roundtrip to confirm broker routing is functional
- Monitor connection latency to the broker
- Verify TLS/WebSocket transport layers work correctly
- IoT infrastructure health monitoring

## Check Type
Type: `mqtt`

---

## Backend

### Package: `server/internal/checkers/checkmqtt/`

| File | Description |
|------|-------------|
| `config.go` | `MQTTConfig` struct with `FromMap()` / `GetConfig()` |
| `checker.go` | `MQTTChecker` implementing `Checker` interface |
| `errors.go` | Package-level error sentinel values |
| `samples.go` | `GetSampleConfigs()` with example configurations |
| `checker_test.go` | Table-driven tests with testcontainers |

### Configuration (`MQTTConfig`)

```go
type MQTTConfig struct {
    Host     string        `json:"host"`
    Port     int           `json:"port,omitempty"`
    Username string        `json:"username,omitempty"`
    Password string        `json:"password,omitempty"`
    Topic    string        `json:"topic,omitempty"`
    TLS      bool          `json:"tls,omitempty"`
    Timeout  time.Duration `json:"timeout,omitempty"`
}
```

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `host` | string | **yes** | — | MQTT broker hostname |
| `port` | int | no | `1883` (or `8883` if TLS) | MQTT broker port |
| `username` | string | no | — | Authentication username |
| `password` | string | no | — | Authentication password |
| `topic` | string | no | `solidping/healthcheck` | Topic for pub/sub test |
| `tls` | bool | no | `false` | Enable TLS (MQTTS) |
| `timeout` | duration | no | `10s` | Connection + pub/sub timeout |

### Validation Rules

- `host` is required
- `port` must be between 1 and 65535
- `topic` if provided must not contain wildcard characters (`#`, `+`)
- `timeout` must be > 0 and ≤ 60s
- Auto-generate `spec.Name` as `host:port` if empty
- Auto-generate `spec.Slug` as `mqtt-{host}` if empty

### Execution Behavior

1. Build broker URL: `tcp://host:port` (or `ssl://host:port` if TLS)
2. Create MQTT client options with:
   - Unique client ID: `solidping-check-{random}`
   - Credentials (if provided)
   - TLS config (if enabled)
   - Connect timeout
   - Clean session: `true`
   - Auto-reconnect: `false` (single-shot check)
3. Record `t0` — connect to broker
4. Wait for CONNACK — if error, return `StatusDown`
5. Record `t1` — compute `connection_time_ms`
6. Subscribe to `topic`
7. Publish a timestamped test message to `topic` with QoS 1
8. Wait for the message to be received on subscription (with timeout)
9. Record `t2` — compute `roundtrip_time_ms`
10. Disconnect gracefully
11. Return `StatusUp` with metrics

**Simplified mode** (no topic configured):
- Steps 1-5 only (connection test)
- Return `StatusUp` after successful CONNACK

**Status mapping:**

| Condition | Status |
|-----------|--------|
| Connection succeeds + pub/sub roundtrip completes | `StatusUp` |
| Connection succeeds (no topic mode) | `StatusUp` |
| Connection refused | `StatusDown` |
| Authentication failure (bad credentials) | `StatusDown` |
| TLS handshake failure | `StatusDown` |
| Pub/sub roundtrip times out | `StatusDown` |
| Context deadline exceeded | `StatusTimeout` |
| Invalid configuration | `StatusError` |

### Metrics

| Key | Type | Description |
|-----|------|-------------|
| `connection_time_ms` | float64 | Time to connect and receive CONNACK |
| `roundtrip_time_ms` | float64 | Time for pub/sub roundtrip (if topic set) |
| `total_time_ms` | float64 | Total check duration |

### Output

| Key | Type | Description |
|-----|------|-------------|
| `host` | string | Broker hostname |
| `port` | int | Broker port |
| `topic` | string | Topic used for health check |
| `tls` | bool | Whether TLS was used |
| `error` | string | Error message if check failed |

### Go Driver

Use `github.com/eclipse/paho.mqtt.golang` (most popular Go MQTT client). Add to `server/go.mod`.

```go
import mqtt "github.com/eclipse/paho.mqtt.golang"

func newMQTTClient(cfg *MQTTConfig) mqtt.Client {
    opts := mqtt.NewClientOptions()
    scheme := "tcp"
    if cfg.TLS { scheme = "ssl" }
    opts.AddBroker(fmt.Sprintf("%s://%s", scheme, net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.resolvePort()))))
    opts.SetClientID("solidping-check-" + randomSuffix())
    opts.SetCleanSession(true)
    opts.SetAutoReconnect(false)
    opts.SetConnectTimeout(cfg.resolveTimeout())
    if cfg.Username != "" {
        opts.SetUsername(cfg.Username)
        opts.SetPassword(cfg.Password)
    }
    if cfg.TLS {
        opts.SetTLSConfig(&tls.Config{InsecureSkipVerify: false})
    }
    return mqtt.NewClient(opts)
}
```

### Testing

Use **testcontainers** with `eclipse-mosquitto:2-openssl`:

**Test cases** (table-driven):
1. **Connect only** — no topic, expect `StatusUp`
2. **Pub/sub roundtrip** — publish and receive, expect `StatusUp`
3. **Wrong credentials** — expect `StatusDown`
4. **Connection refused** — wrong port, expect `StatusDown`
5. **Timeout** — unreachable host, expect `StatusTimeout`
6. **TLS connection** — expect `StatusUp`
7. **Missing host** — validation error
8. **Wildcard topic** — validation error

### Limitations

- MQTT v3.1.1 only (MQTT v5 not supported in initial implementation)
- WebSocket transport not supported (use `tcp://` or `ssl://` only)
- No retained message checking
- No shared subscription support
- No message payload validation (keyword matching)
- QoS 2 (exactly-once) not used for simplicity

---

## Frontend

### Form Fields

```tsx
case "mqtt":
  return (
    <>
      <div className="space-y-2">
        <Label>Host</Label>
        <div className="flex gap-2">
          <Input id="host" type="text" placeholder="mqtt.example.com"
            value={host} onChange={(e) => setHost(e.target.value)} className="flex-1" />
          <Input id="port" type="number" placeholder="1883"
            value={port} onChange={(e) => setPort(e.target.value)} className="w-24" />
        </div>
      </div>
      <div className="flex gap-4">
        <div className="space-y-2 flex-1">
          <Label htmlFor="username">Username (optional)</Label>
          <Input id="username" type="text" value={username} onChange={(e) => setUsername(e.target.value)} />
        </div>
        <div className="space-y-2 flex-1">
          <Label htmlFor="password">Password (optional)</Label>
          <Input id="password" type="password" value={password} onChange={(e) => setPassword(e.target.value)} />
        </div>
      </div>
      <div className="space-y-2">
        <Label htmlFor="topic">Topic (optional)</Label>
        <Input id="topic" type="text" placeholder="solidping/healthcheck"
          value={topic} onChange={(e) => setTopic(e.target.value)} />
        <p className="text-xs text-muted-foreground">
          If set, a pub/sub roundtrip test will be performed
        </p>
      </div>
      <label className="flex items-center gap-2">
        <Checkbox checked={tls} onCheckedChange={(v) => setTls(v === true)} />
        <span className="text-sm">Use TLS</span>
      </label>
    </>
  );
```

---

## Key Files

| File | Action |
|------|--------|
| `server/internal/checkers/checkmqtt/config.go` | New |
| `server/internal/checkers/checkmqtt/checker.go` | New |
| `server/internal/checkers/checkmqtt/errors.go` | New |
| `server/internal/checkers/checkmqtt/samples.go` | New |
| `server/internal/checkers/checkmqtt/checker_test.go` | New |
| `server/internal/checkers/checkerdef/types.go` | Modify |
| `server/internal/checkers/registry/registry.go` | Modify |
| `server/go.mod` | Modify (add `github.com/eclipse/paho.mqtt.golang`) |
| `web/dash0/src/components/shared/check-form.tsx` | Modify |
| `web/dash0/src/locales/en/checks.json` | Modify |
| `web/dash0/src/locales/fr/checks.json` | Modify |

## Verification

- [ ] `make lint` passes
- [ ] `make test` passes
- [ ] Create an MQTT check (connect-only) via the UI
- [ ] Create an MQTT check (pub/sub roundtrip) via the UI
- [ ] Verify roundtrip detects stalled broker
- [ ] Verify TLS mode works
- [ ] E2E tests pass

**Status**: Draft | **Created**: 2026-03-28
