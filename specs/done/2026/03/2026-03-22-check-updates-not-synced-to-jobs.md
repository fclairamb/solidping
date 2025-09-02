# Check Updates Not Synced to Check Jobs

**Date:** 2026-03-22
**Status:** todo
**Type:** bug fix

## Problem

When a check is updated (e.g., changing the URL, auth config, or any `config` field), the changes are **not propagated to `check_jobs`**. The `check_jobs` table has its own `type` and `config` columns that are only set at creation time.

### Root Cause

Two issues in `back/internal/handlers/checks/service.go`:

1. **`UpdateCheck()` (line 735)**: `reconcileCheckJobs()` is only called when `regions`, `period`, or `enabled` change. Config/type changes are ignored entirely.

2. **`reconcileCheckJobs()` (line 994-1012)**: Even when reconciliation runs, existing jobs only get their `period` updated. The `config` and `type` fields on existing `check_jobs` are never synced.

## Fix

### 1. Trigger reconciliation on config changes

In `UpdateCheck()` (~line 735), also trigger reconciliation when `config` changes:

```go
// Before (broken)
if req.Regions != nil || req.Period != nil || req.Enabled != nil {

// After (fixed)
if req.Regions != nil || req.Period != nil || req.Enabled != nil || req.Config != nil {
```

### 2. Sync config and type to existing jobs

In `reconcileCheckJobs()`, when an existing job is found for a region (~line 998-1012), also update `config` and `type`:

```go
if existing, ok := existingByRegion[region]; ok {
    // Update period, config, and type if changed
    needsUpdate := existing.Period != splitPeriod ||
        existing.Type != check.Type ||
        !reflect.DeepEqual(existing.Config, check.Config)

    if needsUpdate {
        if _, err := s.db.DB().NewUpdate().
            Model((*models.CheckJob)(nil)).
            Set("period = ?", splitPeriod).
            Set("type = ?", check.Type).
            Set("config = ?", check.Config).
            Set("updated_at = ?", time.Now()).
            Where("uid = ?", existing.UID).
            Exec(ctx); err != nil {
            return fmt.Errorf("failed to update check job: %w", err)
        }
    }
}
```

Apply the same logic in the "no regions" branch (~line 955-970) — instead of deleting and recreating, update the existing job's config/type/period in-place (or keep the delete+recreate approach but ensure it picks up current values, which it already does via `check.Type` and `check.Config`).

### 3. Add a test

Add a test in `service_test.go` that:

1. Creates a check with an initial config (e.g., HTTP check with URL `https://example.com`)
2. Updates the check's config (e.g., change URL to `https://updated.com`)
3. Verifies the corresponding `check_jobs` row(s) have the updated config
4. Also test updating `type` if applicable

Example test structure:
```go
func TestUpdateCheck_SyncsConfigToJobs(t *testing.T) {
    t.Parallel()
    r := require.New(t)

    // Create check
    // ...

    // Update check config
    // ...

    // Verify check_jobs have updated config
    jobs, err := db.ListCheckJobsByCheckUID(ctx, check.UID)
    r.NoError(err)
    r.Len(jobs, 1)
    r.Equal(updatedConfig, jobs[0].Config)
}
```

## Files to Modify

- `back/internal/handlers/checks/service.go` — Fix `UpdateCheck()` trigger condition and `reconcileCheckJobs()` sync logic
- `back/internal/handlers/checks/service_test.go` — Add test for config sync
