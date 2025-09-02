# UID or Slug Support in Checks API

## Requirement

The checks API endpoints shall accept either a UID (UUID) or a slug interchangeably when identifying a check resource.

### API Endpoint Pattern
```
/api/v1/orgs/$org/checks/{uid-or-slug}
```

The `{uid-or-slug}` parameter should accept:
- **UID**: A valid UUID v4 (e.g., `550e8400-e29b-41d4-a716-446655440000`)
- **Slug**: A human-readable identifier (e.g., `website-uptime`, `api-health`)

### Examples
```bash
# Both of these should work for the same check:
GET /api/v1/orgs/default/checks/550e8400-e29b-41d4-a716-446655440000
GET /api/v1/orgs/default/checks/website-uptime
```

## Constraint: Slug Format

**Slugs MUST NOT be allowed to have the format of a UUID.**

### Rationale
To avoid ambiguity when resolving check identifiers, slugs that look like UUIDs must be rejected during creation/update. This ensures the API can reliably determine whether an identifier is a UID or a slug.

### Validation Rules
- ✅ Valid slugs: `website-uptime`, `api-health-check`, `db-monitor`
- ❌ Invalid slugs: `550e8400-e29b-41d4-a716-446655440000` (looks like UUID)
- ❌ Invalid slugs: `a1b2c3d4-e5f6-1234-5678-9abcdef01234` (matches UUID pattern)

## Implementation Details

### 1. Affected API Endpoints

All endpoints that currently accept check UID in path parameters:

- `GET /api/v1/orgs/$org/checks/{uid-or-slug}` - Get single check
- `PATCH /api/v1/orgs/$org/checks/{uid-or-slug}` - Update check
- `DELETE /api/v1/orgs/$org/checks/{uid-or-slug}` - Delete check
- `POST /api/v1/orgs/$org/checks/{uid-or-slug}/pause` - Pause check
- `POST /api/v1/orgs/$org/checks/{uid-or-slug}/resume` - Resume check
- Any other check-specific endpoints

### 2. Database Schema

**Assumptions:**
- The `checks` table has a `uid` column (UUID, primary key)
- The `checks` table has a `slug` column (text, nullable or required)
- Both `uid` and `slug` must be unique within an organization

**Required indexes:**
```sql
-- Ensure slug lookups are fast
CREATE INDEX IF NOT EXISTS idx_checks_org_slug ON checks(org_uid, slug) WHERE slug IS NOT NULL;
```

### 3. Validation Logic

**UUID Detection:**
```go
// Use standard UUID parsing to detect if identifier is a UUID
func isUUID(s string) bool {
    _, err := uuid.Parse(s)
    return err == nil
}
```

**Slug Validation (on create/update):**
```go
// Reject slugs that look like UUIDs
func validateSlug(slug string) error {
    if slug == "" {
        return nil // Allow empty slug if it's optional
    }

    if isUUID(slug) {
        return &ValidationError{
            Code:   "VALIDATION_ERROR",
            Title:  "Invalid slug format",
            Detail: "Slug cannot be a valid UUID format to avoid ambiguity",
        }
    }

    // Add other slug validation rules if needed
    // e.g., alphanumeric + hyphens only, length limits, etc.

    return nil
}
```

**Check Resolution Logic:**
```go
// In handler methods that accept {uid-or-slug} parameter
func (h *CheckHandler) getCheckByIdentifier(ctx context.Context, orgUID uuid.UUID, identifier string) (*Check, error) {
    if isUUID(identifier) {
        // Parse as UUID and lookup by UID
        checkUID, _ := uuid.Parse(identifier)
        return h.repo.GetCheckByUID(ctx, orgUID, checkUID)
    }

    // Otherwise, lookup by slug
    return h.repo.GetCheckBySlug(ctx, orgUID, identifier)
}
```

### 4. Repository Layer Changes

**Add new method to check repository:**
```go
type CheckRepository interface {
    // Existing methods...
    GetCheckByUID(ctx context.Context, orgUID, checkUID uuid.UUID) (*Check, error)

    // New method needed:
    GetCheckBySlug(ctx context.Context, orgUID uuid.UUID, slug string) (*Check, error)
}
```

**SQL Query:**
```sql
SELECT * FROM checks
WHERE org_uid = $1 AND slug = $2
LIMIT 1;
```

### 5. Error Handling

**When check not found:**
```json
{
  "code": "CHECK_NOT_FOUND",
  "title": "Check not found",
  "detail": "No check found with identifier: website-uptime"
}
```

**When slug validation fails:**
```json
{
  "code": "VALIDATION_ERROR",
  "title": "Invalid slug format",
  "detail": "Slug cannot be a valid UUID format to avoid ambiguity"
}
```

### 6. Test Cases

**Unit Tests:**
```go
func TestIsUUID(t *testing.T) {
    tests := []struct {
        input    string
        expected bool
    }{
        {"550e8400-e29b-41d4-a716-446655440000", true},
        {"website-uptime", false},
        {"not-a-uuid", false},
        {"123e4567-e89b-12d3-a456-426614174000", true},
    }
    // ... test implementation
}

func TestValidateSlug(t *testing.T) {
    tests := []struct {
        slug      string
        shouldErr bool
    }{
        {"website-uptime", false},
        {"550e8400-e29b-41d4-a716-446655440000", true}, // UUID format
        {"api-health-check", false},
        {"", false}, // Empty allowed if optional
    }
    // ... test implementation
}
```

**Integration Tests:**
```go
func TestGetCheckByUIDOrSlug(t *testing.T) {
    // Setup: Create a check with slug "test-check"

    // Test 1: Get by UID should work
    resp := GET("/api/v1/orgs/default/checks/" + checkUID.String())
    assert.Equal(t, 200, resp.StatusCode)

    // Test 2: Get by slug should work
    resp = GET("/api/v1/orgs/default/checks/test-check")
    assert.Equal(t, 200, resp.StatusCode)

    // Test 3: Both should return same check
    // ... verify UIDs match
}

func TestCreateCheckWithUUIDSlug(t *testing.T) {
    // Test: Try to create check with UUID-format slug
    resp := POST("/api/v1/orgs/default/checks", {
        "slug": "550e8400-e29b-41d4-a716-446655440000",
        // ... other fields
    })
    assert.Equal(t, 400, resp.StatusCode)
    assert.Equal(t, "VALIDATION_ERROR", resp.Body.Code)
}
```

**E2E/API Tests:**
- Create check with valid slug → retrieve by slug → verify success
- Create check with valid slug → retrieve by UID → verify success
- Try to create check with UUID-format slug → verify 400 error
- Update check to UUID-format slug → verify 400 error
- Get non-existent slug → verify 404 with CHECK_NOT_FOUND
- Get non-existent UUID → verify 404 with CHECK_NOT_FOUND

### 7. Migration Considerations

**Backwards Compatibility:**
- All existing endpoints that use UID will continue to work
- No breaking changes to API contracts
- Slug support is additive

**Data Migration:**
- No database migration needed if slug column already exists
- If slug is being added, ensure existing checks can have NULL slugs or generate default slugs

### 8. Edge Cases

1. **Case sensitivity**: Determine if slug lookups should be case-sensitive
   - Recommendation: Case-sensitive (simpler, no ambiguity)

2. **Uniqueness scope**: Slugs must be unique within organization
   - Same slug can exist in different organizations

3. **Empty/null slugs**: Checks without slugs can only be accessed by UID

4. **Slug conflicts**: If creating/updating causes slug conflict, return `CONFLICT` error

### 9. CLI Client Updates

The CLI client should prefer slugs over UIDs for better UX:

```bash
# These should all work:
./sp checks get website-uptime
./sp checks get 550e8400-e29b-41d4-a716-446655440000
./sp checks delete website-uptime
./sp checks pause api-health
```

## Implementation Checklist

- [ ] Add `GetCheckBySlug` method to repository interface and implementations (PostgreSQL + SQLite)
- [ ] Add database index on `(org_uid, slug)` if not exists
- [ ] Implement `isUUID()` helper function
- [ ] Implement `validateSlug()` function with UUID rejection
- [ ] Update all check handlers to use `getCheckByIdentifier()` pattern
- [ ] Add slug validation to check creation/update endpoints
- [ ] Write unit tests for UUID detection and slug validation
- [ ] Write integration tests for repository methods
- [ ] Write API tests for all affected endpoints
- [ ] Update CLI client to support slugs
- [ ] Update API documentation
- [ ] Test with real data
