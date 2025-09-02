# Node Roles

## Overview

Introduce a node role system (`SP_NODE_ROLE`) to define which functions a running SolidPing binary will perform. This enables horizontal scaling by dedicating nodes to specific workloads.

## Motivation

In a distributed deployment, it's beneficial to separate concerns:
- **API nodes** can be scaled independently based on HTTP traffic
- **Job processing nodes** can be scaled based on queue depth
- **Check nodes** can be distributed across regions for geographic redundancy

## Configuration

### Environment Variable

| Property | Value |
|----------|-------|
| Key | `node_role` |
| Environment Variable | `SP_NODE_ROLE` |
| Default | `all` |
| Secret | `false` |
| Description | Defines which functions the node will perform |

### YAML Configuration

```yaml
node:
  role: "all"  # Options: all, api, jobs, checks
  region: ""   # Required when role is "checks"
```

## Role Definitions

| Role | Description | Components Enabled |
|------|-------------|-------------------|
| `all` | Default mode - runs all components | HTTP server, job processor, check executor |
| `api` | API-only mode | HTTP server only |
| `jobs` | Job processor mode | General job processing (notifications, cleanup, etc.) |
| `checks` | Check executor mode | Health check execution only |

### Component Matrix

| Component | `all` | `api` | `jobs` | `checks` |
|-----------|-------|-------|--------|----------|
| HTTP Server | Yes | Yes | No | No |
| Job Processor | Yes | No | Yes | No |
| Check Executor | Yes | No | No | Yes |
| Database Connection | Yes | Yes | Yes | Yes |

## Region Requirement for Checks Role

When `SP_NODE_ROLE=checks`, the node **must** also have a region configured:

| Property | Value |
|----------|-------|
| Key | `node_region` |
| Environment Variable | `SP_NODE_REGION` |
| Default | `""` (empty) |
| Secret | `false` |
| Description | Geographic region identifier for this node |

### Validation Rules

1. If `role=checks` and `region` is empty → fail startup with error:
   ```
   Error: SP_NODE_REGION is required when SP_NODE_ROLE is set to "checks"
   ```

2. If `role!=checks` and `region` is set → warning but continue:
   ```
   Warning: SP_NODE_REGION is set but will be ignored for role "<role>"
   ```

## Implementation

### 1. Add Node Configuration to Config struct

In `back/internal/config/config.go`:

```go
type NodeConfig struct {
    Role   string `koanf:"role"`
    Region string `koanf:"region"`
}

type Config struct {
    // ...
    Node NodeConfig `koanf:"node"`
}
```

Default:
```go
Node: NodeConfig{
    Role:   "all",
    Region: "",
}
```

### 2. Role Constants

In `back/internal/config/config.go`:

```go
const (
    NodeRoleAll    = "all"
    NodeRoleAPI    = "api"
    NodeRoleJobs   = "jobs"
    NodeRoleChecks = "checks"
)

// ValidNodeRoles returns all valid role values
func ValidNodeRoles() []string {
    return []string{NodeRoleAll, NodeRoleAPI, NodeRoleJobs, NodeRoleChecks}
}
```

### 3. Config Validation

Add validation in config loading:

```go
func (c *Config) Validate() error {
    // Validate role
    validRoles := map[string]bool{
        NodeRoleAll: true, NodeRoleAPI: true,
        NodeRoleJobs: true, NodeRoleChecks: true,
    }
    if !validRoles[c.Node.Role] {
        return fmt.Errorf("invalid node role %q, must be one of: %v",
            c.Node.Role, ValidNodeRoles())
    }

    // Require region for checks role
    if c.Node.Role == NodeRoleChecks && c.Node.Region == "" {
        return fmt.Errorf("SP_NODE_REGION is required when SP_NODE_ROLE is set to %q",
            NodeRoleChecks)
    }

    return nil
}
```

### 4. Helper Methods

Add convenience methods to Config:

```go
func (c *Config) ShouldRunAPI() bool {
    return c.Node.Role == NodeRoleAll || c.Node.Role == NodeRoleAPI
}

func (c *Config) ShouldRunJobs() bool {
    return c.Node.Role == NodeRoleAll || c.Node.Role == NodeRoleJobs
}

func (c *Config) ShouldRunChecks() bool {
    return c.Node.Role == NodeRoleAll || c.Node.Role == NodeRoleChecks
}
```

### 5. Startup Logic Changes

In `back/cmd/serve.go` (or equivalent):

```go
func runServer(cfg *config.Config) error {
    // Always initialize database
    db := initDatabase(cfg)

    var wg sync.WaitGroup

    // Start HTTP server if needed
    if cfg.ShouldRunAPI() {
        wg.Add(1)
        go func() {
            defer wg.Done()
            startHTTPServer(cfg, db)
        }()
    }

    // Start job processor if needed
    if cfg.ShouldRunJobs() {
        wg.Add(1)
        go func() {
            defer wg.Done()
            startJobProcessor(cfg, db)
        }()
    }

    // Start check executor if needed
    if cfg.ShouldRunChecks() {
        wg.Add(1)
        go func() {
            defer wg.Done()
            startCheckExecutor(cfg, db)
        }()
    }

    wg.Wait()
    return nil
}
```

### 6. Register in systemconfig

In `back/internal/systemconfig/systemconfig.go`:

```go
{
    Key:    "node_role",
    EnvVar: "SP_NODE_ROLE",
    Secret: false,
    ApplyFunc: func(cfg *config.Config, value any) {
        if v, ok := value.(string); ok && v != "" {
            cfg.Node.Role = v
        }
    },
},
{
    Key:    "node_region",
    EnvVar: "SP_NODE_REGION",
    Secret: false,
    ApplyFunc: func(cfg *config.Config, value any) {
        if v, ok := value.(string); ok && v != "" {
            cfg.Node.Region = v
        }
    },
},
```

## Deployment Examples

### Single Node (Default)

```bash
# Everything runs on one node
./solidping serve
```

### Horizontal Scaling - Separate API and Workers

```bash
# API nodes (behind load balancer)
SP_NODE_ROLE=api ./solidping serve

# Job processor nodes
SP_NODE_ROLE=jobs ./solidping serve
```

### Geographic Distribution

```bash
# US-East check nodes
SP_NODE_ROLE=checks SP_NODE_REGION=us-east ./solidping serve

# EU-West check nodes
SP_NODE_ROLE=checks SP_NODE_REGION=eu-west ./solidping serve

# Central API and job processing
SP_NODE_ROLE=api ./solidping serve
SP_NODE_ROLE=jobs ./solidping serve
```

### Docker Compose Example

```yaml
version: '3.8'
services:
  api:
    image: solidping:latest
    environment:
      SP_NODE_ROLE: api
    ports:
      - "4000:4000"
    deploy:
      replicas: 2

  jobs:
    image: solidping:latest
    environment:
      SP_NODE_ROLE: jobs
    deploy:
      replicas: 1

  checks-us-east:
    image: solidping:latest
    environment:
      SP_NODE_ROLE: checks
      SP_NODE_REGION: us-east
    deploy:
      replicas: 2

  checks-eu-west:
    image: solidping:latest
    environment:
      SP_NODE_ROLE: checks
      SP_NODE_REGION: eu-west
    deploy:
      replicas: 2
```

## Logging

At startup, log the node configuration:

```
INFO Starting SolidPing node role=all
INFO Starting SolidPing node role=checks region=us-east
```

When components are skipped due to role:

```
INFO Skipping HTTP server (role=jobs)
INFO Skipping job processor (role=api)
INFO Skipping check executor (role=api)
```

## Health Check Endpoint

The `/api/mgmt/health` endpoint should reflect the node's role:

```json
{
  "status": "ok",
  "node": {
    "role": "checks",
    "region": "us-east"
  },
  "components": {
    "database": "ok",
    "check_executor": "ok"
  }
}
```

Note: When `role=jobs` or `role=checks`, the HTTP server is not running, so no health endpoint is available. For these nodes, use process-level health checks (e.g., checking if the process is running).

## Testing

- [ ] Default role is `all` when not specified
- [ ] Role can be set via `SP_NODE_ROLE` environment variable
- [ ] Role can be set via YAML config
- [ ] Invalid role values are rejected at startup
- [ ] `checks` role requires `SP_NODE_REGION` to be set
- [ ] `ShouldRunAPI()` returns correct values for each role
- [ ] `ShouldRunJobs()` returns correct values for each role
- [ ] `ShouldRunChecks()` returns correct values for each role
- [ ] HTTP server only starts when role is `all` or `api`
- [ ] Job processor only starts when role is `all` or `jobs`
- [ ] Check executor only starts when role is `all` or `checks`

## Future Considerations

- **Combined roles**: Consider allowing `SP_NODE_ROLE=api,jobs` to run multiple roles without checks
- **Role-specific configuration**: Different database pool sizes based on role
- **Metrics labels**: Include role and region in Prometheus metrics for filtering
