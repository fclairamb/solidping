# Organization Check Rate Limiting

## Overview

SolidPing currently has no limit on how many checks an organization can run or how frequently they execute. As the platform grows, some organizations may consume disproportionate worker capacity by creating many high-frequency checks. This spec introduces a **checks-per-minute rate limit** per organization — a quota set exclusively by super admins that caps the total check execution rate for an org.

When an org's total desired rate exceeds its limit, all check_job periods are **proportionally scaled up** so the total effective rate equals the limit. The relative priority between checks is preserved: a check configured at 30s stays 2× faster than one at 60s, even after scaling.

**Design decisions:**
- **Proportional fair scaling** — simplest algorithm that preserves relative check priorities; no priority tiers or weighting needed initially
- **Super admin only** — org admins see the effect but cannot change the limit; this is an operational/billing lever
- **No schema migration** — uses the existing `parameters` table for storage
- **Desired vs effective period** — `checks.period` remains the user-configured value; `check_jobs.period` becomes the effective (possibly scaled) value
- **Idempotent rebalancing** — a single `rebalanceOrgCheckJobs` function can be called from any trigger point

---

## Algorithm: Proportional Fair Scaling

### Definitions

| Symbol | Meaning |
|--------|---------|
| `N` | Number of enabled checks in the org |
| `P_i` | Desired period of check `i` (as configured by the user) |
| `r_i` | Desired rate of check `i` = `60 / P_i` (checks/minute, where `P_i` is in seconds) |
| `R` | Total desired rate = `Σ r_i` |
| `L` | Org rate limit (checks/minute), from `limits.checks_per_minute` parameter |
| `α` | Scale factor = `R / L` (always ≥ 1 when applied) |

### Logic

```
if L is NULL or L ≤ 0:
    → no limit, all jobs use desired periods (α = 1)

R = Σ (60 / P_i) for all enabled checks

if R ≤ L:
    → within budget, all jobs use desired periods (α = 1)

if R > L:
    α = R / L
    for each check i:
        effective_period_i = ceil_to_second(P_i × α)
```

### Properties

- **Preserves relative priority**: `r_i' / r_j' = r_i / r_j` — a check that was 2× faster stays 2× faster
- **Periods only increase**: `α ≥ 1` means no check ever runs faster than its desired period
- **Minimum period respected**: since desired periods are already ≥ 5s and scaling only increases them
- **Quantization**: periods are rounded up to the nearest whole second after scaling to avoid sub-second drift; this means actual total rate may be slightly below `L`, which is safe

### Multi-region interaction

A check with `N_regions` regions creates `N_regions` jobs, each with period `P × N_regions`, staggered so the effective rate is `1/P`. The rate contribution is always `60/P_desired` regardless of region count.

When scaling is applied:

```
job_period = ceil_to_second(P_desired × α) × N_regions
```

The scale factor applies to the base period before region multiplication.

### Worked example

Org rate limit: **2 checks/minute**

| Check | Desired period | Desired rate (checks/min) |
|-------|---------------|--------------------------|
| A | 30s | 2.0 |
| B | 60s | 1.0 |
| C | 120s | 0.5 |

- Total desired rate `R = 3.5 checks/min`
- Limit `L = 2 checks/min`
- Scale factor `α = 3.5 / 2 = 1.75`

| Check | Desired period | Scaled period | Effective rate |
|-------|---------------|---------------|----------------|
| A | 30s | ceil(30 × 1.75) = 53s | 1.132 |
| B | 60s | ceil(60 × 1.75) = 105s | 0.571 |
| C | 120s | ceil(120 × 1.75) = 210s | 0.286 |

- Total effective rate = **1.989 checks/min** (≤ 2.0 ✓, slightly under due to ceiling)
- Relative priority preserved: A is ~2× faster than B, B is ~2× faster than C ✓

### Adding a check to a rate-limited org

Continuing the example, a new check D with period 60s is created:

- New total desired rate = `3.5 + 1.0 = 4.5`
- New `α = 4.5 / 2 = 2.25`
- All jobs are rebalanced:

| Check | Desired period | New scaled period | New effective rate |
|-------|---------------|-------------------|-------------------|
| A | 30s | 68s | 0.882 |
| B | 60s | 135s | 0.444 |
| C | 120s | 270s | 0.222 |
| D | 60s | 135s | 0.444 |

- Total = **1.993 checks/min** ✓

### Removing a check relaxes the remaining checks

If check D is deleted, `α` drops back to 1.75 and all remaining jobs return to the periods in the first table.

If check C is also deleted, `R = 3.0`, `α = 3.0/2 = 1.5`:

| Check | Desired period | Scaled period | Effective rate |
|-------|---------------|---------------|----------------|
| A | 30s | 45s | 1.333 |
| B | 60s | 90s | 0.667 |

- Total = **2.0 checks/min** ✓

---

## Data Model

### Storage

Uses the existing `parameters` table — no migration needed.

| Field | Value |
|-------|-------|
| `organization_uid` | The org's UID |
| `key` | `limits.checks_per_minute` |
| `value` | `{"value": 2.0}` (JSONMap) |
| `secret` | `false` |

### Separation of desired vs effective period

- `checks.period` — the user-configured desired period. Never modified by rate limiting.
- `check_jobs.period` — the effective period, computed as `ceil_to_second(desired × α) × num_regions`. Updated by `rebalanceOrgCheckJobs`.

---

## API

### Super admin endpoints

All under `RequireSuperAdmin` middleware.

#### Set rate limit

```
PUT /api/v1/system/orgs/:org/limits/checks-per-minute
```

Request:
```json
{"value": 2.0}
```

Response `200`:
```json
{
  "value": 2.0,
  "currentRate": 3.5,
  "scaleFactor": 1.75,
  "rateLimited": true
}
```

Validation:
- `value` must be a positive number (> 0)
- Setting triggers `rebalanceOrgCheckJobs`

#### Get rate limit

```
GET /api/v1/system/orgs/:org/limits/checks-per-minute
```

Response `200` (limit set):
```json
{
  "value": 2.0,
  "currentRate": 3.5,
  "scaleFactor": 1.75,
  "rateLimited": true
}
```

Response `200` (no limit):
```json
{
  "value": null,
  "currentRate": 3.5,
  "scaleFactor": 1.0,
  "rateLimited": false
}
```

#### Remove rate limit

```
DELETE /api/v1/system/orgs/:org/limits/checks-per-minute
```

Response `204`. Triggers `rebalanceOrgCheckJobs` to restore all jobs to desired periods.

### Check response changes

Add two fields to `CheckResponse`:

```go
type CheckResponse struct {
    // ... existing fields ...
    EffectivePeriod *string `json:"effectivePeriod,omitempty"` // only set when different from period
    RateLimited     bool    `json:"rateLimited,omitempty"`     // true when org rate limit is active
}
```

- `effectivePeriod` is only included when it differs from `period` (i.e., when rate limiting is active)
- `rateLimited` indicates whether the org's rate limit is currently being enforced

---

## Implementation

### Core function: `rebalanceOrgCheckJobs`

```go
func (s *Service) rebalanceOrgCheckJobs(ctx context.Context, orgUID string) error {
    // 1. Fetch the org rate limit
    limit, err := s.getOrgRateLimit(ctx, orgUID) // returns 0 if not set

    // 2. Fetch all enabled checks for the org
    checks, err := s.db.ListEnabledChecksByOrg(ctx, orgUID)

    // 3. Compute total desired rate
    totalRate := 0.0
    for _, c := range checks {
        totalRate += 60.0 / c.Period.Seconds()
    }

    // 4. Compute scale factor
    alpha := 1.0
    if limit > 0 && totalRate > limit {
        alpha = totalRate / limit
    }

    // 5. Reconcile all check jobs with the scale factor
    for _, c := range checks {
        if err := s.reconcileCheckJobsWithScale(ctx, &c, alpha); err != nil {
            return err
        }
    }
    return nil
}
```

### Refactoring `reconcileCheckJobs`

The existing `reconcileCheckJobs` becomes a thin wrapper:

```go
func (s *Service) reconcileCheckJobs(ctx context.Context, check *models.Check) error {
    return s.reconcileCheckJobsWithScale(ctx, check, 1.0)
}
```

`reconcileCheckJobsWithScale` is the existing `reconcileCheckJobs` logic with one change: `basePeriod = ceilToSecond(check.Period × alpha)` instead of `basePeriod = check.Period`.

### Period quantization helper

```go
func ceilToSecond(d time.Duration) time.Duration {
    if d%time.Second == 0 {
        return d
    }
    return d.Truncate(time.Second) + time.Second
}
```

### Rebalancing triggers

| Trigger | Location | What to do |
|---------|----------|------------|
| Check created | `Service.CreateCheck` | After DB insert + initial job creation, call `rebalanceOrgCheckJobs` |
| Check updated (period/enabled/regions) | `Service.UpdateCheck` | Replace single-check `reconcileCheckJobs` with `rebalanceOrgCheckJobs` |
| Check deleted | `Service.DeleteCheck` | After deleting jobs + check, call `rebalanceOrgCheckJobs` to relax remaining |
| Rate limit changed | `RateLimitHandler.Set/Delete` | Call `rebalanceOrgCheckJobs` after parameter write |

### Scale factor for API responses

To populate `effectivePeriod` and `rateLimited` on check responses without N+1 queries:

```go
func (s *Service) getOrgScaleFactor(ctx context.Context, orgUID string) (float64, error) {
    limit, err := s.getOrgRateLimit(ctx, orgUID)
    if err != nil || limit <= 0 {
        return 1.0, err
    }
    checks, err := s.db.ListEnabledChecksByOrg(ctx, orgUID)
    if err != nil {
        return 1.0, err
    }
    totalRate := 0.0
    for _, c := range checks {
        totalRate += 60.0 / c.Period.Seconds()
    }
    if totalRate <= limit {
        return 1.0, nil
    }
    return totalRate / limit, nil
}
```

Called once per `ListChecks`/`GetCheck` request, then applied to each response item.

### Route registration

In `server.go`, after existing system routes:

```go
orgLimits := systemGroup.NewGroup("/orgs/:org/limits")
orgLimits.PUT("/checks-per-minute", rateLimitHandler.SetChecksPerMinute)
orgLimits.GET("/checks-per-minute", rateLimitHandler.GetChecksPerMinute)
orgLimits.DELETE("/checks-per-minute", rateLimitHandler.DeleteChecksPerMinute)
```

### New files

- `server/internal/handlers/ratelimits/handler.go` — HTTP handler
- `server/internal/handlers/ratelimits/service.go` — business logic (calls parameter DB ops + rebalance)

### Modified files

- `server/internal/handlers/checks/service.go` — `rebalanceOrgCheckJobs`, `reconcileCheckJobsWithScale`, `getOrgScaleFactor`, response field population
- `server/internal/app/server.go` — route registration

---

## Edge Cases

| Scenario | Behavior |
|----------|----------|
| No limit set for org | All checks run at desired periods. `α = 1`. |
| Limit set but total rate is within budget | Same as no limit — no scaling applied. |
| Single check exceeds limit | That one check is slowed down. E.g., check with period=10s (6/min) and limit=2/min → α=3, effective period=30s. |
| All checks disabled | Total rate = 0, no jobs exist, no scaling needed. |
| Limit set to very small value | All periods scale up proportionally. A limit of 0.1/min with a 30s check → α=12, period=360s. |
| Limit removed | `rebalanceOrgCheckJobs` runs with α=1, restoring all desired periods. |
| Concurrent check creation | Two simultaneous creates may compute slightly stale α. Last rebalance wins and is correct. Acceptable for initial implementation. |
| Check with multiple regions | Rate contribution is `60/P_desired` (not per-region). Scale applies before region multiplication. |

---

## Testing

### Unit tests

```go
// TestCeilToSecond — exact seconds unchanged, sub-second rounds up
// TestGetOrgScaleFactor — no limit → 1.0; within budget → 1.0; over budget → correct α
// TestRebalanceOrgCheckJobs — 3 checks, limit set, verify all job periods scaled correctly
// TestRebalanceNoLimit — verify jobs use desired periods when no limit
// TestRebalanceWithMultiRegion — verify scale + region multiplication
// TestRebalanceOnCheckCreate — create check in rate-limited org, all jobs rebalanced
// TestRebalanceOnCheckDelete — delete check, remaining jobs relax toward desired
// TestRebalanceOnCheckDisable — disable check, remaining jobs relax
```

### Integration tests

```go
// TestRateLimitCRUD — super admin sets/gets/deletes limit via API
// TestRateLimitForbiddenForOrgAdmin — non-super-admin gets 403
// TestEffectivePeriodInCheckResponse — create checks, set limit, verify effectivePeriod in list
// TestRebalanceE2E — create 3 checks, set limit below total, verify job periods in DB
```

### Manual verification

```bash
# Login as super admin
TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"email":"admin@solidping.com","password":"solidpass"}' \
  'http://localhost:4000/api/v1/auth/login' | jq -r '.accessToken')

# Create some checks with short periods
# (use existing check creation API)

# Set rate limit to 2 checks/minute
curl -s -X PUT -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"value": 2.0}' \
  'http://localhost:4000/api/v1/system/orgs/default/limits/checks-per-minute' | jq .

# Verify checks show effective periods
curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:4000/api/v1/orgs/default/checks' | jq '.data[] | {slug, period, effectivePeriod, rateLimited}'

# Remove limit and verify periods return to normal
curl -s -X DELETE -H "Authorization: Bearer $TOKEN" \
  'http://localhost:4000/api/v1/system/orgs/default/limits/checks-per-minute'
```

---

**Status**: Backlog | **Created**: 2026-03-30
