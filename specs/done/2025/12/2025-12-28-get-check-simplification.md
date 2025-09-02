# GetCheck Function Simplification

## Current State

Currently, the database layer has two separate functions for retrieving checks:

1. **`GetCheck(ctx, uid)`** - Retrieves a check by its UID only (no org validation)
2. **`GetCheckBySlug(ctx, orgUID, slug)`** - Retrieves a check by organization UID and slug

## Problem

- `GetCheck` does not require an organization UID, which could allow cross-tenant access if not properly guarded at the handler level
- Having two functions creates redundancy and inconsistent access patterns
- The caller must decide which function to use based on whether they have a UID or slug

## Proposed Solution

Replace both functions with:

```go
// GetCheck retrieves a check by its UID, always requiring organization context
func GetCheck(ctx context.Context, orgUID, checkUID string) (*models.Check, error)

// GetCheckByUidOrSlug retrieves a check by either UID or slug, auto-detecting the identifier type
func GetCheckByUidOrSlug(ctx context.Context, orgUID, identifier string) (*models.Check, error)
```

Where:
- `GetCheck` - Strict lookup by UID, always validates organization ownership
- `GetCheckByUidOrSlug` - Flexible lookup that auto-detects if `identifier` is a UUID or slug

## Files to Update

### 1. Database Interface
**File:** `back/internal/db/service.go`

Change:
```go
GetCheck(ctx context.Context, uid string) (*models.Check, error)
GetCheckBySlug(ctx context.Context, orgUID, slug string) (*models.Check, error)
```
To:
```go
GetCheck(ctx context.Context, orgUID, checkUID string) (*models.Check, error)
GetCheckByUidOrSlug(ctx context.Context, orgUID, identifier string) (*models.Check, error)
```

### 2. SQLite Implementation
**File:** `back/internal/db/sqlite/sqlite.go`

Update `GetCheck` to require `orgUID`:
```go
func (s *Service) GetCheck(ctx context.Context, orgUID, checkUID string) (*models.Check, error) {
    check := new(models.Check)
    err := s.db.NewSelect().
        Model(check).
        Where("uid = ?", checkUID).
        Where("organization_uid = ?", orgUID).
        Where("deleted_at IS NULL").
        Scan(ctx)
    if err != nil {
        return nil, err
    }
    return check, nil
}
```

Rename `GetCheckBySlug` to `GetCheckByUidOrSlug` with UUID detection:
```go
func (s *Service) GetCheckByUidOrSlug(ctx context.Context, orgUID, identifier string) (*models.Check, error) {
    check := new(models.Check)
    query := s.db.NewSelect().
        Model(check).
        Where("organization_uid = ?", orgUID).
        Where("deleted_at IS NULL")

    if _, err := uuid.Parse(identifier); err == nil {
        query = query.Where("uid = ?", identifier)
    } else {
        query = query.Where("slug = ?", identifier)
    }

    if err := query.Scan(ctx); err != nil {
        return nil, err
    }
    return check, nil
}
```

### 3. PostgreSQL Implementation
**File:** `back/internal/db/postgres/postgres.go`

Same changes as SQLite implementation.

### 4. Checks Handler Service
**File:** `back/internal/handlers/checks/service.go`

Replace UUID detection pattern with `GetCheckByUidOrSlug`:
```go
// Before
if isUUID(identifier) {
    check, err = s.db.GetCheck(ctx, identifier)
} else {
    check, err = s.db.GetCheckBySlug(ctx, org.UID, identifier)
}

// After
check, err = s.db.GetCheckByUidOrSlug(ctx, org.UID, identifier)
```

For direct UID lookups (e.g., after updates), use `GetCheck`:
```go
// Before
updatedCheck, err := s.db.GetCheck(ctx, check.UID)

// After
updatedCheck, err := s.db.GetCheck(ctx, org.UID, check.UID)
```

### 5. Badges Handler Service
**File:** `back/internal/handlers/badges/service.go`

```go
// Before
if isUUID(checkIdentifier) {
    check, err = s.dbSvc.GetCheck(ctx, checkIdentifier)
} else {
    check, err = s.dbSvc.GetCheckBySlug(ctx, org.UID, checkIdentifier)
}

// After
check, err = s.dbSvc.GetCheckByUidOrSlug(ctx, org.UID, checkIdentifier)
```

### 6. Results Handler Service
**File:** `back/internal/handlers/results/service.go`

```go
// Before
check, err := s.db.GetCheckBySlug(ctx, orgUID, id)

// After
check, err := s.db.GetCheckByUidOrSlug(ctx, orgUID, id)
```

### 7. Check Worker
**File:** `back/internal/checkworker/worker.go`

```go
// Before
existing, err := r.dbService.GetCheckBySlug(ctx, r.defaultOrgUID, slug)

// After
existing, err := r.dbService.GetCheckByUidOrSlug(ctx, r.defaultOrgUID, slug)
```

### 8. Job Worker
**File:** `back/internal/jobs/jobworker/worker.go`

```go
// Before
existing, err := w.dbService.GetCheckBySlug(ctx, w.defaultOrgUID, slug)

// After
existing, err := w.dbService.GetCheckByUidOrSlug(ctx, w.defaultOrgUID, slug)
```

### 9. Database Tests
**File:** `back/internal/db/service_test.go`

Update tests to use new signatures and add tests for `GetCheckByUidOrSlug`.

## Implementation Order

1. Update the interface in `service.go`
2. Update both database implementations (SQLite and PostgreSQL)
3. Update all callers (handlers, workers)
4. Update tests
5. Run `make test` to verify all changes
6. Run `make lint` to ensure code quality
