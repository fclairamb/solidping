# gRPC Health Check Monitoring

## Overview

Add a gRPC health check that verifies gRPC services are responding correctly. Supports both the standard gRPC Health Checking Protocol (grpc.health.v1) and custom unary method invocations with keyword matching. This is essential for microservice architectures where gRPC is the primary IPC mechanism.

**Use cases:**
- Verify a gRPC service is reachable and responding
- Check specific service health via the standard gRPC health protocol (`grpc.health.v1.Health/Check`)
- Invoke a custom gRPC method and validate the response contains expected content
- Monitor gRPC endpoint latency
- Validate TLS connectivity to gRPC services

## Check Type
Type: `grpc`

---

## Backend

### Package: `server/internal/checkers/checkgrpc/`

| File | Description |
|------|-------------|
| `config.go` | `GRPCConfig` struct with `FromMap()` / `GetConfig()` |
| `checker.go` | `GRPCChecker` implementing `Checker` interface |
| `errors.go` | Package-level error sentinel values |
| `samples.go` | `GetSampleConfigs()` with example configurations |
| `checker_test.go` | Table-driven tests with embedded gRPC server |

### Configuration (`GRPCConfig`)

```go
type GRPCConfig struct {
    Host        string        `json:"host"`
    Port        int           `json:"port"`
    TLS         bool          `json:"tls"`
    TLSSkipVerify bool       `json:"tls_skip_verify"`
    ServiceName string        `json:"service_name"`
    Timeout     time.Duration `json:"timeout"`
    Keyword     string        `json:"keyword"`
    InvertKeyword bool        `json:"invert_keyword"`
}
```

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `host` | string | **yes** | — | gRPC server hostname or IP |
| `port` | int | no | `50051` | gRPC server port |
| `tls` | bool | no | `false` | Enable TLS for the connection |
| `tls_skip_verify` | bool | no | `false` | Skip TLS certificate verification |
| `service_name` | string | no | `""` | Service name for health check (empty = overall server health) |
| `timeout` | duration | no | `10s` | Connection + RPC timeout |
| `keyword` | string | no | — | Keyword to search for in health check response |
| `invert_keyword` | bool | no | `false` | If true, check that keyword is NOT present |

### Validation Rules

- `host` is required and must be non-empty
- `port` must be between 1 and 65535
- `timeout` must be > 0 and ≤ 60s
- Auto-generate `spec.Name` as `host:port` (+ `/service_name` if set) if empty
- Auto-generate `spec.Slug` as `grpc-{host}` if empty

### Execution Behavior

1. Build target address as `host:port`
2. Create gRPC dial options:
   - If `tls` is true: use `credentials.NewTLS(&tls.Config{InsecureSkipVerify: tlsSkipVerify})`
   - If `tls` is false: use `grpc.WithTransportCredentials(insecure.NewCredentials())`
3. Create context with timeout
4. Record `t0` — dial the gRPC server with `grpc.NewClient(target, opts...)`
5. Record `t1` (connection established) — compute `connection_time_ms`
6. Invoke the standard gRPC health check:
   ```go
   healthClient := healthpb.NewHealthClient(conn)
   resp, err := healthClient.Check(ctx, &healthpb.HealthCheckRequest{
       Service: cfg.ServiceName,
   })
   ```
7. Check response status:
   - `SERVING` → service is healthy
   - `NOT_SERVING` → service is unhealthy
   - `UNKNOWN` → service health unknown
8. If `keyword` is set, check if the response string representation contains the keyword (or doesn't, if `invert_keyword`)
9. Record `t2` (RPC complete) — compute `rpc_time_ms`
10. Return result with metrics

**Status mapping:**

| Condition | Status |
|-----------|--------|
| Health check returns SERVING | `StatusUp` |
| Health check returns NOT_SERVING | `StatusDown` |
| Health check returns UNKNOWN | `StatusDown` |
| Keyword found (or not found when inverted) | `StatusUp` |
| Keyword not found (or found when inverted) | `StatusDown` |
| Connection refused / host unreachable | `StatusDown` |
| TLS handshake failure | `StatusDown` |
| Context deadline exceeded | `StatusTimeout` |
| Service not registered for health checks | `StatusDown` |
| Invalid configuration | `StatusError` |

### Metrics

| Key | Type | Description |
|-----|------|-------------|
| `connection_time_ms` | float64 | Time to establish gRPC connection |
| `rpc_time_ms` | float64 | Time for the health check RPC |
| `total_time_ms` | float64 | Total check duration |

### Output

| Key | Type | Description |
|-----|------|-------------|
| `host` | string | Target hostname |
| `port` | int | Target port |
| `service_name` | string | Checked service name |
| `serving_status` | string | gRPC health status (SERVING, NOT_SERVING, UNKNOWN) |
| `tls` | bool | Whether TLS was used |
| `error` | string | Error message if check failed |

### Go Dependencies

Use the standard gRPC health checking libraries:
- `google.golang.org/grpc` — gRPC client
- `google.golang.org/grpc/health/grpc_health_v1` — standard health check protocol
- `google.golang.org/grpc/credentials` — TLS support
- `google.golang.org/grpc/credentials/insecure` — plaintext support

These are well-maintained, widely used, and part of the official gRPC ecosystem.

### Testing

Use an embedded gRPC server in tests:

```go
func startTestGRPCServer(t *testing.T, healthStatus healthpb.HealthCheckResponse_ServingStatus) (string, int) {
    lis, err := net.Listen("tcp", "127.0.0.1:0")
    require.NoError(t, err)

    s := grpc.NewServer()
    healthServer := health.NewServer()
    healthServer.SetServingStatus("", healthStatus)
    healthServer.SetServingStatus("myservice", healthStatus)
    healthpb.RegisterHealthServer(s, healthServer)

    go func() { _ = s.Serve(lis) }()
    t.Cleanup(s.GracefulStop)

    addr := lis.Addr().(*net.TCPAddr)
    return addr.IP.String(), addr.Port
}
```

**Test cases** (table-driven):
1. **Happy path** — SERVING status, expect `StatusUp`
2. **NOT_SERVING** — expect `StatusDown`, `serving_status: "NOT_SERVING"`
3. **Specific service name** — check named service, expect `StatusUp`
4. **Unknown service** — non-registered service, expect `StatusDown`
5. **Keyword match** — response contains keyword, expect `StatusUp`
6. **Keyword mismatch** — response doesn't contain keyword, expect `StatusDown`
7. **Inverted keyword** — keyword absent = success, expect `StatusUp`
8. **Connection refused** — wrong port, expect `StatusDown`
9. **Timeout** — unreachable host, expect `StatusTimeout`
10. **Missing host** — validation error

### Limitations

- Only supports the standard gRPC health checking protocol (not arbitrary method invocation)
- No support for client certificates (mTLS) in initial implementation
- No protobuf file upload for custom service definitions
- Streaming RPCs not supported
- gRPC reflection not used (server must implement health service)

### Future Enhancements

- Custom unary method invocation with protobuf definitions
- mTLS support with client certificates
- gRPC reflection-based service discovery
- Response body JSON path assertions

---

## Frontend

### Dashboard (`web/dash0/src/components/shared/check-form.tsx`)

#### Type Registration

Add `"grpc"` to `CheckType` union and `checkTypes` array:
```typescript
{ value: "grpc", label: "gRPC", description: "Check gRPC service health" },
```

#### Form Fields

```tsx
case "grpc":
  return (
    <>
      <div className="space-y-2">
        <Label>Host</Label>
        <div className="flex gap-2">
          <Input id="host" type="text" placeholder="grpc.example.com"
            value={host} onChange={(e) => setHost(e.target.value)} className="flex-1" />
          <Input id="port" type="number" placeholder="50051"
            value={port} onChange={(e) => setPort(e.target.value)} className="w-24" />
        </div>
      </div>
      <div className="space-y-2">
        <Label htmlFor="serviceName">Service Name (optional)</Label>
        <Input id="serviceName" type="text" placeholder="myservice"
          value={serviceName} onChange={(e) => setServiceName(e.target.value)} />
        <p className="text-xs text-muted-foreground">
          Leave empty to check overall server health
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
| `server/internal/checkers/checkgrpc/config.go` | New |
| `server/internal/checkers/checkgrpc/checker.go` | New |
| `server/internal/checkers/checkgrpc/errors.go` | New |
| `server/internal/checkers/checkgrpc/samples.go` | New |
| `server/internal/checkers/checkgrpc/checker_test.go` | New |
| `server/internal/checkers/checkerdef/types.go` | Modify |
| `server/internal/checkers/registry/registry.go` | Modify |
| `server/go.mod` | Modify (add `google.golang.org/grpc`) |
| `web/dash0/src/components/shared/check-form.tsx` | Modify |
| `web/dash0/src/locales/en/checks.json` | Modify |
| `web/dash0/src/locales/fr/checks.json` | Modify |

## Verification

- [ ] `make lint` passes
- [ ] `make test` passes (including embedded gRPC server tests)
- [ ] Create a gRPC check via the UI against a test gRPC server
- [ ] Verify SERVING status shows `StatusUp`
- [ ] Verify NOT_SERVING status shows `StatusDown`
- [ ] Verify TLS connections work
- [ ] Verify keyword matching works
- [ ] E2E tests pass

**Status**: Draft | **Created**: 2026-03-28
