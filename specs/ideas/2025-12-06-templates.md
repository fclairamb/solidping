# Check Templates / Matrix Expansion

## Problem Statement

We want to define one check that expands across multiple configurations:
- HTTP ping check
- On apps: app1, app2, app3
- On envs: dev, test, staging, prod
- URL pattern: `https://api.comp-${env}.com/${app}/`

This would generate 12 checks (3 apps × 4 envs) from a single definition.

## Proposed Solution: Matrix Field on Checks

Extend the existing `checks` table with a `matrix` field that defines variable expansions.

### Schema Changes

```sql
-- Add matrix field to checks table
alter table checks add column matrix jsonb;

comment on column checks.matrix is 'Matrix of variables for expanding a single check into multiple jobs. Each key defines a variable, each value is an array of possible values.';
```

### How It Works

1. **Check Definition with Matrix**:
```json
{
  "name": "API Health Check",
  "code": "api_health",
  "type": "http",
  "config": {
    "url": "https://api.comp-${env}.com/${app}/",
    "method": "GET",
    "expected_status": 200
  },
  "matrix": {
    "app": ["app1", "app2", "app3"],
    "env": ["dev", "test", "staging", "prod"]
  }
}
```

2. **Job Expansion**: When a check with a matrix is enabled, the system creates one `check_job` per combination:

| check_job | Variables | Resolved URL |
|-----------|-----------|--------------|
| job_1 | `{app: "app1", env: "dev"}` | `https://api.comp-dev.com/app1/` |
| job_2 | `{app: "app1", env: "test"}` | `https://api.comp-test.com/app1/` |
| job_3 | `{app: "app1", env: "staging"}` | `https://api.comp-staging.com/app1/` |
| ... | ... | ... |
| job_12 | `{app: "app3", env: "prod"}` | `https://api.comp-prod.com/app3/` |

3. **Check Jobs Schema**: Add a field to store the resolved variables:

```sql
alter table check_jobs add column matrix_values jsonb;

comment on column check_jobs.matrix_values is 'The specific variable values for this job instance from the parent check matrix.';
```

### Data Flow

```
┌─────────────────────────────────────────────────────────┐
│ checks                                                  │
│ ┌─────────────────────────────────────────────────────┐ │
│ │ code: "api_health"                                  │ │
│ │ config: {url: "https://api.comp-${env}.com/${app}/"│ │
│ │ matrix: {app: [...], env: [...]}                   │ │
│ └─────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────┘
                          │
                          │ expands to (on enable/update)
                          ▼
┌─────────────────────────────────────────────────────────┐
│ check_jobs (one per matrix combination)                 │
│ ┌─────────────────┐ ┌─────────────────┐                │
│ │ matrix_values:  │ │ matrix_values:  │  ...           │
│ │ {app: "app1",   │ │ {app: "app1",   │                │
│ │  env: "dev"}    │ │  env: "test"}   │                │
│ └─────────────────┘ └─────────────────┘                │
└─────────────────────────────────────────────────────────┘
                          │
                          │ workers resolve variables at execution
                          ▼
┌─────────────────────────────────────────────────────────┐
│ results                                                 │
│ ┌─────────────────┐ ┌─────────────────┐                │
│ │ context:        │ │ context:        │  ...           │
│ │ {app: "app1",   │ │ {app: "app1",   │                │
│ │  env: "dev"}    │ │  env: "test"}   │                │
│ └─────────────────┘ └─────────────────┘                │
└─────────────────────────────────────────────────────────┘
```

### Variable Resolution

Variables in check config are resolved at job execution time:

```go
func resolveConfig(config map[string]any, matrixValues map[string]string) map[string]any {
    // Deep copy and replace ${var} patterns with values from matrixValues
    // e.g., "https://api.comp-${env}.com/${app}/"
    //    -> "https://api.comp-dev.com/app1/"
}
```

### API Design

**Create check with matrix:**
```http
POST /api/v1/orgs/$org/checks
{
  "code": "api_health",
  "type": "http",
  "config": {
    "url": "https://api.comp-${env}.com/${app}/",
    "method": "GET"
  },
  "matrix": {
    "app": ["app1", "app2", "app3"],
    "env": ["dev", "test", "staging", "prod"]
  }
}
```

**Response includes expansion count:**
```json
{
  "data": {
    "uid": "...",
    "code": "api_health",
    "matrix": {...},
    "matrix_job_count": 12
  }
}
```

**List results with matrix filtering:**
```http
GET /api/v1/orgs/$org/results?check_uid=$uid&matrix.app=app1&matrix.env=dev
```

### Combining with context_conditions

Matrix expansion works alongside `context_conditions` for worker routing:

```json
{
  "code": "global_api_check",
  "config": {
    "url": "https://api.comp-${env}.com/${app}/"
  },
  "matrix": {
    "app": ["app1", "app2"],
    "env": ["dev", "prod"]
  }
}
```

Each expanded job can additionally have `context_conditions` to run from specific workers/regions:

```sql
-- check_jobs for the above would have:
-- job 1: matrix_values={app: "app1", env: "dev"}, context_conditions={region: "eu"}
-- job 2: matrix_values={app: "app1", env: "dev"}, context_conditions={region: "us"}
-- ... (if we want multi-region checks)
```

### Implementation Steps

1. Add `matrix` column to `checks` table
2. Add `matrix_values` column to `check_jobs` table
3. Update check service to expand matrix into jobs on create/update
4. Update worker to resolve variables before executing checks
5. Store matrix values in results.context for filtering/display
6. Update results API to support matrix filtering

### Alternative Considered: Separate Templates Table

A `check_templates` table was considered but rejected because:
- Adds complexity with another entity type
- Current `checks` table already supports JSONB config
- Matrix field is a natural extension of the existing model
- Keeps the 1-check-to-many-jobs relationship already implied by the schema

### Edge Cases

- **Empty matrix**: Treated as a regular check (single job)
- **Matrix update**: Reconcile jobs (add new combinations, remove old ones)
- **Variable not in matrix**: Left as literal string `${var}`
- **Nested variables**: Not supported in v1 (e.g., `${${level}}`)
