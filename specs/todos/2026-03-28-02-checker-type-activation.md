# Checker Type Activation System

## Overview

SolidPing has 20 check types (30 planned), all unconditionally available. As the platform grows, operators need control over which types are available — for security, dependency management, and multi-tenant flexibility.

**Use cases:**
- Lock down a production instance to only safe network checks
- Disable JavaScript and Docker checks on shared/hosted instances
- Let each organization further restrict which check types their users can create
- Prevent workers without Chrome from claiming browser check jobs
- Auto-detect missing dependencies (raw sockets, Docker socket, Chrome) at worker startup

**Design decisions:**
- **3-layer activation**: Server config (global) → Per-org override (DB) → Worker capabilities (auto-detected)
- **2-tier risk labels**: `safe` vs `unsafe` (binary, simple to reason about)
- **Soft-disable**: Disabled types keep checks in DB but stop scheduling jobs; resume when re-enabled
- **Default**: All types enabled (opt-out, not opt-in) — doesn't break existing setups

---

## Label System

### Risk: Safe vs Unsafe

**Unsafe** — requires special OS permissions, spawns processes, or executes user-provided code:

| Type | Reason |
|------|--------|
| `icmp` | Needs `cap_net_raw` / raw socket privileges |
| `docker` | Docker socket = root-equivalent access |
| `browser` | Launches Chrome/Chromium process |
| `js` | Executes arbitrary user-provided JavaScript |

**Safe** — only establishes standard network connections using Go libraries:
Everything else: HTTP, TCP, UDP, DNS, SSL, Domain, Heartbeat, WebSocket, FTP, SFTP, SSH, SMTP, POP3, IMAP, gRPC, PostgreSQL, MySQL, Redis, MongoDB, Oracle, MSSQL, Kafka, RabbitMQ, MQTT, SNMP, Game Server

### Dependency Labels (multiple per type)

| Label | Types |
|-------|-------|
| `standalone` | HTTP, TCP, UDP, DNS, SSL, Domain, Heartbeat, WebSocket, SSH, gRPC |
| `requires:database-driver` | PostgreSQL, MySQL, MongoDB, Redis, Oracle, MSSQL |
| `requires:chrome` | Browser |
| `requires:docker-socket` | Docker |
| `requires:raw-socket` | ICMP |
| `requires:scripting-runtime` | JavaScript |
| `requires:mail-protocol` | SMTP, POP3, IMAP |
| `requires:file-protocol` | FTP, SFTP |
| `requires:messaging-client` | Kafka, RabbitMQ, MQTT |

### Category Labels (for UI grouping)

`category:network`, `category:security`, `category:mail`, `category:database`, `category:remote-access`, `category:messaging`, `category:infrastructure`, `category:other`

---

## Layer 1: Server-Level Activation (config)

### Configuration

**File**: `server/internal/config/config.go`

```go
type CheckersConfig struct {
    Enabled       []string `koanf:"enabled"`         // Explicit allowlist (empty = all)
    Disabled      []string `koanf:"disabled"`         // Blocklist (applied after labels)
    EnabledLabels []string `koanf:"enabled_labels"`   // Enable types matching any of these labels
}
```

**Precedence**: `enabled` (explicit allowlist) > `enabled_labels` + `disabled` > default (all enabled)

**Config examples:**
```yaml
# Example 1: Disable unsafe types
checkers:
  enabled_labels: ["safe"]

# Example 2: Allow safe + databases, but not JS
checkers:
  enabled_labels: ["safe", "requires:database-driver"]
  disabled: ["js"]

# Example 3: Explicit allowlist
checkers:
  enabled: ["http", "tcp", "dns", "ssl", "postgresql"]
```

**Env vars:**
```
SP_CHECKERS_ENABLED=http,tcp,dns
SP_CHECKERS_DISABLED=docker,browser,js
SP_CHECKERS_ENABLED_LABELS=safe,requires:database-driver
```

### Resolution Logic

```
func resolveServerEnabledTypes(cfg CheckersConfig, allTypes []CheckTypeMeta) []CheckType:
  if cfg.Enabled is non-empty:
    return intersection(cfg.Enabled, allTypes)  // explicit allowlist

  result = allTypes  // start with all
  if cfg.EnabledLabels is non-empty:
    result = types matching ANY label in cfg.EnabledLabels
  result = result - cfg.Disabled
  return result
```

---

## Layer 2: Per-Org Activation (database)

Organizations can further restrict (never expand) the server-enabled types.

### Database

Use the existing `parameters` table (key-value per org, already supports JSONB values):

```
key: "checkers.disabled"
value: ["docker", "js"]   -- JSON array
```

No new table needed. The `parameters` table already exists with `organization_uid`, `key`, `value (jsonb)`.

### API

```
GET    /api/v1/orgs/:org/settings/checkers     → { "disabled": ["docker", "js"], "effective": [...all enabled types...] }
PATCH  /api/v1/orgs/:org/settings/checkers      → { "disabled": ["docker", "js"] }
```

The `effective` field in the response shows the final resolved list (server-enabled minus org-disabled), so the UI knows exactly what's available.

### Resolution

```
effective_types = server_enabled_types - org_disabled_types
```

An org can only disable types that the server has enabled. It cannot enable types the server disabled.

---

## Layer 3: Worker Capabilities (auto-detected)

Workers auto-detect their capabilities at startup and report them. Job claiming filters by capabilities.

### Auto-Detection at Startup

```go
func detectCapabilities() []string {
    caps := []string{"safe"}  // all workers can run safe types

    if canPing()            { caps = append(caps, "raw-socket") }
    if hasDockerSocket()    { caps = append(caps, "docker-socket") }
    if hasChromeInstalled() { caps = append(caps, "chrome") }
    caps = append(caps, "scripting-runtime")  // JS runtime is always compiled in
    caps = append(caps, "database-driver")     // drivers are always compiled in
    caps = append(caps, "mail-protocol")
    caps = append(caps, "file-protocol")
    caps = append(caps, "messaging-client")
    return caps
}

func canPing() bool {
    // Try to create a raw ICMP socket, close immediately
}

func hasDockerSocket() bool {
    // Check if /var/run/docker.sock exists and is accessible
}

func hasChromeInstalled() bool {
    // exec.LookPath("chromium") || exec.LookPath("google-chrome") || ...
}
```

### Worker Model Change

**File**: `server/internal/db/models/worker.go`

Add `Capabilities []string` field (stored as JSONB). Reported during registration and heartbeat.

### Job Claiming Filter

**File**: `server/internal/checkworker/checkjobsvc/service.go`

The `selectAvailableJobs()` query currently only filters by region. Add capability filtering:

The worker resolves its enabled check types at startup (intersection of server config and auto-detected capabilities) and uses that as a simple `IN` clause. Clean, fast, no complex join.

```sql
-- Add to selectAvailableJobs:
AND type IN (?)  -- worker's enabled types list
```

---

## Backend

### CheckTypeMeta Registry

**File**: `server/internal/checkers/checkerdef/types.go`

```go
type CheckTypeMeta struct {
    Type        CheckType
    Labels      []string
    Description string
}

var checkTypesRegistry = []CheckTypeMeta{
    {CheckTypeHTTP, []string{"safe", "standalone", "category:network"}, "Monitor HTTP/HTTPS endpoints"},
    {CheckTypeTCP, []string{"safe", "standalone", "category:network"}, "Check TCP port connectivity"},
    {CheckTypeICMP, []string{"unsafe", "requires:raw-socket", "category:network"}, "Ping hosts via ICMP"},
    {CheckTypeDNS, []string{"safe", "standalone", "category:network"}, "Monitor DNS resolution"},
    {CheckTypeSSL, []string{"safe", "standalone", "category:security"}, "Check SSL certificate validity"},
    {CheckTypeDomain, []string{"safe", "standalone", "category:security"}, "Monitor domain expiration"},
    {CheckTypeHeartbeat, []string{"safe", "standalone", "category:other"}, "Receive heartbeat pings"},
    {CheckTypeSMTP, []string{"safe", "requires:mail-protocol", "category:mail"}, "Check SMTP server connectivity"},
    {CheckTypeUDP, []string{"safe", "standalone", "category:network"}, "Check UDP port reachability"},
    {CheckTypeSSH, []string{"safe", "standalone", "category:remote-access"}, "Check SSH server availability"},
    {CheckTypePOP3, []string{"safe", "requires:mail-protocol", "category:mail"}, "Check POP3 server availability"},
    {CheckTypeIMAP, []string{"safe", "requires:mail-protocol", "category:mail"}, "Check IMAP server availability"},
    {CheckTypeWebSocket, []string{"safe", "standalone", "category:network"}, "Check WebSocket connectivity"},
    {CheckTypePostgreSQL, []string{"safe", "requires:database-driver", "category:database"}, "Check PostgreSQL database health"},
    {CheckTypeMySQL, []string{"safe", "requires:database-driver", "category:database"}, "Check MySQL/MariaDB database health"},
    {CheckTypeRedis, []string{"safe", "requires:database-driver", "category:database"}, "Check Redis server health"},
    {CheckTypeMongoDB, []string{"safe", "requires:database-driver", "category:database"}, "Check MongoDB database health"},
    {CheckTypeFTP, []string{"safe", "requires:file-protocol", "category:remote-access"}, "Check FTP server availability"},
    {CheckTypeSFTP, []string{"safe", "requires:file-protocol", "category:remote-access"}, "Check SFTP server availability"},
    {CheckTypeJS, []string{"unsafe", "requires:scripting-runtime", "category:other"}, "Run custom JavaScript scripts"},
    // Future types follow same pattern
}
```

### Activation Resolver

**New file**: `server/internal/checkers/checkerdef/activation.go`

```go
type ActivationResolver struct {
    serverEnabled map[CheckType]bool  // resolved at startup
}

func NewActivationResolver(cfg CheckersConfig) *ActivationResolver { ... }
func (r *ActivationResolver) IsTypeEnabled(ct CheckType, orgDisabled []string) bool { ... }
func (r *ActivationResolver) ListEnabledTypes(orgDisabled []string) []CheckTypeMeta { ... }
func (r *ActivationResolver) WorkerEnabledTypes(capabilities []string, orgDisabled []string) []CheckType { ... }
```

### Enforcement Points

| Layer | Where | Action |
|-------|-------|--------|
| **API: list types** | `GET /api/v1/check-types` | Return only effective types for the org |
| **API: create check** | `POST /api/v1/orgs/:org/checks` | Reject if type not in effective types (400) |
| **Job reconciliation** | `reconcileCheckJobs()` | Skip job creation for disabled types |
| **Worker: claim jobs** | `selectAvailableJobs()` | Only claim types the worker can run |
| **Worker: execute** | `executeJob()` | Double-check type is enabled (handles config drift) |
| **Frontend: form** | check-form.tsx | Only show enabled types in dropdown |

### Soft-Disable Behavior for Existing Checks

When a type becomes disabled (at any layer):
1. **Jobs**: `reconcileCheckJobs()` deletes jobs for disabled-type checks
2. **Check status**: Check remains in DB, last result preserved
3. **UI**: Check shows a banner: "This check type has been disabled by the administrator"
4. **API**: Check is still readable via GET, but PATCH rejects type changes, and the check can't be re-enabled if type is disabled
5. **Re-enable**: When type is re-enabled, next `reconcileCheckJobs()` run recreates jobs

---

## New API Endpoints

### `GET /api/v1/check-types`

Returns all check types with metadata and activation status (server-level).

### `GET /api/v1/orgs/:org/check-types`

Returns check types resolved for the org (server + org settings).

```json
{
  "data": [
    {
      "type": "http",
      "description": "Monitor HTTP/HTTPS endpoints",
      "labels": ["safe", "standalone", "category:network"],
      "enabled": true
    },
    {
      "type": "docker",
      "description": "Monitor Docker container health",
      "labels": ["unsafe", "requires:docker-socket", "category:infrastructure"],
      "enabled": false,
      "disabledReason": "server"
    }
  ]
}
```

### `GET /api/v1/orgs/:org/settings/checkers`

Returns org-specific checker settings.

```json
{
  "disabled": ["docker", "js"],
  "effective": ["http", "tcp", "dns", "ssl", ...]
}
```

### `PATCH /api/v1/orgs/:org/settings/checkers`

Update org-specific checker settings (admin only).

```json
{
  "disabled": ["docker", "js"]
}
```

---

## Frontend

### Dashboard (`web/dash0/src/components/shared/check-form.tsx`)

Replace hardcoded `checkTypes` array with data fetched from `GET /api/v1/orgs/:org/check-types`. The form fields per type remain hardcoded (they're structural), but the available types in the dropdown come from the API.

### New Hook

**File**: `web/dash0/src/api/hooks.ts`

```typescript
function useCheckTypes(org: string) {
  return useQuery({
    queryKey: ["check-types", org],
    queryFn: () => apiClient.get(`/api/v1/orgs/${org}/check-types`),
    staleTime: 5 * 60 * 1000,  // 5 min cache — types rarely change
  });
}
```

### Disabled Check Banner

When viewing a check whose type is disabled, show:
```
⚠ This check type has been disabled by the administrator. The check is paused and no new results will be collected.
```

---

## Key Files

| File | Action |
|------|--------|
| `server/internal/checkers/checkerdef/types.go` | Add `CheckTypeMeta`, labels registry, `GetMeta()`, `MatchesLabels()` |
| `server/internal/checkers/checkerdef/activation.go` | **New** — `ActivationResolver` |
| `server/internal/config/config.go` | Add `CheckersConfig` struct |
| `server/internal/checkers/registry/registry.go` | Wire activation into `GetChecker()` / `ParseConfig()` |
| `server/internal/handlers/checks/handler.go` | Add `GET /api/v1/check-types`, `GET /api/v1/orgs/:org/check-types` |
| `server/internal/handlers/checks/service.go` | Guard `CreateCheck` with `IsTypeEnabled()` |
| `server/internal/handlers/checksettings/` | **New** — handler + service for per-org checker settings CRUD |
| `server/internal/db/models/worker.go` | Add `Capabilities []string` field |
| `server/internal/checkworker/worker.go` | Auto-detect capabilities, filter job types |
| `server/internal/checkworker/checkjobsvc/service.go` | Add type filter to `selectAvailableJobs()` |
| `server/internal/app/server.go` | Register new routes |
| `web/dash0/src/components/shared/check-form.tsx` | Fetch types from API, show only enabled |
| `web/dash0/src/api/hooks.ts` | Add `useCheckTypes()` query hook |
| `web/dash0/src/locales/en/checks.json` | Add "type disabled" messages |
| `web/dash0/src/locales/fr/checks.json` | Add "type disabled" messages |

### DB Migration

```sql
-- Add capabilities to workers table
ALTER TABLE workers ADD COLUMN capabilities jsonb DEFAULT '[]';
```

No migration needed for per-org settings (uses existing `parameters` table).

---

## Verification

- [ ] Default config (no `checkers` section): all 20 types available — no regression
- [ ] `SP_CHECKERS_DISABLED=docker,browser,js`: those types hidden from API/UI, creation rejected
- [ ] `SP_CHECKERS_ENABLED_LABELS=safe`: only safe types available (excludes ICMP, Docker, Browser, JS)
- [ ] `SP_CHECKERS_ENABLED=http,tcp`: only those two types available
- [ ] Per-org disable: org disables MySQL → MySQL hidden for that org only
- [ ] Per-org can't enable server-disabled types
- [ ] Existing checks of disabled type: jobs deleted, check shows "type disabled" in UI
- [ ] Re-enabling a type: jobs recreated on next reconciliation
- [ ] Worker without Chrome: doesn't claim browser jobs
- [ ] Worker without Docker socket: doesn't claim docker jobs
- [ ] `GET /api/v1/check-types` returns correct filtered list with labels
- [ ] `GET /api/v1/orgs/:org/check-types` respects both server + org settings
- [ ] Frontend dropdown only shows enabled types
- [ ] `make lint` and `make test` pass

**Status**: Draft | **Created**: 2026-03-28
