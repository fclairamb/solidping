# Docker Container Monitoring

## Overview

Add a Docker container health check that monitors whether a specific container is running, healthy, and responsive. This goes beyond a simple process check — it verifies container state via the Docker Engine API, including health check status if configured on the container.

**Use cases:**
- Verify a container is running and not stuck in a restart loop
- Check Docker health check status (healthy/unhealthy) for containers with `HEALTHCHECK` directives
- Monitor container resource metrics (CPU, memory usage)
- Detect containers that silently exit or enter a "created" but not "running" state
- Self-hosted monitoring of Docker Compose or standalone container stacks

## Check Type
Type: `docker`

---

## Backend

### Package: `server/internal/checkers/checkdocker/`

| File | Description |
|------|-------------|
| `config.go` | `DockerConfig` struct with `FromMap()` / `GetConfig()` |
| `checker.go` | `DockerChecker` implementing `Checker` interface |
| `errors.go` | Package-level error sentinel values |
| `samples.go` | `GetSampleConfigs()` with example configurations |
| `checker_test.go` | Table-driven tests with testcontainers |

### Configuration (`DockerConfig`)

```go
type DockerConfig struct {
    Host          string        `json:"host"`
    ContainerName string        `json:"container_name"`
    ContainerID   string        `json:"container_id"`
    Timeout       time.Duration `json:"timeout"`
}
```

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `host` | string | no | `unix:///var/run/docker.sock` | Docker daemon socket or TCP endpoint (`tcp://host:2376`) |
| `container_name` | string | one of name/id | — | Container name to monitor (supports regex prefix match) |
| `container_id` | string | one of name/id | — | Container ID to monitor (short or full SHA) |
| `timeout` | duration | no | `10s` | API call timeout |

### Validation Rules

- At least one of `container_name` or `container_id` must be provided
- `host` if provided must start with `unix://` or `tcp://`
- `timeout` must be > 0 and ≤ 60s
- Auto-generate `spec.Name` from container name/ID if empty
- Auto-generate `spec.Slug` as `docker-{container_name}` if empty

### Execution Behavior

1. Create Docker client with configured host (default: local socket)
2. Create context with timeout
3. Record `t0`
4. Lookup container by name or ID using `client.ContainerInspect(ctx, nameOrID)`
5. Check `State.Running` — if false, return `StatusDown`
6. If container has a health check, inspect `State.Health.Status`:
   - `"healthy"` → continue
   - `"unhealthy"` → return `StatusDown` with last health check log
   - `"starting"` → return `StatusUp` (newly started, not yet checked)
7. Collect `State.StartedAt`, restart count, CPU/memory stats
8. Record `t1` — compute `inspect_time_ms`
9. Return `StatusUp` with metrics

**Status mapping:**

| Condition | Status |
|-----------|--------|
| Container running + healthy (or no health check) | `StatusUp` |
| Container not found | `StatusDown` |
| Container exited / paused / dead | `StatusDown` |
| Container health check reports "unhealthy" | `StatusDown` |
| Docker daemon unreachable | `StatusDown` |
| Context deadline exceeded | `StatusTimeout` |
| Invalid configuration | `StatusError` |

### Metrics

| Key | Type | Description |
|-----|------|-------------|
| `inspect_time_ms` | float64 | Time to inspect container state |
| `restart_count` | int | Container restart count |
| `uptime_seconds` | float64 | Seconds since container started |

### Output

| Key | Type | Description |
|-----|------|-------------|
| `container_name` | string | Container name |
| `container_id` | string | Container ID (short) |
| `state` | string | Container state (running, exited, paused, etc.) |
| `health_status` | string | Health check status if available (healthy, unhealthy, starting) |
| `health_log` | string | Last health check log entry (if unhealthy) |
| `image` | string | Container image name |
| `started_at` | string | Container start timestamp |
| `error` | string | Error message if check failed |

### Go Driver

Use `github.com/docker/docker/client` (official Docker SDK for Go). Add to `server/go.mod`.

```go
import (
    "github.com/docker/docker/client"
    "github.com/docker/docker/api/types/container"
)

func newDockerClient(host string) (*client.Client, error) {
    opts := []client.Opt{client.FromEnv, client.WithAPIVersionNegotiation()}
    if host != "" {
        opts = append(opts, client.WithHost(host))
    }
    return client.NewClientWithOpts(opts...)
}
```

### Security Considerations

- Docker socket access grants root-equivalent privileges — document this clearly
- For TCP connections, recommend TLS with client certificates
- Never expose Docker socket to untrusted check workers
- Read-only operations only (inspect, stats) — never start/stop/exec

### Testing

Use **testcontainers** to spin up a Docker-in-Docker or test against the local Docker socket:

**Test cases** (table-driven):
1. **Happy path** — running container, expect `StatusUp`
2. **Container with health check (healthy)** — expect `StatusUp`, `health_status: "healthy"`
3. **Container with health check (unhealthy)** — expect `StatusDown`, `health_status: "unhealthy"`
4. **Stopped container** — expect `StatusDown`, `state: "exited"`
5. **Non-existent container** — expect `StatusDown`, error contains "not found"
6. **Lookup by name** — verify name-based lookup works
7. **Lookup by ID** — verify ID-based lookup works
8. **Unreachable daemon** — wrong host, expect `StatusDown`
9. **Missing container_name and container_id** — validation error

### Limitations

- Requires access to Docker socket or Docker TCP API
- Cannot monitor containers on remote hosts without Docker TLS setup
- Resource metrics (CPU/memory) require a separate `ContainerStats` call which adds latency
- Docker Swarm services not supported (only standalone containers)
- Podman compatibility untested but likely works via Docker-compatible API

---

## Frontend

### Dashboard (`web/dash0/src/components/shared/check-form.tsx`)

#### Type Registration

Add `"docker"` to `CheckType` union and `checkTypes` array:
```typescript
{ value: "docker", label: "Docker", description: "Monitor Docker container health" },
```

#### Form Fields

```tsx
case "docker":
  return (
    <>
      <div className="space-y-2">
        <Label htmlFor="host">Docker Host (optional)</Label>
        <Input
          id="host"
          type="text"
          placeholder="unix:///var/run/docker.sock"
          value={host}
          onChange={(e) => setHost(e.target.value)}
        />
        <p className="text-xs text-muted-foreground">
          Leave empty for local Docker socket. Use tcp://host:2376 for remote.
        </p>
      </div>
      <div className="space-y-2">
        <Label htmlFor="containerName">Container Name or ID</Label>
        <Input
          id="containerName"
          type="text"
          placeholder="my-app or abc123def"
          value={containerName}
          onChange={(e) => setContainerName(e.target.value)}
        />
      </div>
    </>
  );
```

#### New State Variable

```typescript
const [containerName, setContainerName] = useState(
  getConfigField(initialData?.config, "container_name") ||
  getConfigField(initialData?.config, "container_id")
);
```

---

## Key Files

| File | Action |
|------|--------|
| `server/internal/checkers/checkdocker/config.go` | New |
| `server/internal/checkers/checkdocker/checker.go` | New |
| `server/internal/checkers/checkdocker/errors.go` | New |
| `server/internal/checkers/checkdocker/samples.go` | New |
| `server/internal/checkers/checkdocker/checker_test.go` | New |
| `server/internal/checkers/checkerdef/types.go` | Modify |
| `server/internal/checkers/registry/registry.go` | Modify |
| `server/go.mod` | Modify (add `github.com/docker/docker`) |
| `web/dash0/src/components/shared/check-form.tsx` | Modify |
| `web/dash0/src/locales/en/checks.json` | Modify |
| `web/dash0/src/locales/fr/checks.json` | Modify |

## Verification

- [ ] `make lint` passes
- [ ] `make test` passes (including Docker-based tests)
- [ ] Create a Docker check via the UI against a local container
- [ ] Verify running container shows `StatusUp` with restart count and uptime
- [ ] Verify stopped container shows `StatusDown`
- [ ] Verify container with failing health check shows `StatusDown` + health log
- [ ] Verify non-existent container shows `StatusDown` with clear error
- [ ] E2E tests pass

**Status**: Draft | **Created**: 2026-03-28
