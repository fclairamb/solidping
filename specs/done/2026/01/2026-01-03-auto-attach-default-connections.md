# Auto-Attach Default Integration Connections

## Overview

Automatically link newly created checks to integration connections marked as default within the organization. This ensures new checks inherit notification settings without manual configuration.

## Implementation Status

| Component | Status |
|-----------|--------|
| Add `organization_uid` to `check_connections` | Not Yet Implemented |
| Auto-attach logic on check creation | Not Yet Implemented |
| Migration for schema change | Not Yet Implemented |

## Goals

1. Add `organization_uid` column to `check_connections` table for better data integrity and querying
2. Automatically create `check_connection` entries when a check is created
3. Only auto-attach connections where `is_default = true` and `enabled = true`
4. Scope auto-attachment to the same organization as the check

---

## Data Model Changes

### `check_connections` table (Schema Update Required)

The schema change should be done in the existing migration file, not a new one.

**Current Schema:**
| Column | Type | Description |
|--------|------|-------------|
| `uid` | UUID | Primary key |
| `check_uid` | UUID | Foreign key to checks |
| `connection_uid` | UUID | Foreign key to integration_connections |
| `created_at` | TIMESTAMP | When the association was created |

**Updated Schema:**
| Column | Type | Description |
|--------|------|-------------|
| `uid` | UUID | Primary key |
| `check_uid` | UUID | Foreign key to checks |
| `connection_uid` | UUID | Foreign key to integration_connections |
| **`organization_uid`** | **UUID** | **Foreign key to organizations** |
| `created_at` | TIMESTAMP | When the association was created |

**Constraints:**
- Unique on `(check_uid, connection_uid)`
- `organization_uid` must match the organization of both `check_uid` and `connection_uid`

**Indexes:**
- `check_connections_check_connection_idx` - Unique on `(check_uid, connection_uid)` (existing)
- `check_connections_connection_idx` - Index on `connection_uid` (existing)
- **NEW:** `check_connections_org_idx` - Index on `organization_uid` for efficient org-scoped queries

---

## Auto-Attachment Logic

### When Creating a Check

```go
// Pseudocode for check creation
func (s *CheckService) CreateCheck(ctx context.Context, orgUID string, req *CreateCheckRequest) (*Check, error) {
    // 1. Create the check
    check := &Check{
        UID:             generateUID(),
        OrganizationUID: orgUID,
        Name:            req.Name,
        // ... other fields
    }

    if err := s.db.InsertCheck(ctx, check); err != nil {
        return nil, err
    }

    // 2. Find all default connections for this organization
    defaultConnections, err := s.db.ListIntegrationConnections(ctx, &ListConnectionsFilter{
        OrganizationUID: orgUID,
        IsDefault:       true,
        Enabled:         true,
    })
    if err != nil {
        return nil, err
    }

    // 3. Create check_connections for each default connection
    for _, conn := range defaultConnections {
        checkConn := &CheckConnection{
            UID:             generateUID(),
            CheckUID:        check.UID,
            ConnectionUID:   conn.UID,
            OrganizationUID: orgUID,
            CreatedAt:       time.Now(),
        }

        if err := s.db.InsertCheckConnection(ctx, checkConn); err != nil {
            // Log error but don't fail check creation
            log.Error("Failed to auto-attach connection", "error", err, "connection_uid", conn.UID)
        }
    }

    return check, nil
}
```

### Query for Default Connections

```sql
-- PostgreSQL
SELECT * FROM integration_connections
WHERE organization_uid = $1
  AND is_default = true
  AND enabled = true
  AND deleted_at IS NULL;

-- SQLite
SELECT * FROM integration_connections
WHERE organization_uid = ?
  AND is_default = 1
  AND enabled = 1
  AND deleted_at IS NULL;
```

---

## Migration Strategy

### Migration File: `20260103000002_check_connections_org_uid.up.sql`

**PostgreSQL:**
```sql
-- Add organization_uid column
ALTER TABLE check_connections
  ADD COLUMN organization_uid UUID;

-- Backfill organization_uid from checks table
UPDATE check_connections cc
SET organization_uid = c.organization_uid
FROM checks c
WHERE cc.check_uid = c.uid;

-- Make column NOT NULL after backfill
ALTER TABLE check_connections
  ALTER COLUMN organization_uid SET NOT NULL;

-- Add foreign key constraint
ALTER TABLE check_connections
  ADD CONSTRAINT check_connections_organization_fk
  FOREIGN KEY (organization_uid) REFERENCES organizations(uid) ON DELETE CASCADE;

-- Add index for organization queries
CREATE INDEX check_connections_org_idx ON check_connections (organization_uid);

-- Add comment
COMMENT ON COLUMN check_connections.organization_uid IS 'Organization that owns this check-connection association (denormalized for query performance)';
```

**SQLite:**
```sql
-- SQLite doesn't support ADD COLUMN with NOT NULL + FK directly
-- Need to recreate the table

-- Create new table with organization_uid
CREATE TABLE check_connections_new (
  uid               TEXT PRIMARY KEY,
  check_uid         TEXT NOT NULL REFERENCES checks(uid) ON DELETE CASCADE,
  connection_uid    TEXT NOT NULL REFERENCES integration_connections(uid) ON DELETE CASCADE,
  organization_uid  TEXT NOT NULL REFERENCES organizations(uid) ON DELETE CASCADE,
  created_at        TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Copy data with organization_uid from checks
INSERT INTO check_connections_new (uid, check_uid, connection_uid, organization_uid, created_at)
SELECT
  cc.uid,
  cc.check_uid,
  cc.connection_uid,
  c.organization_uid,
  cc.created_at
FROM check_connections cc
JOIN checks c ON cc.check_uid = c.uid;

-- Drop old table
DROP TABLE check_connections;

-- Rename new table
ALTER TABLE check_connections_new RENAME TO check_connections;

-- Recreate indexes
CREATE UNIQUE INDEX check_connections_check_connection_idx ON check_connections (check_uid, connection_uid);
CREATE INDEX check_connections_connection_idx ON check_connections (connection_uid);
CREATE INDEX check_connections_org_idx ON check_connections (organization_uid);
```

### Down Migration: `20260103000002_check_connections_org_uid.down.sql`

**PostgreSQL:**
```sql
-- Drop index
DROP INDEX IF EXISTS check_connections_org_idx;

-- Drop foreign key
ALTER TABLE check_connections DROP CONSTRAINT IF EXISTS check_connections_organization_fk;

-- Drop column
ALTER TABLE check_connections DROP COLUMN organization_uid;
```

**SQLite:**
```sql
-- Recreate table without organization_uid
CREATE TABLE check_connections_new (
  uid               TEXT PRIMARY KEY,
  check_uid         TEXT NOT NULL REFERENCES checks(uid) ON DELETE CASCADE,
  connection_uid    TEXT NOT NULL REFERENCES integration_connections(uid) ON DELETE CASCADE,
  created_at        TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Copy data without organization_uid
INSERT INTO check_connections_new (uid, check_uid, connection_uid, created_at)
SELECT uid, check_uid, connection_uid, created_at
FROM check_connections;

-- Drop new table
DROP TABLE check_connections;

-- Rename
ALTER TABLE check_connections_new RENAME TO check_connections;

-- Recreate original indexes
CREATE UNIQUE INDEX check_connections_check_connection_idx ON check_connections (check_uid, connection_uid);
CREATE INDEX check_connections_connection_idx ON check_connections (connection_uid);
```

---

## Code Changes

### Model Update

File: `internal/db/models/check_connection.go` (create if doesn't exist)

```go
package models

import "time"

// CheckConnection links a check to an integration connection for notifications.
type CheckConnection struct {
    UID             string    `bun:"uid,pk"`
    CheckUID        string    `bun:"check_uid,notnull"`
    ConnectionUID   string    `bun:"connection_uid,notnull"`
    OrganizationUID string    `bun:"organization_uid,notnull"`
    CreatedAt       time.Time `bun:"created_at,notnull"`
}
```

### Service Interface Update

Add methods to DB service interface:

```go
// In internal/db/service.go
type Service interface {
    // ... existing methods

    // CheckConnections
    InsertCheckConnection(ctx context.Context, conn *models.CheckConnection) error
    ListCheckConnections(ctx context.Context, checkUID string) ([]*models.CheckConnection, error)
    DeleteCheckConnection(ctx context.Context, checkUID, connectionUID string) error

    // IntegrationConnections (add filter support)
    ListIntegrationConnections(ctx context.Context, filter *ListConnectionsFilter) ([]*models.IntegrationConnection, error)
}

// ListConnectionsFilter filters integration connections
type ListConnectionsFilter struct {
    OrganizationUID string
    Type            *string
    IsDefault       *bool
    Enabled         *bool
}
```

### Check Service Update

File: `internal/handlers/checks/service.go`

```go
func (s *Service) CreateCheck(ctx context.Context, orgUID string, req *CreateCheckRequest) (*models.Check, error) {
    // 1. Create the check (existing logic)
    check := &models.Check{
        // ... populate fields
    }

    if err := s.db.InsertCheck(ctx, check); err != nil {
        return nil, err
    }

    // 2. Auto-attach default connections
    if err := s.autoAttachDefaultConnections(ctx, check); err != nil {
        // Log error but don't fail check creation
        s.logger.Error("Failed to auto-attach default connections",
            "error", err,
            "check_uid", check.UID,
            "org_uid", orgUID,
        )
    }

    return check, nil
}

func (s *Service) autoAttachDefaultConnections(ctx context.Context, check *models.Check) error {
    // Find all default connections
    connections, err := s.db.ListIntegrationConnections(ctx, &db.ListConnectionsFilter{
        OrganizationUID: check.OrganizationUID,
        IsDefault:       ptr(true),
        Enabled:         ptr(true),
    })
    if err != nil {
        return fmt.Errorf("list default connections: %w", err)
    }

    // Create check_connections
    for _, conn := range connections {
        checkConn := &models.CheckConnection{
            UID:             generateUID(),
            CheckUID:        check.UID,
            ConnectionUID:   conn.UID,
            OrganizationUID: check.OrganizationUID,
            CreatedAt:       time.Now(),
        }

        if err := s.db.InsertCheckConnection(ctx, checkConn); err != nil {
            s.logger.Warn("Failed to create check connection",
                "error", err,
                "connection_uid", conn.UID,
            )
            // Continue with other connections
        }
    }

    return nil
}

func ptr[T any](v T) *T {
    return &v
}
```

---

## Benefits

1. **Data Integrity**: `organization_uid` on `check_connections` ensures all associations are within the same organization
2. **Performance**: Direct organization queries on `check_connections` without joining through `checks` table
3. **User Experience**: New checks automatically inherit notification settings from default connections
4. **Flexibility**: Users can still manually add/remove connections after check creation
5. **Maintainability**: Denormalized `organization_uid` makes queries simpler and faster

---

## Testing

### Unit Tests

```go
func TestAutoAttachDefaultConnections(t *testing.T) {
    // Setup: Create org with 2 default connections (1 enabled, 1 disabled)
    // Action: Create a new check
    // Assert: Check has exactly 1 check_connection (to the enabled default)
}

func TestNoDefaultConnections(t *testing.T) {
    // Setup: Create org with no default connections
    // Action: Create a new check
    // Assert: Check has 0 check_connections
}

func TestMultipleOrganizations(t *testing.T) {
    // Setup: Create 2 orgs, each with different default connections
    // Action: Create check in org1
    // Assert: Check only has connections from org1, not org2
}
```

### Integration Tests

```bash
# Login
TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"email":"admin@solidping.com","password":"solidpass"}' \
  'http://localhost:4000/api/v1/orgs/default/auth/login' | jq -r '.accessToken')

# Create a default connection
CONNECTION_UID=$(curl -s -X POST \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"type":"webhook","name":"Default Webhook","isDefault":true,"enabled":true,"settings":{"url":"https://example.com/webhook"}}' \
  'http://localhost:4000/api/v1/orgs/default/connections' | jq -r '.uid')

# Create a check
CHECK_UID=$(curl -s -X POST \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Test Check","slug":"test-check","type":"http","config":{"url":"https://example.com"}}' \
  'http://localhost:4000/api/v1/orgs/default/checks' | jq -r '.uid')

# Verify check has connection attached
curl -s -H "Authorization: Bearer $TOKEN" \
  "http://localhost:4000/api/v1/orgs/default/checks/$CHECK_UID/connections" | jq '.'
# Expected: Array with 1 connection matching CONNECTION_UID
```

---

## Implementation Order

1. Create migration files for `check_connections.organization_uid`
2. Run migrations on dev database
3. Update `CheckConnection` model
4. Implement `InsertCheckConnection` in DB service
5. Implement `ListIntegrationConnections` with filter support
6. Add `autoAttachDefaultConnections` to check service
7. Call auto-attach in `CreateCheck` handler
8. Add unit tests for auto-attach logic
9. Add integration tests
10. Update API documentation

---

## Related Specs

- [2026-01-01-notifiers.md](2026-01-01-notifiers.md) - Notification system overview
- [2025-12-26-incidents.md](2025-12-26-incidents.md) - Incident management

---

## Open Questions

1. **Q:** Should we auto-attach on check update if new default connections are added later?
   **A:** No. Only attach on creation. Users can manually add connections later.

2. **Q:** What if auto-attachment fails?
   **A:** Log the error but don't fail check creation. The check should still be created successfully.

3. **Q:** Should we validate that `check_uid` and `connection_uid` are in the same organization?
   **A:** Yes, add validation in the service layer before insert.
