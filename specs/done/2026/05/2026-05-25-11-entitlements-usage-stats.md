# Entitlements Usage Stats (checks count, checks/min) + maxChecks enforcement

> Extends the existing `GET /api/v1/orgs/:org/entitlements` endpoint with opt-in usage
> reporting and re-introduces a `maxChecks` limit with creation-time enforcement.

## Context

The entitlements system stores two limits today (`maxSsoUsers`, `maxChecksPerMinute`)
but computes **no usage** — there is no place in the codebase where current check count
or aggregate checks-per-minute is queried or returned. The dashboard has zero entitlements
code and no quota-vs-usage surface.

Key anchors:

- `EntitlementLimits` struct — `server/internal/db/models/entitlements_payload.go:27-30`. A code comment
  explicitly states `maxChecks` from prior versions is silently dropped. This spec re-introduces it.
- Limits live inside the `org_entitlements.payload` JSONB (version 1). Adding an optional field is
  backward-compatible; **no DB migration is needed**.
- `GET` handler returns flat `Resolved` + `upgradeUrl`, not `{data}`-wrapped —
  `server/internal/handlers/entitlements/handler.go:105-126`.
- PUT/PATCH use `DisallowUnknownFields` and today reject `maxChecks` (handler.go:149-158); the comment
  at line 153-154 names it a deprecated key. That changes.
- `overlayLimits` (handler.go:265-272) and `merge` (service.go:257-263) only handle the two current fields.
- `QuotaError`, `ErrEntitlementExceeded`, and `FormatQuotaError` already exist and are designed for
  exactly this use — `entitlements/service.go:17-41` and `handlers/entitlements/handler.go:396-405`. The
  comment on `FormatQuotaError` reads: "Used by the future enforcement-PR handlers."
- Check creation funnels through `checks.Service.CreateCheck` (service.go:758). **Clone calls
  `s.db.CreateCheck` directly** (service.go:2471) — it bypasses the service method, so the quota guard
  must be added to the clone path explicitly.
- `checks.NewService` (service.go:213) has no entitlements dependency; constructed at server.go:480.
  The entitlements service is available there as `s.services.Entitlements`.
- `Check.Period` is `timeutils.Duration` stored as a SQL interval (Postgres) / `HH:MM:SS` text (SQLite)
  — a SQL-side `SUM(60/period)` is not portable. Rate is computed in Go.

## Goals

1. `GET /api/v1/orgs/:org/entitlements?with=usage` returns a `usage` block
   (`checks`, `checksPerMinute`, `ssoUsers`). Without the param, no usage is computed.
2. Re-introduce `maxChecks` in `EntitlementLimits`, accepted by PUT/PATCH and returned in `limits`.
3. Enforce `maxChecks` at check creation: return 402 `QUOTA_EXCEEDED` when at or over cap.
   Internal/system checks are exempt.
4. Frontend: `useEntitlements(org, { withUsage? })` hook + an `organization.usage` route showing
   limits-vs-usage bars with an upgrade link.

## Non-goals

- Distributed/shared-store rework of the per-minute token bucket (process-local, follow-up per
  existing service.go:43-47 comment).
- Retention windows, feature flags, plan SKUs — those stay in the external billing service.
- SSO seat enforcement changes: `ssoUsers` is reported but existing `CheckSSOMembership` logic is
  untouched.
- Enforcement of `maxChecksPerMinute` at creation (already enforced at worker dispatch).

## Approach

### 1. Model — add `maxChecks` (`entitlements_payload.go`)

```go
// EntitlementLimits is the quantitative half of an entitlement set.
// nil = unlimited. JSON tags are the wire format consumed by the API.
type EntitlementLimits struct {
    MaxChecks          *int `json:"maxChecks,omitempty"`
    MaxSSOUsers        *int `json:"maxSsoUsers,omitempty"`
    MaxChecksPerMinute *int `json:"maxChecksPerMinute,omitempty"`
}
```

Remove (or rewrite) the comment at lines 24-26 that says extra fields including `maxChecks` are
silently dropped — `maxChecks` is now modeled. Adding an optional field to the v1 JSONB payload
is backward-compatible; absent keys unmarshal to `nil` (= unlimited). No version bump.

### 2. Merge / overlay — update two sites

**`overlayLimits`** (handler.go:265-272) — add:
```go
if src.MaxChecks != nil {
    dst.MaxChecks = src.MaxChecks
}
```

**`merge`** in entitlements service (service.go:257-263) — add:
```go
if limits.MaxChecks != nil {
    out.Limits.MaxChecks = limits.MaxChecks
}
```

Remove the reference to `maxChecks` as a rejected deprecated key at handler.go:153-154 (it is now
accepted; `DisallowUnknownFields` will no longer reject it).

### 3. Usage computation — new DB method + entitlements service method

**New model** `CheckRate` in `server/internal/db/models/check.go` (near the `Check` struct):
```go
type CheckRate struct {
    Enabled bool
    Period  timeutils.Duration
}
```

**New `db.Service` method** (interface `server/internal/db/service.go`):
```go
// ListOrgCheckRates returns (enabled, period) for all non-deleted, non-internal
// checks of the given org. Used to compute usage stats.
ListOrgCheckRates(ctx context.Context, orgUID string) ([]models.CheckRate, error)
```

Implement in both `postgres/postgres.go` and `sqlite/sqlite.go`: a thin SELECT of `enabled`,
`period` from `checks` where `organization_uid = ?` AND `deleted_at IS NULL` AND `internal = false`.
No arithmetic in SQL. Internal checks are excluded so system-created checks (discovery hosts,
heartbeats) do not consume the user's quota.

> **Known nuance**: the worker's rate bucket (`ReserveCheckExecution`) counts *all* executions
> including internal ones — so `usage.checksPerMinute` will be slightly lower than the effective
> consumed rate when internal checks are present. Document this in the usage struct godoc.

**New `Usage` struct** in `server/internal/entitlements/` (e.g., `usage.go` or `defaults.go`):
```go
// Usage is the org's current resource consumption, computed on demand.
// checksPerMinute is sum(60s/period) over enabled non-internal checks;
// it excludes internal checks and therefore may be lower than the effective
// worker-dispatch rate when the org has system-created checks.
type Usage struct {
    Checks          int     `json:"checks"`
    ChecksPerMinute float64 `json:"checksPerMinute"`
    SSOUsers        int     `json:"ssoUsers"`
}
```

**New `Service.Usage`** in `entitlements/service.go`:
```go
func (s *Service) Usage(ctx context.Context, orgUID string) (Usage, error) {
    rates, err := s.db.ListOrgCheckRates(ctx, orgUID)
    if err != nil {
        return Usage{}, fmt.Errorf("list check rates: %w", err)
    }
    var perMin float64
    for _, r := range rates {
        if r.Enabled {
            perMin += float64(time.Minute) / float64(time.Duration(r.Period))
        }
    }
    ssoUsers, err := s.db.CountSSOMembersForOrg(ctx, orgUID)
    if err != nil {
        return Usage{}, fmt.Errorf("count sso members: %w", err)
    }
    return Usage{Checks: len(rates), ChecksPerMinute: perMin, SSOUsers: ssoUsers}, nil
}
```

### 4. GET handler — parse `?with=usage` (`handlers/entitlements/handler.go`)

Parse `with` as a comma-separated string (consistent with `?with=last_result,last_status_change`
used by the checks endpoint):
```go
withUsage := strings.Contains(req.URL.Query().Get("with"), "usage")
```

Update the response struct:
```go
return h.WriteJSON(writer, http.StatusOK, struct {
    entcore.Resolved
    Usage      *entcore.Usage `json:"usage,omitempty"`
    UpgradeURL string         `json:"upgradeUrl,omitempty"`
}{Resolved: resolved, Usage: usagePtr, UpgradeURL: upgradeURL})
```

Where `usagePtr` is `nil` when `!withUsage` (no usage computed, cheap path unchanged), or a
pointer to the computed `Usage` value.

**Wire shape with `?with=usage`:**
```json
{
  "limits": {
    "maxChecks": 100,
    "maxChecksPerMinute": 6,
    "maxSsoUsers": 30
  },
  "usage": {
    "checks": 42,
    "checksPerMinute": 12.5,
    "ssoUsers": 3
  },
  "source": "default",
  "stale": false
}
```

### 5. Enforcement at check creation

**New `Service.CheckCreateAllowed`** in `entitlements/service.go` (mirrors `CheckSSOMembership`):
```go
func (s *Service) CheckCreateAllowed(ctx context.Context, orgUID string) error {
    resolved, err := s.Resolve(ctx, orgUID)
    if err != nil {
        return fmt.Errorf("resolve entitlements: %w", err)
    }
    if resolved.Limits.MaxChecks == nil {
        return nil // unlimited
    }
    limit := *resolved.Limits.MaxChecks
    rates, err := s.db.ListOrgCheckRates(ctx, orgUID)
    if err != nil {
        return fmt.Errorf("count checks: %w", err)
    }
    if len(rates) >= limit {
        return &QuotaError{
            LimitName:    "MaxChecks",
            Limit:        limit,
            CurrentUsage: len(rates),
        }
    }
    return nil
}
```

**Inject entitlements into checks service** (`handlers/checks/service.go`):
```go
type Service struct {
    db            db.Service
    eventNotifier notifier.EventNotifier
    regions       *regions.Service
    creds         credentials.Service
    entitlements  *entcore.Service   // new
}

func NewService(
    dbService db.Service,
    eventNotifier notifier.EventNotifier,
    creds credentials.Service,
    entSvc *entcore.Service,   // new param
) *Service { ... }
```

Update `checks.NewService(...)` at `server/internal/app/server.go:480` to pass
`s.services.Entitlements`.

**Call the guard in `CreateCheck`** (service.go:758) — after the org lookup (line 760-763),
before slug/config validation, skip for internal checks:
```go
if req.Internal == nil || !*req.Internal {
    if err := s.entitlements.CheckCreateAllowed(ctx, org.UID); err != nil {
        return CheckResponse{}, err
    }
}
```

**Call the guard in `CloneCheck`** — the clone path at service.go:2471 calls `s.db.CreateCheck`
directly (bypassing `s.CreateCheck`). Add the same guard before that call, skipping for clones
that set `internal = true`.

**Translate to 402 in `handleCreateError`** (handler.go:464):
- Add `ErrorCodeQuotaExceeded = "QUOTA_EXCEEDED"` constant in `handlers/base/`.
- Add a case before `default`:
```go
case errors.Is(err, entcore.ErrEntitlementExceeded):
    var qe *entcore.QuotaError
    errors.As(err, &qe)
    body := entitlements.FormatQuotaError(qe)
    body["code"] = base.ErrorCodeQuotaExceeded
    return h.WriteJSON(writer, http.StatusPaymentRequired, body)
```

### 6. Frontend — `useEntitlements` hook (`web/dash0/src/api/hooks.ts`)

```ts
export interface EntitlementsLimits {
  maxChecks?: number | null;
  maxChecksPerMinute?: number | null;
  maxSsoUsers?: number | null;
}

export interface EntitlementsUsage {
  checks: number;
  checksPerMinute: number;
  ssoUsers: number;
}

export interface EntitlementsResponse {
  limits: EntitlementsLimits;
  usage?: EntitlementsUsage;
  source: string;
  stale: boolean;
  upgradeUrl?: string;
}

export function useEntitlements(org: string, opts?: { withUsage?: boolean }) {
  return useQuery({
    queryKey: ["entitlements", org, opts?.withUsage ?? false],
    queryFn: () =>
      apiFetch<EntitlementsResponse>(
        `/api/v1/orgs/${org}/entitlements${opts?.withUsage ? "?with=usage" : ""}`
      ),
    enabled: !!org,
    staleTime: 60 * 1000,
  });
}
```

### 7. Frontend — usage page (`web/dash0/src/routes/orgs/$org/organization.usage.tsx`)

New route in the Organization sidebar group (same as `organization.settings.tsx`,
`organization.members.tsx`, etc.). Add a "Usage" nav entry in `$org.tsx` (the `isOrganization`
sidebar matcher at ~line 270).

Page renders three usage-vs-limit rows for Checks, Checks per minute, and SSO users:
- Each row: label + progress bar (`current / limit`), numeric `n / max` label.
- When `limit == null`: show "Unlimited" instead of a bar.
- Bar overflows gracefully (capped at 100 %, turns red/destructive) when usage exceeds the limit.
- Show an "Upgrade" button/link anchored to `upgradeUrl` when present.
- Fully mobile-responsive (single-column stacked layout on small screens).
- Consult `http://localhost:4000/dash0/orgs/default/design-reference` for the Progress primitive
  (add it to the design-reference if missing).

### i18n keys

Add keys for labels used by the usage page, following the pattern of sibling organization pages.
At minimum: Usage, Checks, Checks per minute, SSO users, Unlimited, Upgrade, Your plan.

## Files affected

| Area | File | Change |
|---|---|---|
| Model | `server/internal/db/models/entitlements_payload.go` | add `MaxChecks`; update comment |
| Model | `server/internal/db/models/check.go` | add `CheckRate` struct |
| DB interface | `server/internal/db/service.go` | add `ListOrgCheckRates` |
| DB impl | `server/internal/db/postgres/postgres.go` | implement `ListOrgCheckRates` |
| DB impl | `server/internal/db/sqlite/sqlite.go` | implement `ListOrgCheckRates` |
| Ent service | `server/internal/entitlements/service.go` | add `Usage`, `CheckCreateAllowed`; update `merge` |
| Ent types | `server/internal/entitlements/usage.go` (new) or `defaults.go` | add `Usage` struct |
| Ent handler | `server/internal/handlers/entitlements/handler.go` | `?with=usage`; accept `maxChecks`; `overlayLimits` |
| Base | `server/internal/handlers/base/` | add `ErrorCodeQuotaExceeded` |
| Checks service | `server/internal/handlers/checks/service.go` | inject ent svc; quota guard in `CreateCheck` + `CloneCheck` |
| Checks handler | `server/internal/handlers/checks/handler.go` | 402 case in `handleCreateError` |
| Wiring | `server/internal/app/server.go` | pass `s.services.Entitlements` to `checks.NewService` |
| FE hook | `web/dash0/src/api/hooks.ts` | `useEntitlements` + types |
| FE page | `web/dash0/src/routes/orgs/$org/organization.usage.tsx` | new page |
| FE nav | `web/dash0/src/routes/orgs/$org.tsx` | add Usage nav link |

## Tests

### Backend

All tests: `t.Parallel()`, `require.New(t)`, table-driven where multiple cases.

- **`entitlements/service_test.go`**:
  - `Usage`: count = number of non-internal non-deleted checks; `checksPerMinute` = sum of 60/period
    for enabled checks only; `ssoUsers` = SSO member count.
  - `CheckCreateAllowed`: allows when nil cap; allows under cap; blocks at or over cap with
    `*QuotaError{LimitName:"MaxChecks"}`; skips for internal (exempt).
  - `merge`: `MaxChecks` propagates from stored row; nil row stays nil (unlimited).
- **`handlers/entitlements/handler_test.go`**:
  - GET without `?with=usage`: `usage` field absent.
  - GET with `?with=usage`: `usage` populated.
  - `limits` includes `maxChecks` when set.
  - PUT/PATCH with `{"limits":{"maxChecks":50}}` accepted (no longer rejected).
- **`handlers/checks/service_test.go`**:
  - `CreateCheck` returns `ErrEntitlementExceeded` when over `maxChecks` cap.
  - Internal check creation bypasses the cap.
- **`handlers/checks/handler_test.go`**:
  - POST `/checks` returns 402 with `code: "QUOTA_EXCEEDED"` and quota fields when over cap.

### Frontend

Playwright e2e in `web/dash0/e2e/`:

- Navigate to `/orgs/<org>/organization/usage`; page renders three usage rows.
- When all limits are null, shows "Unlimited" for each row.
- Set `maxChecks = 1` via admin API, create one check, verify the bar fills; attempt to create
  another check → API returns 402 and the UI surfaces the quota error (toast or error message).

## Verification

1. `make build` — no compilation errors.
2. `make lint` — no new findings.
3. `make test` — backend suite passes.
4. `make test-dash` — Playwright e2e passes.
5. Manual API smoke:
   ```bash
   # 1. Login
   TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
     -d '{"org":"default","email":"admin@solidping.com","password":"solidpass"}' \
     'http://localhost:4000/api/v1/auth/login' | jq -r '.accessToken')

   # 2. Set maxChecks = 2
   curl -s -X PATCH -H "Authorization: Bearer $TOKEN" \
     -H 'Content-Type: application/json' \
     -d '{"limits":{"maxChecks":2}}' \
     'http://localhost:4000/api/v1/orgs/default/entitlements' | jq .

   # 3. Verify limits + usage
   curl -s -H "Authorization: Bearer $TOKEN" \
     'http://localhost:4000/api/v1/orgs/default/entitlements?with=usage' | jq .

   # 4. Create checks until 402
   curl -s -X POST -H "Authorization: Bearer $TOKEN" \
     -H 'Content-Type: application/json' \
     -d '{"config":{"url":"https://example.com"}}' \
     'http://localhost:4000/api/v1/orgs/default/checks' | jq .
   ```
6. UI: visit `http://localhost:4000/dash0/orgs/default/organization/usage` and confirm bars render.

## Implementation plan

1. **Model** — add `MaxChecks` to `EntitlementLimits`; add `CheckRate` model. Verify `make build`.
2. **DB layer** — add `ListOrgCheckRates` interface + postgres + sqlite impls. Verify `make build`.
3. **Entitlements service** — add `Usage` struct; implement `Service.Usage` and
   `Service.CheckCreateAllowed`; update `merge`. Verify `make build` + `make test`.
4. **Entitlements handler** — parse `?with=usage`; wire `overlayLimits`; remove deprecated-key comment.
   Verify `make build` + `make test`.
5. **Checks enforcement** — inject ent svc into `checks.NewService`; update server.go wiring; add
   quota guard in `CreateCheck` and `CloneCheck`; add `QUOTA_EXCEEDED` error code; add 402 case in
   `handleCreateError`. Verify `make build` + `make test`.
6. **Frontend** — add `useEntitlements` hook + types; scaffold `organization.usage.tsx`; add nav link.
   Verify `make build` + `bun run lint` in `web/dash0/`.
7. **Tests** — write backend handler + service tests; write Playwright e2e scenarios. Verify
   `make test` + `make test-dash`.
8. **Full pass** — `make build && make lint && make test && make test-dash`.
9. **Archive** — move spec to `specs/done/2026/05/2026-05-25-11-entitlements-usage-stats.md`.

## Implementation Plan

Concrete, ordered checklist derived from the Approach above, with decisions locked in
against the current code:

1. **Model** (`entitlements_payload.go`, `check.go`)
   - Add `MaxChecks *int json:"maxChecks,omitempty"` as the first field of `EntitlementLimits`;
     rewrite the lines 24-26 comment (no longer "silently dropped").
   - Add `CheckRate{Enabled bool; Period timeutils.Duration}` to `check.go`.
2. **DB layer** (`db/service.go`, `postgres/postgres.go`, `sqlite/sqlite.go`)
   - Add interface method `ListOrgCheckRates(ctx, orgUID) ([]models.CheckRate, error)`.
   - Implement in both backends: `SELECT enabled, period FROM checks WHERE organization_uid = ?
     AND deleted_at IS NULL AND internal = false`. No SQL arithmetic.
3. **Entitlements service** (`entitlements/usage.go` new, `service.go`)
   - Add `Usage` struct (`Checks int`, `ChecksPerMinute float64`, `SSOUsers int`) in `usage.go`.
   - Implement `Service.Usage(ctx, orgUID) (Usage, error)` — sums `60s/period` for enabled rates,
     count = len(rates), ssoUsers via `CountSSOMembersForOrg`.
   - Implement `Service.CheckCreateAllowed(ctx, orgUID) error` — nil cap → allow; `len(rates) >= cap`
     → `*QuotaError{LimitName:"MaxChecks"}`.
   - Add `MaxChecks` propagation to `merge`.
4. **Entitlements handler** (`handlers/entitlements/handler.go`)
   - Parse `?with=usage` via `strings.Contains(query.Get("with"), "usage")`.
   - Compute usage only when requested; embed `Usage *entcore.Usage json:"usage,omitempty"` in the
     GET response struct (also embed in the write-path Resolve response for consistency — GET only).
   - Add `MaxChecks` to `overlayLimits`.
   - `DisallowUnknownFields` now accepts `maxChecks` once the model field exists; rewrite the
     misleading comment at lines 153-154.
5. **Checks enforcement** (`handlers/checks/service.go`, `handler.go`, `base/base.go`, `app/server.go`,
   `mcp/handler.go`, `handlers/discovery/service_test.go`, `handlers/checks/encryption_test.go`)
   - Add `entitlements *entcore.Service` field + param to `checks.NewService`; update ALL call sites.
   - Guard in `CreateCheck` (skip when `req.Internal != nil && *req.Internal`).
   - Guard in `CloneCheck` before `s.db.CreateCheck` (skip when `clone.Internal`).
   - Add `ErrorCodeQuotaExceeded = "QUOTA_EXCEEDED"` to `base/base.go`.
   - Add 402 case in `handleCreateError` translating `entcore.ErrEntitlementExceeded` via
     `entitlements.FormatQuotaError` + `code = QUOTA_EXCEEDED`.
6. **Frontend** (`web/dash0/src/api/hooks.ts`, `routes/orgs/$org/organization.usage.tsx`,
   `routes/orgs/$org/organization.tsx`, `components/ui/progress.tsx` new, locales)
   - Add `useEntitlements` hook + `EntitlementsResponse/Limits/Usage` types.
   - Add a `Progress` UI primitive (no Radix dependency available; lightweight div-based bar).
   - Add `organization.usage.tsx` page: three limit-vs-usage rows, Unlimited fallback, overflow→destructive,
     Upgrade link, mobile responsive.
   - Add a "Usage" tab to the Organization `TabNav` and a breadcrumb branch in `$org.tsx`.
   - Add i18n keys (`usage.*`) in en/fr/es/de org.json + `usage` nav key.
7. **Tests**
   - `entitlements/service_test.go`: `Usage`, `CheckCreateAllowed`, `merge` (MaxChecks).
   - `handlers/entitlements/handler_test.go` (new): GET with/without usage, maxChecks in limits,
     PUT/PATCH accept maxChecks.
   - `handlers/checks/service_test.go`: `CreateCheck` over cap → `ErrEntitlementExceeded`; internal bypass.
   - `handlers/checks/handler_test.go` (new): POST 402 with `code: QUOTA_EXCEEDED`.
   - Playwright e2e `web/dash0/e2e/entitlements-usage.spec.ts`.
8. **Full pass** — `rtk make build-backend build-dash0 lint-back test`.
9. **Archive** — move spec to `specs/done/2026/05/`.
