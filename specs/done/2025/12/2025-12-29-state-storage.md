# State Storage with Distributed Locking

## Problem Statement

SolidPing needs a flexible key-value state storage mechanism that can:

1. **Store notification state**: When an incident is created, we post to Slack/Discord. When it escalates or resolves, we need to update the same thread/message. This requires storing platform-specific identifiers (channel ID, thread timestamp, message ID).

2. **Distributed locking**: Prevent concurrent operations on the same resource across multiple workers (e.g., avoid duplicate notifications, coordinate job execution).

## Use Cases

### State Storage
- **Slack Thread Continuity**: Store channel_id + thread_ts to reply in the same thread when incident updates
- **Discord Message Updates**: Store channel_id + message_id to edit or reply to the original alert
- **Webhook Deduplication**: Store last sent payload hash to avoid duplicate alerts

### Distributed Locking
- **Notification Deduplication**: Acquire lock before sending notification to prevent duplicates from concurrent workers
- **Job Coordination**: Prevent multiple workers from processing the same job simultaneously
- **Rate Limiting**: Implement per-resource rate limits across distributed workers

## Proposed Solution

Create a `state_entries` table for key-value storage with atomic operations for distributed coordination.

### Schema

#### PostgreSQL

```sql
CREATE TABLE state_entries (
    uid VARCHAR(36) PRIMARY KEY,
    organization_uid VARCHAR(36) NOT NULL,
    key VARCHAR(255) NOT NULL,
    value JSONB,                          -- NULL allowed for lock-only entries
    expires_at TIMESTAMPTZ,               -- NULL = never expires

    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at TIMESTAMPTZ,

    UNIQUE(organization_uid, key)
);

CREATE INDEX idx_state_entries_expires ON state_entries(expires_at)
    WHERE expires_at IS NOT NULL AND deleted_at IS NULL;

CREATE INDEX idx_state_entries_org ON state_entries(organization_uid)
    WHERE deleted_at IS NULL;
```

#### SQLite

```sql
CREATE TABLE state_entries (
    uid TEXT PRIMARY KEY,
    organization_uid TEXT NOT NULL,
    key TEXT NOT NULL CHECK(length(key) <= 255),
    value TEXT,                           -- JSON stored as text
    expires_at TEXT,                      -- ISO 8601 timestamp

    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    deleted_at TEXT,

    UNIQUE(organization_uid, key)
);

CREATE INDEX idx_state_entries_expires ON state_entries(expires_at)
    WHERE expires_at IS NOT NULL AND deleted_at IS NULL;

CREATE INDEX idx_state_entries_org ON state_entries(organization_uid)
    WHERE deleted_at IS NULL;
```

### Go Model

```go
type StateEntry struct {
    UID             string     `bun:"uid,pk,type:varchar(36)"`
    OrganizationUID string     `bun:"organization_uid,notnull"`
    Key             string     `bun:"key,notnull"`
    Value           *JSONMap   `bun:"value,type:jsonb"`
    ExpiresAt       *time.Time `bun:"expires_at"`
    CreatedAt       time.Time  `bun:"created_at,notnull"`
    UpdatedAt       time.Time  `bun:"updated_at,notnull"`
    DeletedAt       *time.Time `bun:"deleted_at"`
}

func NewStateEntry(orgUID, key string) *StateEntry {
    now := time.Now()
    return &StateEntry{
        UID:             uuid.New().String(),
        OrganizationUID: orgUID,
        Key:             key,
        CreatedAt:       now,
        UpdatedAt:       now,
    }
}
```

### Key Namespacing

Use colon-separated keys to create logical namespaces:

```go
// Helper function
func StateKey(parts ...string) string {
    return strings.Join(parts, ":")
}

// Examples:
StateKey("incident", incidentUID, "slack_notification")  // "incident:abc123:slack_notification"
StateKey("check", checkUID, "last_result_hash")          // "check:xyz789:last_result_hash"
StateKey("org", "rate_limit", "slack")                   // "org:rate_limit:slack"
```

### Service Interface

```go
type StateService interface {
    // Scoping
    WithPrefix(prefix string) StateService

    // Basic CRUD operations
    Get(ctx context.Context, orgUID, key string) (*StateEntry, error)
    Set(ctx context.Context, orgUID, key string, value *JSONMap, ttl *time.Duration) error
    Delete(ctx context.Context, orgUID, key string) error
    List(ctx context.Context, orgUID, pattern string) ([]*StateEntry, error)

    // Atomic operations
    GetOrCreate(ctx context.Context, orgUID, key string, defaultValue *JSONMap, ttl *time.Duration) (*StateEntry, bool, error)
    SetIfNotExists(ctx context.Context, orgUID, key string, value *JSONMap, ttl *time.Duration) (bool, error)

    // Maintenance
    DeleteExpired(ctx context.Context) (int64, error)
}
```

**Method descriptions:**

- `WithPrefix`: Returns a new `StateService` that automatically prepends the given prefix to all keys. Prefixes are cumulative (calling `WithPrefix("a").WithPrefix("b")` results in prefix `"a:b:"`).
- `Get`: Returns entry or `nil` if not found. Pure read, no side effects.
- `Set`: Creates or updates entry. TTL is relative duration (e.g., `24 * time.Hour`).
- `Delete`: Soft-deletes entry.
- `List`: Returns all entries matching the pattern within the current prefix scope. Pattern uses SQL LIKE syntax (`%` for any characters, `_` for single character). Empty pattern returns all entries under the prefix.
- `GetOrCreate`: Returns existing entry or creates new one. Returns `(entry, created, error)`.
- `SetIfNotExists`: Creates only if key doesn't exist. Returns `(created, error)`.
- `DeleteExpired`: Removes entries past their `expires_at`. Returns count deleted.

### Cleanup Mechanism

Expired entries must be periodically cleaned up:

```go
// In worker/scheduler, run periodically (e.g., every 5 minutes)
func (w *Worker) cleanupExpiredState(ctx context.Context) {
    count, err := w.stateService.DeleteExpired(ctx)
    if err != nil {
        log.Error("Failed to cleanup expired state", "error", err)
        return
    }
    if count > 0 {
        log.Info("Cleaned up expired state entries", "count", count)
    }
}
```

The `DeleteExpired` implementation:

```sql
-- PostgreSQL
UPDATE state_entries
SET deleted_at = NOW()
WHERE expires_at < NOW()
  AND expires_at IS NOT NULL
  AND deleted_at IS NULL;

-- SQLite
UPDATE state_entries
SET deleted_at = datetime('now')
WHERE expires_at < datetime('now')
  AND expires_at IS NOT NULL
  AND deleted_at IS NULL;
```

The `List` implementation (with prefix "incident:" and pattern "%:slack"):

```sql
-- PostgreSQL
SELECT * FROM state_entries
WHERE organization_uid = $1
  AND key LIKE 'incident:%:slack'
  AND deleted_at IS NULL
  AND (expires_at IS NULL OR expires_at > NOW())
ORDER BY key;

-- SQLite
SELECT * FROM state_entries
WHERE organization_uid = ?
  AND key LIKE 'incident:%:slack'
  AND deleted_at IS NULL
  AND (expires_at IS NULL OR expires_at > datetime('now'))
ORDER BY key;
```

### Example Usage

#### Storing Slack Thread Info

```go
key := StateKey("incident", incidentUID, "slack_notification")

// After sending initial Slack notification (no expiry)
err := stateService.Set(ctx, orgUID, key, &models.JSONMap{
    "channel_id": "C1234567890",
    "thread_ts":  "1234567890.123456",
    "message_ts": "1234567890.123456",
}, nil)

// When incident updates, retrieve thread info
entry, err := stateService.Get(ctx, orgUID, key)
if err != nil {
    return err
}
if entry != nil && entry.Value != nil {
    threadTS := (*entry.Value)["thread_ts"].(string)
    // Reply in same thread
}
```

#### Preventing Duplicate Notifications (PostgreSQL)

```go
key := StateKey("incident", incidentUID, "notification_sent")
ttl := 24 * time.Hour

// Atomic check-and-set
created, err := stateService.SetIfNotExists(ctx, orgUID, key, &models.JSONMap{
    "sent_at": time.Now(),
}, &ttl)
if err != nil {
    return err
}

if !created {
    // Already notified by another worker
    return nil
}

// We won the race - send notification
if err := sendSlackNotification(incident); err != nil {
    // Delete the entry so retry can occur
    stateService.Delete(ctx, orgUID, key)
    return err
}
```

#### Using GetOrCreate for Counters

```go
key := StateKey("org", "daily_notifications")
ttl := 24 * time.Hour

entry, created, err := stateService.GetOrCreate(ctx, orgUID, key, &models.JSONMap{
    "count": 0,
}, &ttl)
if err != nil {
    return err
}

count := int((*entry.Value)["count"].(float64))
if count >= maxDailyNotifications {
    return ErrRateLimited
}

// Increment counter
stateService.Set(ctx, orgUID, key, &models.JSONMap{
    "count": count + 1,
}, &ttl)
```

#### Using WithPrefix for Scoped Access

```go
// Create a scoped service for incident-related state
incidentState := stateService.WithPrefix("incident")

// All operations are automatically prefixed with "incident:"
// This stores at key "incident:abc123:slack"
err := incidentState.Set(ctx, orgUID, "abc123:slack", &models.JSONMap{
    "channel_id": "C1234567890",
    "thread_ts":  "1234567890.123456",
}, nil)

// Further scoping for a specific incident
incidentABC := incidentState.WithPrefix("abc123")
// This stores at key "incident:abc123:discord"
err = incidentABC.Set(ctx, orgUID, "discord", &models.JSONMap{
    "channel_id": "123456789",
    "message_id": "987654321",
}, nil)
```

#### Listing Entries with Pattern Matching

```go
// List all Slack notification state for an organization
incidentState := stateService.WithPrefix("incident")
entries, err := incidentState.List(ctx, orgUID, "%:slack")
// Returns entries with keys like "incident:abc123:slack", "incident:xyz789:slack"

// List all state for a specific incident
incidentABC := stateService.WithPrefix("incident:abc123")
entries, err := incidentABC.List(ctx, orgUID, "")
// Returns all entries under "incident:abc123:" prefix

// List with wildcard pattern
entries, err := stateService.List(ctx, orgUID, "check:%:last_result%")
// Returns entries matching the pattern
```

## Implementation Steps

1. Create migrations for `state_entries` table (both PostgreSQL and SQLite)
2. Add `StateEntry` model to `models/models.go`
3. Add state methods to `db.Service` interface
4. Implement in both `postgres/postgres.go` and `sqlite/sqlite.go`
5. Add cleanup job to worker scheduler
6. Update notification handlers to use state storage

## Out of Scope

- Notification configuration (handled by `parameters` table)
- Historical notification logs (separate feature)
