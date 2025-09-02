# Edge Worker HTTP API

## Overview

Add support for edge workers that connect to the master server via HTTP instead of directly to PostgreSQL. This enables customers to monitor elements of their own infrastructure by deploying lightweight workers inside their network, without exposing the SolidPing database.

## Goals

1. Edge workers connect via HTTP API, requiring no PostgreSQL dependency
2. Support two modes: global (all orgs for a region) and org-scoped (single customer)
3. Secure worker authentication with registration token bootstrap flow
4. Per-organization config encryption (AES-256-GCM) to protect credentials in transit and at rest
5. Customer-specific regions for org-scoped deployments
6. Same binary, new node role (`edge`)

## Non-Goals

- Heartbeat checks on edge workers (passive checks requiring DB access stay on internal workers)
- Push-based job distribution (edge workers poll the master)
- WebSocket/streaming connection between edge worker and master

---

## Architecture

### Current State

The `CheckWorker` in `back/internal/checkworker/worker.go` uses:
- `checkjobsvc.Service` interface for `ClaimJobs` and `ReleaseLease` (already abstracted)
- `db.Service` directly for `RegisterOrUpdateWorker`, `UpdateWorkerHeartbeat`, `SaveResultWithStatusTracking`, `GetCheck`, `CreateResult`
- `incidents.Service` for `ProcessCheckResult`

### Target State

Introduce a `WorkerBackend` interface that abstracts all worker-to-server operations. Two implementations:

| Implementation | Transport | Used by |
|----------------|-----------|---------|
| `DirectBackend` | SQL (bun ORM) | Internal workers (`SP_NODE_ROLE=all,checks`) |
| `HTTPBackend` | HTTP client | Edge workers (`SP_NODE_ROLE=edge`) |

```
                    ┌──────────────────────┐
                    │   Master Server      │
                    │  (PostgreSQL + API)  │
                    │                      │
                    │  POST /workers/*     │
                    └──────┬───────────────┘
                           │ HTTPS
              ┌────────────┼───────────┐
              │            │           │
        ┌─────┴──────┐ ┌───┴─────┐ ┌───┴──────┐
        │ Edge       │ │ Edge    │ │ Internal │
        │ eu-fr      │ │ acme-dc │ │ Worker   │
        │ Mode 1     │ │ Mode 2  │ │ (SQL)    │
        │ (global)   │ │(org)    │ │          │
        └────────────┘ └─────────┘ └──────────┘
```

---

## WorkerBackend Interface

### `back/internal/checkworker/backend/backend.go`

```go
package backend

import (
    "context"
    "time"

    "github.com/fclairamb/solidping/back/internal/db/models"
)

// WorkerBackend abstracts all worker-to-server operations.
// DirectBackend talks to SQL; HTTPBackend talks to the master HTTP API.
type WorkerBackend interface {
    // Register registers or updates the worker. Returns the registered worker.
    Register(ctx context.Context, worker *models.Worker) (*models.Worker, error)

    // Heartbeat updates the worker's last_active_at timestamp.
    Heartbeat(ctx context.Context, workerUID string) error

    // ClaimJobs atomically claims up to limit check jobs for the given worker.
    ClaimJobs(ctx context.Context, workerUID string, region *string, limit int, maxAhead time.Duration) ([]*models.CheckJob, error)

    // SubmitResult saves a check result and triggers incident processing server-side.
    // For HTTPBackend, the server handles lease release and rescheduling.
    // Returns the next scheduled time for the job.
    SubmitResult(ctx context.Context, jobUID string, workerUID string, result *SubmitResultRequest) (*SubmitResultResponse, error)
}

// SubmitResultRequest contains the result data sent by a worker.
type SubmitResultRequest struct {
    Status   int            `json:"status"`
    Duration float32        `json:"duration"`
    Metrics  map[string]any `json:"metrics,omitempty"`
    Output   map[string]any `json:"output,omitempty"`
}

// SubmitResultResponse contains the server's response after processing a result.
type SubmitResultResponse struct {
    NextScheduledAt time.Time `json:"nextScheduledAt"`
}
```

Note: `SubmitResult` for the `HTTPBackend` combines result saving, incident processing, and lease release into a single HTTP call. The server handles all of this atomically. For `DirectBackend`, it calls the existing `dbService.SaveResultWithStatusTracking`, `incidentSvc.ProcessCheckResult`, and `checkJobSvc.ReleaseLease` in sequence.

### DirectBackend (`back/internal/checkworker/backend/direct.go`)

Wraps existing `db.Service`, `checkjobsvc.Service`, and `incidents.Service` calls. This is a thin adapter — no behavior change for internal workers.

### HTTPBackend (`back/internal/checkworker/backend/http.go`)

HTTP client that calls the master's Worker API endpoints. Handles:
- JSON serialization/deserialization
- Bearer token authentication (`spw_...`)
- Retry with exponential backoff on transient errors (502, 503, 504)
- Config decryption when `check_jobs.encrypted = true`

---

## Worker API Endpoints

All endpoints authenticated with worker tokens (`Authorization: Bearer spw_...`).

### POST /api/v1/workers/register

Called once when an edge worker starts. Uses a one-time registration token to create the worker and get a permanent token.

**Request (with registration token):**
```json
{
    "registrationToken": "spr_abc123...",
    "slug": "edge-eu-fr-01",
    "name": "Paris Edge Worker 01",
    "region": "eu-fr"
}
```

**Request (with existing worker token — re-registration/update):**
```json
{
    "slug": "edge-eu-fr-01",
    "name": "Paris Edge Worker 01",
    "region": "eu-fr"
}
```

**Response:**
```json
{
    "uid": "worker-uid-here",
    "token": "spw_permanent_token_here",
    "region": "eu-fr",
    "mode": "edge",
    "organizationUid": null,
    "encryptionKeys": {
        "org-uid-1": "base64-encoded-aes-key"
    }
}
```

Notes:
- `token` is only returned on first registration (with `spr_` token). Subsequent calls with `spw_` token do not return the token again.
- `encryptionKeys` maps org UIDs to their AES-256 keys. For Mode 2 workers, this contains exactly one key. For Mode 1, it contains keys for all orgs that have encryption enabled in the worker's region. Keys are transmitted once over TLS.
- `organizationUid` is set when the registration token was created for a specific org (Mode 2).

**Error responses:**
- `401 UNAUTHORIZED` — Invalid or expired registration token
- `409 CONFLICT` — Worker slug already exists with a different token
- `422 VALIDATION_ERROR` — Invalid slug, name, or region

### POST /api/v1/workers/heartbeat

Updates `last_active_at` for the authenticated worker.

**Request:** (empty body)

**Response:**
```json
{
    "ok": true,
    "encryptionKeys": {
        "org-uid-new": "base64-encoded-aes-key"
    }
}
```

Notes:
- `encryptionKeys` is only present if new org keys were added since last heartbeat/registration. This handles key rotation and new orgs without requiring re-registration.

### POST /api/v1/workers/jobs/claim

Atomically selects and claims available check jobs. Single-step: server picks the best jobs for this worker's region and capacity.

**Request:**
```json
{
    "capacity": 5,
    "maxAhead": "30s"
}
```

**Response:**
```json
{
    "data": [
        {
            "uid": "job-uid",
            "organizationUid": "org-uid",
            "checkUid": "check-uid",
            "region": "eu-fr",
            "type": "http",
            "config": {
                "url": "https://example.com",
                "method": "GET"
            },
            "encrypted": false,
            "period": "60s",
            "scheduledAt": "2026-03-22T10:00:00Z"
        },
        {
            "uid": "job-uid-2",
            "organizationUid": "org-uid-2",
            "checkUid": "check-uid-2",
            "region": "eu-fr",
            "type": "ssh",
            "config": {
                "_encrypted": "base64-ciphertext...",
                "_nonce": "base64-nonce...",
                "_keyId": "org-uid-2"
            },
            "encrypted": true,
            "period": "300s",
            "scheduledAt": "2026-03-22T10:00:05Z"
        }
    ]
}
```

Notes:
- Region and worker identity are derived from the authenticated token — not sent in the request body.
- For org-scoped workers (Mode 2), the server only returns jobs for that org.
- Heartbeat check types are excluded from the response (server filters them out for edge workers).
- The server uses the existing `ClaimJobs` logic (`SELECT FOR UPDATE SKIP LOCKED` on PostgreSQL).
- Empty `data` array (not null) when no jobs are available.

**Why single-step claim (not two-step list+reserve):**
- Mirrors the proven `SELECT FOR UPDATE SKIP LOCKED` pattern already used internally
- One HTTP round-trip instead of two
- No race conditions between list and reserve
- The worker doesn't need to be selective — it takes whatever is available for its region

### POST /api/v1/workers/jobs/:jobUid/results

Submit a check result. The server handles result persistence, incident processing, lease release, and rescheduling.

**Request:**
```json
{
    "status": 1,
    "duration": 245.5,
    "metrics": {
        "ttfb": 120,
        "dnsTime": 30,
        "tlsTime": 45
    },
    "output": {
        "statusCode": 200,
        "url": "https://example.com"
    }
}
```

**Response:**
```json
{
    "ok": true,
    "nextScheduledAt": "2026-03-22T10:01:00Z"
}
```

**Error responses:**
- `404 NOT_FOUND` — Job UID not found
- `403 FORBIDDEN` — Job is not leased to this worker
- `422 VALIDATION_ERROR` — Invalid status value

Notes:
- The server calls `SaveResultWithStatusTracking`, `ProcessCheckResult` (incidents), and `ReleaseLease` in a single transaction.
- `nextScheduledAt` tells the edge worker when this job will next be available (informational only — the edge worker doesn't need to track it).
- Status values: `1` (up), `2` (down), `3` (timeout), `4` (error), `5` (running)

---

## Authentication

### Token Types

| Prefix | Type | Purpose | Scope |
|--------|------|---------|-------|
| `spr_` | Registration token | One-time bootstrap for edge worker registration | Global or org-scoped |
| `spw_` | Worker token | Permanent auth for edge worker API calls | Tied to worker record |

### Registration Token Flow

1. **Admin creates registration token** via dashboard or API:
   ```
   POST /api/v1/admin/worker-registration-tokens
   {
       "name": "Paris DC Worker",
       "region": "eu-fr",
       "organizationUid": null,   // null = Mode 1 (global), set = Mode 2 (org-scoped)
       "expiresAt": "2026-04-22T00:00:00Z"
   }
   ```
   Response includes the `spr_...` token (shown once).

2. **Edge worker registers** using the registration token:
   ```
   POST /api/v1/workers/register
   Authorization: Bearer spr_abc123...
   ```
   Response includes the permanent `spw_...` token (shown once).
   The registration token is marked as used and cannot be reused.

3. **Edge worker uses permanent token** for all subsequent API calls:
   ```
   POST /api/v1/workers/jobs/claim
   Authorization: Bearer spw_xyz789...
   ```

### Worker Token Storage

```sql
-- workers table additions
ALTER TABLE workers ADD COLUMN mode VARCHAR(10) NOT NULL DEFAULT 'internal';
ALTER TABLE workers ADD COLUMN organization_uid VARCHAR(36) REFERENCES organizations(uid);
ALTER TABLE workers ADD COLUMN token_hash VARCHAR(128);
ALTER TABLE workers ADD COLUMN token_prefix VARCHAR(12);
```

- `mode`: `internal` (SQL-connected) or `edge` (HTTP-connected)
- `organization_uid`: NULL for Mode 1 (global), set for Mode 2 (org-scoped)
- `token_hash`: bcrypt hash of the `spw_...` token
- `token_prefix`: First 8 chars of the token for identification in UI (e.g., `spw_abc1...`)

### Auth Middleware Extension

The existing auth middleware in `back/internal/middleware/auth.go` recognizes Bearer tokens. Extend it to detect `spw_` prefix:

```go
func (m *AuthMiddleware) extractWorkerFromToken(token string) (*models.Worker, error) {
    if !strings.HasPrefix(token, "spw_") {
        return nil, nil // Not a worker token
    }
    // Look up worker by token_prefix, verify bcrypt hash
    // Set worker context (UID, region, org scope)
}
```

Worker-authenticated requests set a `ContextKeyWorker` in the request context. Worker API handlers check for this instead of user context.

---

## Per-Organization Config Encryption

### Overview

Check configurations may contain sensitive credentials (HTTP basic auth, SSH passwords/keys, SMTP passwords, etc.). Per-org encryption ensures these are protected both at rest in the database and in transit to edge workers.

### Key Management

Each organization can generate an encryption key:

```
POST /api/v1/orgs/:org/settings/encryption-key
```

Response:
```json
{
    "keyId": "org-uid",
    "createdAt": "2026-03-22T10:00:00Z"
}
```

The key is:
- A random 256-bit AES key
- Stored in the `parameters` table as a secret parameter (key: `encryption_key`, secret: true)
- Transmitted to edge workers during registration (one-time, over TLS)
- Used for AES-256-GCM encryption/decryption

### Encryption Flow

**When creating/updating check jobs** (in `reconcileCheckJobs`):
1. Check if the org has an encryption key
2. If yes, encrypt the `config` JSONB with AES-256-GCM
3. Store as: `{"_encrypted": "<base64-ciphertext>", "_nonce": "<base64-nonce>", "_keyId": "<org-uid>"}`
4. Set `check_jobs.encrypted = true`

**When edge worker claims jobs:**
1. Encrypted configs are sent as-is in the claim response
2. Edge worker checks `encrypted` flag
3. If encrypted, uses the org's key (received during registration, identified by `_keyId`) to decrypt
4. Decrypted config is passed to the checker

**When internal worker claims jobs:**
1. Same flow — internal `DirectBackend` reads the org's key from parameters and decrypts before passing to the checker

### Encryption Implementation

```go
// back/internal/crypto/config_encryption.go

package crypto

import (
    "crypto/aes"
    "crypto/cipher"
    "crypto/rand"
    "encoding/base64"
    "encoding/json"
    "fmt"
)

// EncryptConfig encrypts a config map using AES-256-GCM.
func EncryptConfig(config map[string]any, key []byte, keyID string) (map[string]any, error) {
    plaintext, err := json.Marshal(config)
    if err != nil {
        return nil, fmt.Errorf("marshal config: %w", err)
    }

    block, err := aes.NewCipher(key)
    if err != nil {
        return nil, fmt.Errorf("create cipher: %w", err)
    }

    gcm, err := cipher.NewGCM(block)
    if err != nil {
        return nil, fmt.Errorf("create GCM: %w", err)
    }

    nonce := make([]byte, gcm.NonceSize())
    if _, err := rand.Read(nonce); err != nil {
        return nil, fmt.Errorf("generate nonce: %w", err)
    }

    ciphertext := gcm.Seal(nil, nonce, plaintext, nil)

    return map[string]any{
        "_encrypted": base64.StdEncoding.EncodeToString(ciphertext),
        "_nonce":     base64.StdEncoding.EncodeToString(nonce),
        "_keyId":     keyID,
    }, nil
}

// DecryptConfig decrypts an encrypted config map using AES-256-GCM.
func DecryptConfig(encryptedConfig map[string]any, key []byte) (map[string]any, error) {
    ciphertextB64, _ := encryptedConfig["_encrypted"].(string)
    nonceB64, _ := encryptedConfig["_nonce"].(string)

    ciphertext, err := base64.StdEncoding.DecodeString(ciphertextB64)
    if err != nil {
        return nil, fmt.Errorf("decode ciphertext: %w", err)
    }

    nonce, err := base64.StdEncoding.DecodeString(nonceB64)
    if err != nil {
        return nil, fmt.Errorf("decode nonce: %w", err)
    }

    block, err := aes.NewCipher(key)
    if err != nil {
        return nil, fmt.Errorf("create cipher: %w", err)
    }

    gcm, err := cipher.NewGCM(block)
    if err != nil {
        return nil, fmt.Errorf("create GCM: %w", err)
    }

    plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
    if err != nil {
        return nil, fmt.Errorf("decrypt: %w", err)
    }

    var config map[string]any
    if err := json.Unmarshal(plaintext, &config); err != nil {
        return nil, fmt.Errorf("unmarshal config: %w", err)
    }

    return config, nil
}
```

### Key Rotation

1. Admin generates a new encryption key for the org
2. Old key is kept in `parameters` as `encryption_key_previous` (for in-flight decryption)
3. All check jobs are re-encrypted with the new key (background job)
4. New key is distributed to edge workers via the next heartbeat response (`encryptionKeys` field)
5. Once all jobs are re-encrypted, old key is deleted

---

## Custom Regions (Org-Scoped)

### Overview

Customers deploying edge workers need regions specific to their infrastructure (e.g., `acme-datacenter-1`). These regions are org-scoped and don't appear in the global region list.

### Storage

Org-scoped custom regions are stored in the existing `parameters` table:
- **Key**: `custom_regions`
- **Organization UID**: the org's UID
- **Value**: `[{"slug": "acme-dc1", "emoji": "🏢", "name": "Acme Datacenter 1"}]`

### Region Resolution Update

Update `back/internal/regions/regions.go` `ResolveRegionsForCheck`:

1. Check-specific regions (if set on the check)
2. **Org custom regions** (new — from `custom_regions` parameter)
3. Org default regions (from `default_regions` parameter)
4. System default regions (from system `regions` parameter)
5. All defined global regions (fallback)

### API

```
GET  /api/v1/orgs/:org/regions           — Returns both global and custom regions
POST /api/v1/orgs/:org/regions           — Create a custom region
DELETE /api/v1/orgs/:org/regions/:slug   — Delete a custom region
```

**Create custom region:**
```json
{
    "slug": "acme-dc1",
    "emoji": "🏢",
    "name": "Acme Datacenter 1"
}
```

Custom region slugs must not conflict with global region slugs.

---

## Configuration

### Edge Worker Environment Variables

```bash
# Required for edge mode
SP_NODE_ROLE=edge                          # New role value
SP_MASTER_URL=https://solidping.example.com # Master server URL
SP_WORKER_TOKEN=spw_abc123...              # Permanent worker token (after registration)
SP_REGION=eu-fr-paris                      # Worker region

# First-time registration only
SP_WORKER_REGISTRATION_TOKEN=spr_xyz...    # One-time registration token

# Optional (same as internal workers)
SP_CHECK_WORKER_NB=5                       # Runner pool size (default: 5)
SP_LOG_LEVEL=info                          # Log level
```

### Config Struct Changes

```go
// back/internal/config/config.go

const NodeRoleEdge = "edge" // Add to existing constants

// EdgeWorkerConfig contains edge worker configuration.
type EdgeWorkerConfig struct {
    MasterURL         string        `koanf:"master_url"`
    WorkerToken       string        `koanf:"worker_token"`
    RegistrationToken string        `koanf:"registration_token"`
    TLSSkipVerify     bool          `koanf:"tls_skip_verify"`     // For dev/testing only
    PollInterval      time.Duration `koanf:"poll_interval"`       // Default: 10s
    HeartbeatInterval time.Duration `koanf:"heartbeat_interval"`  // Default: 50s
}
```

Add to `Config`:
```go
EdgeWorker EdgeWorkerConfig `koanf:"edge_worker"`
```

Add to `ShouldRunChecks`:
```go
func (c *Config) ShouldRunEdge() bool {
    return c.Node.Role == NodeRoleEdge
}
```

Validation:
- `NodeRoleEdge` requires `SP_MASTER_URL` to be set
- `NodeRoleEdge` requires either `SP_WORKER_TOKEN` or `SP_WORKER_REGISTRATION_TOKEN`
- `NodeRoleEdge` does NOT require a database configuration

---

## Data Model Changes

### Workers Table Migration

```sql
-- Add edge worker fields to workers table
ALTER TABLE workers ADD COLUMN mode VARCHAR(10) NOT NULL DEFAULT 'internal';
ALTER TABLE workers ADD COLUMN organization_uid VARCHAR(36) REFERENCES organizations(uid);
ALTER TABLE workers ADD COLUMN token_hash VARCHAR(128);
ALTER TABLE workers ADD COLUMN token_prefix VARCHAR(12);

CREATE INDEX idx_workers_token_prefix ON workers(token_prefix) WHERE token_prefix IS NOT NULL;
CREATE INDEX idx_workers_organization_uid ON workers(organization_uid) WHERE organization_uid IS NOT NULL;
```

### Worker Registration Tokens Table

```sql
CREATE TABLE worker_registration_tokens (
    uid VARCHAR(36) PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    token_hash VARCHAR(128) NOT NULL,
    token_prefix VARCHAR(12) NOT NULL,
    region VARCHAR(50),
    organization_uid VARCHAR(36) REFERENCES organizations(uid),
    created_by_uid VARCHAR(36) NOT NULL REFERENCES users(uid),
    used_by_worker_uid VARCHAR(36) REFERENCES workers(uid),
    used_at TIMESTAMP,
    expires_at TIMESTAMP NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_wrt_token_prefix ON worker_registration_tokens(token_prefix);
```

### Model Structs

```go
// back/internal/db/models/worker_registration_token.go

type WorkerRegistrationToken struct {
    UID             string     `bun:"uid,pk,type:varchar(36)"`
    Name            string     `bun:"name,notnull"`
    TokenHash       string     `bun:"token_hash,notnull"`
    TokenPrefix     string     `bun:"token_prefix,notnull"`
    Region          *string    `bun:"region"`
    OrganizationUID *string    `bun:"organization_uid"`
    CreatedByUID    string     `bun:"created_by_uid,notnull"`
    UsedByWorkerUID *string    `bun:"used_by_worker_uid"`
    UsedAt          *time.Time `bun:"used_at"`
    ExpiresAt       time.Time  `bun:"expires_at,notnull"`
    CreatedAt       time.Time  `bun:"created_at,notnull,default:current_timestamp"`
}
```

---

## Edge Worker Execution Flow

### Startup

```
1. Load config (SP_NODE_ROLE=edge, SP_MASTER_URL, SP_WORKER_TOKEN)
2. Skip database initialization entirely
3. Create HTTPBackend with master URL and token
4. If SP_WORKER_REGISTRATION_TOKEN is set and SP_WORKER_TOKEN is not:
   a. Call POST /workers/register with registration token
   b. Receive and store permanent worker token
   c. Receive encryption keys
5. Else:
   a. Call POST /workers/register with worker token (re-registration)
   b. Receive updated encryption keys
6. Start CheckWorker with HTTPBackend
7. Fetcher loop, runner pool, heartbeat loop — same as internal worker
```

### Job Claiming (via HTTPBackend)

```
fetchAndDistributeJobs:
  1. available = availableRunners.Load()
  2. POST /workers/jobs/claim { capacity: available, maxAhead: "30s" }
  3. For each job in response:
     a. If job.encrypted: decrypt config using stored org key
     b. Send to runner via jobsChan
```

### Result Submission (via HTTPBackend)

```
executeJob:
  1. Parse config, get checker from registry
  2. Wait for scheduledAt
  3. Execute check with 30s timeout
  4. POST /workers/jobs/:jobUid/results { status, duration, metrics, output }
  5. Server handles: save result, process incidents, release lease, reschedule
```

### Heartbeat

```
heartbeatLoop:
  Every 50s: POST /workers/heartbeat
  If response contains encryptionKeys: update local key cache
```

---

## Backend Implementation

### Files to create

| File | Purpose |
|------|---------|
| `back/internal/checkworker/backend/backend.go` | `WorkerBackend` interface definition |
| `back/internal/checkworker/backend/direct.go` | `DirectBackend` — wraps existing SQL services |
| `back/internal/checkworker/backend/http.go` | `HTTPBackend` — HTTP client for edge workers |
| `back/internal/checkworker/backend/http_test.go` | Tests for HTTPBackend |
| `back/internal/crypto/config_encryption.go` | AES-256-GCM encrypt/decrypt for configs |
| `back/internal/crypto/config_encryption_test.go` | Encryption tests |
| `back/internal/crypto/token.go` | Token generation (`spr_`, `spw_` prefixed tokens) |
| `back/internal/handlers/workers/handler.go` | HTTP handlers for worker API endpoints |
| `back/internal/handlers/workers/service.go` | Business logic for worker API |
| `back/internal/handlers/workers/handler_test.go` | Handler tests |
| `back/internal/handlers/workers/service_test.go` | Service tests |
| `back/internal/db/models/worker_registration_token.go` | Registration token model |

### Files to modify

| File | Change |
|------|--------|
| `back/internal/checkworker/worker.go` | Replace `dbService` + `checkJobSvc` + `incidentSvc` with `WorkerBackend` |
| `back/internal/db/models/worker.go` | Add `Mode`, `OrganizationUID`, `TokenHash`, `TokenPrefix` fields |
| `back/internal/config/config.go` | Add `NodeRoleEdge`, `EdgeWorkerConfig`, `ShouldRunEdge()` |
| `back/internal/app/server.go` | Register `/api/v1/workers/*` routes, add edge worker startup path |
| `back/internal/middleware/auth.go` | Extend to recognize `spw_` worker tokens |
| `back/internal/regions/regions.go` | Add custom regions to resolution cascade |
| `back/internal/handlers/checks/service.go` | Encrypt configs in `reconcileCheckJobs` when org has encryption key |
| `back/internal/checkworker/checkjobsvc/service.go` | Filter out heartbeat checks for edge workers in `ClaimJobs` |

### Database migrations

| File | Purpose |
|------|---------|
| `back/internal/db/*/migrations/XXXXXX_edge_workers.up.sql` | Add worker columns + registration tokens table |
| `back/internal/db/*/migrations/XXXXXX_edge_workers.down.sql` | Rollback |

---

## Dashboard Changes

### Worker Management Page

New section under org settings:

1. **Edge Workers list**: Show registered edge workers with status, region, mode, last active
2. **Registration Tokens**: Create/list/revoke registration tokens
3. **Custom Regions**: Create/list/delete org-scoped regions
4. **Encryption Key**: Generate/rotate org encryption key

### Workers List Columns

| Column | Source |
|--------|--------|
| Name | `workers.name` |
| Slug | `workers.slug` |
| Mode | `workers.mode` (badge: "internal" / "edge") |
| Region | `workers.region` |
| Org | `workers.organization_uid` (null = "Global") |
| Status | Based on `last_active_at` (online if < 2 minutes ago) |
| Last Active | `workers.last_active_at` |

---

## Security Considerations

1. **TLS required**: Edge worker connections must use HTTPS. The master should reject non-TLS connections to `/api/v1/workers/*` endpoints in production.

2. **Token security**: Worker tokens (`spw_`) are stored as bcrypt hashes. The plaintext token is only shown once during registration.

3. **Registration token expiry**: Registration tokens have mandatory expiry dates. Unused tokens should be cleaned up.

4. **Org isolation**: Mode 2 workers can only claim jobs for their org. No API endpoint accepts an org UID parameter — the server derives the org exclusively from the worker record (set at registration time via the registration token). The worker has no way to request jobs from another org.

5. **Self-throttling by design**: The claim endpoint doesn't need rate limiting. Workers only call `/claim` when they have available runners, and the fetcher loop waits for runner completion or a timeout before re-polling. The lease mechanism prevents a worker from claiming more jobs than exist. If general API rate limiting is needed later (e.g., public API abuse prevention), it can be added as middleware — it's not edge-worker-specific.

6. **Encryption key scope**: Org encryption keys never leave the master server in plaintext except during worker registration/heartbeat (over TLS). They are stored as secret parameters (not readable via the normal parameters API).

7. **Config decryption**: Edge workers decrypt configs locally. The plaintext config only exists in memory during check execution.

8. **Worker token revocation**: Deleting a worker from the dashboard immediately invalidates its token (token lookup fails).

---

## Tests

### Unit Tests

- `crypto/config_encryption_test.go`: Encrypt/decrypt round-trip, invalid key, corrupted ciphertext, wrong key
- `backend/http_test.go`: Mock HTTP server, test all WorkerBackend methods, test retry logic, test auth headers
- `handlers/workers/service_test.go`: Registration flow, token validation, claim filtering, result submission
- `crypto/token_test.go`: Token generation, prefix validation, hash verification

### Integration Tests (testcontainers)

- Full registration flow: create registration token → register worker → claim jobs → submit results
- Mode 1 vs Mode 2 isolation: verify org-scoped workers only see their org's jobs
- Config encryption round-trip: create check with encryption → claim → verify encrypted → decrypt → verify config
- Token revocation: delete worker → verify token rejected
- Heartbeat: verify `last_active_at` updates, verify encryption key distribution
- Custom regions: create custom region → assign to check → verify edge worker claims it

### E2E Tests (Playwright)

- Admin creates registration token in dashboard
- Admin views edge workers list
- Admin manages custom regions
- Admin generates/rotates encryption key

---

## Sample Configurations

### Docker Compose — Edge Worker

```yaml
services:
  edge-worker:
    image: solidping:latest
    command: ["serve"]
    environment:
      SP_NODE_ROLE: edge
      SP_MASTER_URL: https://solidping.example.com
      SP_WORKER_TOKEN: spw_abc123...
      SP_REGION: customer-dc1
      SP_CHECK_WORKER_NB: 5
      SP_LOG_LEVEL: info
```

### First-Time Registration

```bash
# 1. Admin creates registration token (via API or dashboard)
TOKEN=$(curl -s -X POST \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Customer DC1","region":"customer-dc1","organizationUid":"org-uid-here","expiresAt":"2026-04-22T00:00:00Z"}' \
  'https://solidping.example.com/api/v1/admin/worker-registration-tokens' \
  | jq -r '.token')

# 2. Start edge worker with registration token
SP_NODE_ROLE=edge \
SP_MASTER_URL=https://solidping.example.com \
SP_WORKER_REGISTRATION_TOKEN=$TOKEN \
SP_REGION=customer-dc1 \
./solidping serve

# Worker registers, receives permanent token, saves it locally
# On subsequent starts, use SP_WORKER_TOKEN instead
```

---

## Implementation Phases

### Phase 1: WorkerBackend Abstraction

1. Create `WorkerBackend` interface and `DirectBackend` implementation
2. Refactor `CheckWorker` to use `WorkerBackend` instead of direct `dbService`/`checkJobSvc`/`incidentSvc` calls
3. Verify all existing tests pass (no behavior change)

### Phase 2: Server-Side Worker API

1. Database migration: worker columns + registration tokens table
2. Token generation and validation (`spr_`, `spw_`)
3. Auth middleware extension for worker tokens
4. Worker API handlers: register, heartbeat, claim, submit
5. Server-side tests

### Phase 3: Config Encryption

1. `crypto/config_encryption.go` — AES-256-GCM encrypt/decrypt
2. Encryption key management (generate, store, distribute)
3. Encrypt configs in `reconcileCheckJobs`
4. Decrypt in `DirectBackend.ClaimJobs` (for internal workers)
5. Tests for encryption round-trip

### Phase 4: HTTPBackend + Edge Worker Mode

1. `HTTPBackend` implementation with retry logic
2. Edge worker config and startup path
3. Config decryption in HTTPBackend
4. Integration test: full edge worker flow

### Phase 5: Custom Regions + Dashboard

1. Custom regions CRUD API
2. Region resolution update
3. Dashboard: workers list, registration tokens, custom regions, encryption key management

---

## Key Files Reference

| Existing File | Relevance |
|--------------|-----------|
| `back/internal/checkworker/worker.go` | Core worker — refactor to use WorkerBackend |
| `back/internal/checkworker/checkjobsvc/service.go` | Current ClaimJobs/ReleaseLease interface |
| `back/internal/db/models/worker.go` | Worker model to extend |
| `back/internal/db/models/check_job.go` | CheckJob model (has `encrypted` field) |
| `back/internal/config/config.go` | Config to extend with edge worker settings |
| `back/internal/app/server.go` | Route registration and worker startup |
| `back/internal/middleware/auth.go` | Auth middleware to extend |
| `back/internal/regions/regions.go` | Region resolution to update |
| `back/internal/handlers/checks/service.go` | `reconcileCheckJobs` — add encryption |

## Verification

1. **Internal workers unchanged**: After Phase 1, run `make test` — all existing tests must pass
2. **Worker API**: `curl` tests for register → claim → submit flow
3. **Encryption**: Unit tests for encrypt/decrypt round-trip
4. **Edge worker E2E**: Start edge worker against running master, verify jobs are claimed and results appear in dashboard
5. **Org isolation**: Verify Mode 2 worker cannot see other orgs' jobs
6. **Lint**: `make lint` passes
