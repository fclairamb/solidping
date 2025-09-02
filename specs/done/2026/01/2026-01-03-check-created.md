# check.created Event Implementation

## Overview

The `check.created` event should be emitted from the service layer directly. This enables consistent event handling across all check creation paths:
- The startup job
- The API
- The Slack integration

## Current State Analysis

### Existing Event Infrastructure

The codebase already has a robust dual-purpose event system:

1. **Audit Log**: Events stored in database (`models.Event`) for historical tracking
2. **Real-Time Notifications**: `EventNotifier` pub/sub for immediate system reactions

Event types are already defined in `internal/db/models/event.go`:
```go
EventTypeCheckCreated  = "check.created"
EventTypeCheckUpdated  = "check.updated"
EventTypeCheckDeleted  = "check.deleted"
```

### Current Check Creation Paths

| Path | Location | Event Emitted? |
|------|----------|----------------|
| API | `handlers/checks/service.go:CreateCheck()` | No |
| Slack | `integrations/slack/service.go:502` | No (calls API service) |
| Startup Job | `jobs/jobtypes/job_startup.go:270` | Partial (notifies workers, no audit log) |

### Gap

Events are defined but **not being emitted** when checks are created. The startup job only notifies the `EventNotifier` to wake workers, but doesn't persist an audit log entry.

## Implementation Plan

### 1. Inject EventNotifier into checks.Service

**File**: `internal/handlers/checks/service.go`

```go
type Service struct {
    db            db.Service
    eventNotifier notifier.EventNotifier  // Add this
}

func NewService(db db.Service, eventNotifier notifier.EventNotifier) *Service {
    return &Service{
        db:            db,
        eventNotifier: eventNotifier,
    }
}
```

**File**: `internal/app/services/services.go`

Update service instantiation to inject the notifier.

### 2. Add emitEvent Helper Method

Follow the pattern established in `incidents/service.go:234-261`:

```go
func (s *Service) emitEvent(
    ctx context.Context,
    orgUID string,
    eventType models.EventType,
    check *models.Check,
    actorType models.ActorType,
    actorUID *string,
) error {
    event := models.NewEvent(orgUID, eventType, actorType)
    event.CheckUID = &check.UID
    event.ActorUID = actorUID
    event.Payload = models.JSONMap{
        "check_uid":  check.UID,
        "check_slug": check.Slug,
        "check_name": check.Name,
        "check_type": check.Type,
    }

    if err := s.db.CreateEvent(ctx, event); err != nil {
        return fmt.Errorf("failed to create event: %w", err)
    }

    // Notify workers to pick up the new check immediately
    if err := s.eventNotifier.Notify(ctx, string(eventType), "{}"); err != nil {
        slog.WarnContext(ctx, "failed to send real-time notification",
            "event_type", eventType,
            "error", err,
        )
        // Don't fail check creation for notification errors
    }

    return nil
}
```

### 3. Emit Event in CreateCheck()

**File**: `internal/handlers/checks/service.go` (after line ~371, after labels and connections are attached)

```go
// Emit check.created event
if err := s.emitEvent(ctx, orgUID, models.EventTypeCheckCreated, check,
    models.ActorTypeUser, userUID); err != nil {
    slog.WarnContext(ctx, "failed to emit check.created event", "error", err)
    // Don't fail check creation for event emission errors
}
```

### 4. Extract User Context

The handler has access to the authenticated user via JWT middleware. Add a helper or use existing patterns to extract `userUID` from context and pass it to the service.

Options:
- Pass `userUID` as parameter to `CreateCheck()`
- Extract from context in service (if context carries auth info)

### 5. Update Startup Job

**File**: `internal/jobs/jobtypes/job_startup.go`

The startup job creates checks directly via `jctx.DBService.CreateCheck()`. Options:
- Refactor to use `checks.Service.CreateCheck()` for consistency
- Or keep using DB directly but emit event via `jctx.EventNotifier` with `ActorTypeSystem`

Recommended: Emit event with system actor after each sample check creation:
```go
event := models.NewEvent(orgUID, models.EventTypeCheckCreated, models.ActorTypeSystem)
event.CheckUID = &check.UID
event.Payload = models.JSONMap{
    "check_slug": check.Slug,
    "check_name": check.Name,
    "check_type": check.Type,
    "source":     "startup_samples",
}
jctx.DBService.CreateEvent(ctx, event)
```

## Event Payload Schema

```json
{
  "check_uid": "string",
  "check_slug": "string",
  "check_name": "string",
  "check_type": "http|tcp|dns|...",
  "source": "api|slack|startup_samples (optional)"
}
```

## Actor Types

| Source | ActorType | ActorUID |
|--------|-----------|----------|
| API | `user` | Authenticated user's UID |
| Slack | `user` | Slack user's mapped UID (if available) |
| Startup Job | `system` | `nil` |

## Files to Modify

1. `internal/handlers/checks/service.go` - Add event emission
2. `internal/app/services/services.go` - Inject EventNotifier
3. `internal/jobs/jobtypes/job_startup.go` - Emit audit event
4. `internal/handlers/checks/handler.go` - Pass user context (if needed)

## Testing Strategy

1. **Unit Tests**
   - Mock `EventNotifier` and verify `Notify()` called
   - Mock `db.Service.CreateEvent()` and verify event structure

2. **Integration Tests** (`test/integration/checks_test.go`)
   - Create check via API
   - Verify event exists in database with correct payload
   - Verify `ActorTypeUser` and correct `ActorUID`

3. **Event Listing**
   - Verify `GET /api/v1/orgs/:org/checks/:check_uid/events` returns the creation event

## Future Considerations

Apply same pattern for:
- `check.updated` - Emit on PATCH /checks/:uid
- `check.deleted` - Emit on DELETE /checks/:uid (soft delete)

This provides a complete audit trail for check lifecycle changes.
