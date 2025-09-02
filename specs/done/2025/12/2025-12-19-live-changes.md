# Live Check Updates

## Problem
When a new check is created, checkrunners are sleeping and won't discover the new check until their next wake-up cycle. This means:
- New checks may not start running for several seconds (or longer depending on sleep period)
- Poor user experience - users expect immediate feedback after creating a check
- Inconsistent behavior depending on when in the sleep cycle the check is created

## Solution
Notify checkrunners immediately when a check is created so they can start running it without waiting for the next sleep cycle.

## Architecture

### Checkrunner Roles
- **All checkrunners** receive notifications about check changes and wake up immediately
- This allows all runners to discover new checks faster and compete for claiming them
- The lease-based job claiming system prevents duplicate processing

All checkrunners listen for notifications. When any runner is notified, it wakes up early from its sleep cycle and attempts to claim available jobs. The existing lease mechanism ensures that only one runner processes each check.

### Sleep Period
The scheduler sleeps between check iterations. If the sleep period is:
- **< 5s**: Polling is frequent enough, notification overhead not worth it
- **≥ 5s**: Notifications are beneficial to reduce wait time for new checks

## Implementation

### Notifier Interface
Define a common interface for check notifications:

```go
// CheckNotifier sends notifications when checks are created/updated/deleted
type CheckNotifier interface {
    // NotifyCheckCreated signals that a new check was created
    NotifyCheckCreated(ctx context.Context) error

    // WaitForNotification returns a channel that receives notifications
    // or nil if notifications are not supported
    WaitChannel() <-chan struct{}

    // Close releases resources
    Close() error
}
```

### Local/Channel Implementation (for SQLite)
SQLite doesn't have built-in pub/sub, so we use an in-memory Go channel:

```go
// LocalCheckNotifier uses in-memory channels for notifications
type LocalCheckNotifier struct {
    checkCreated chan struct{}
}

func NewLocalCheckNotifier() *LocalCheckNotifier {
    return &LocalCheckNotifier{
        checkCreated: make(chan struct{}, 1), // Buffered to avoid blocking
    }
}

func (n *LocalCheckNotifier) NotifyCheckCreated(ctx context.Context) error {
    select {
    case n.checkCreated <- struct{}{}:
        // Notification sent
    default:
        // Channel full or no listener, skip (runner will pick it up on next cycle)
    }
    return nil
}

func (n *LocalCheckNotifier) WaitChannel() <-chan struct{} {
    return n.checkCreated
}

func (n *LocalCheckNotifier) Close() error {
    close(n.checkCreated)
    return nil
}
```

**Key points:**
- Use a buffered channel (size 1) to avoid blocking
- Non-blocking send (`select` with `default`) to prevent API slowdown
- Don't send the check data, just a wake-up signal
- Runner will query for new checks normally

### PostgreSQL Implementation (NOTIFY/LISTEN)
PostgreSQL has native LISTEN/NOTIFY support:

```go
// PgCheckNotifier uses PostgreSQL LISTEN/NOTIFY for notifications
type PgCheckNotifier struct {
    db           *bun.DB
    listener     *pq.Listener
    notification chan struct{}
    closeOnce    sync.Once
}

func NewPgCheckNotifier(db *bun.DB, connString string) (*PgCheckNotifier, error) {
    n := &PgCheckNotifier{
        db:           db,
        notification: make(chan struct{}, 1),
    }

    // Set up PostgreSQL listener
    n.listener = pq.NewListener(connString, 10*time.Second, time.Minute, func(ev pq.ListenerEventType, err error) {
        if err != nil {
            // Log listener errors but don't fail
            log.Printf("postgres listener error: %v", err)
        }
    })

    // Start listening on the check_created channel
    if err := n.listener.Listen("check_created"); err != nil {
        return nil, fmt.Errorf("failed to listen: %w", err)
    }

    // Start goroutine to forward postgres notifications to our channel
    go n.listenLoop()

    return n, nil
}

func (n *PgCheckNotifier) listenLoop() {
    for {
        select {
        case notification := <-n.listener.Notify:
            if notification != nil {
                // Forward to our notification channel (non-blocking)
                select {
                case n.notification <- struct{}{}:
                default:
                }
            }
        case <-time.After(90 * time.Second):
            // Periodic ping to keep connection alive
            go n.listener.Ping()
        }
    }
}

func (n *PgCheckNotifier) NotifyCheckCreated(ctx context.Context) error {
    // Send NOTIFY signal via database connection
    _, err := n.db.ExecContext(ctx, "NOTIFY check_created")
    // Ignore errors - this is best-effort notification
    return nil
}

func (n *PgCheckNotifier) WaitChannel() <-chan struct{} {
    return n.notification
}

func (n *PgCheckNotifier) Close() error {
    var err error
    n.closeOnce.Do(func() {
        if n.listener != nil {
            err = n.listener.Close()
        }
        close(n.notification)
    })
    return err
}
```

**Key points:**
- Uses `pq.Listener` for PostgreSQL LISTEN functionality
- Forwards notifications to an internal channel for consistency with local implementation
- Non-blocking channel send to prevent slowdowns
- Periodic ping to keep connection alive
- Errors from NOTIFY are ignored (best-effort)
- Can use within transactions: `tx.ExecContext(ctx, "NOTIFY check_created")`

### Usage in API Handler
The API handler uses the interface without knowing the implementation:

```go
// When a check is created (in the API handler)
func (h *CheckHandler) CreateCheck(ctx context.Context, req CreateCheckRequest) error {
    // ... create check in database ...

    // Notify checkrunner 0 (works with both implementations)
    if h.notifier != nil {
        _ = h.notifier.NotifyCheckCreated(ctx)
    }

    return nil
}
```

### Usage in CheckRunner
The checkrunner uses the wait channel:

```go
// In checkrunner 0
func (r *CheckRunner) Run(ctx context.Context) {
    notifyChan := r.notifier.WaitChannel()

    for {
        // Process checks
        r.processChecks(ctx)

        // Sleep with interrupt capability
        select {
        case <-time.After(r.sleepPeriod):
            // Normal wake up
        case <-notifyChan:
            // Wake up early due to new check
        case <-ctx.Done():
            return
        }
    }
}

## Notification Events
Currently only one event is needed:
- **check_created**: When a new check is created

Future events could include:
- **check_updated**: When a check's schedule changes
- **check_deleted**: When a check is deleted (to stop running it immediately)

## Benefits
- **Clean abstraction**: Interface-based design allows swapping implementations
- **Cross-database compatibility**: Same API handler code works with both PostgreSQL and SQLite
- **Optimal for each database**: SQLite uses lightweight channels, PostgreSQL uses native NOTIFY/LISTEN
- **No conditional logic**: Database-specific code is encapsulated in the notifier implementation
- **Testable**: Easy to mock the notifier interface in tests
- **Graceful degradation**: Notifications are best-effort; system works even if they fail

## Error Handling
- **NotifyCheckCreated errors**: Always ignored - notifications are best-effort optimizations
- **Local notifier**: Channel full is handled gracefully with non-blocking send
- **PostgreSQL notifier**:
  - NOTIFY errors are ignored (notification is optional)
  - LISTEN connection errors are logged but don't stop the system
  - Listener automatically reconnects on connection loss
- **No active listeners**: Messages sent to channel/NOTIFY are simply dropped
- **Transaction rollback**: PostgreSQL NOTIFY within transaction is automatically rolled back

The pattern is: **Notifications are always best-effort** - the system continues working even if all notifications fail.

## Testing Considerations
- **Interface compliance**: Both implementations satisfy the CheckNotifier interface
- **Local notifier**:
  - Verify non-blocking sends (channel full doesn't block)
  - Test notification delivery to waiting checkrunner
  - Test Close() properly closes channel
- **PostgreSQL notifier**:
  - Verify NOTIFY is sent to database
  - Verify LISTEN receives notifications
  - Test listener reconnection on connection loss
  - Test Close() properly closes listener and channel
- **API handler**: Same code works with both notifier implementations
- **Checkrunner**: Wakes up when notification is received
- **Transactions**: PostgreSQL NOTIFY within transaction is rolled back on error
- **Multiple creates**: Rapid check creations don't cause blocking or errors

## Implementation Phases

**Phase 1: Define interface and implement LocalCheckNotifier**
- Create `CheckNotifier` interface
- Implement `LocalCheckNotifier` using Go channels
- Inject notifier into API handlers and checkrunner 0
- SQLite uses `LocalCheckNotifier` by default
- PostgreSQL can initially also use `LocalCheckNotifier` (works, but not optimal)

**Phase 2: Implement PgCheckNotifier**
- Implement `PgCheckNotifier` using PostgreSQL LISTEN/NOTIFY
- Create notifier based on database type at startup
- PostgreSQL automatically uses `PgCheckNotifier` for optimal performance
- SQLite continues using `LocalCheckNotifier`

**Database-specific initialization:**
```go
func NewCheckNotifier(db *bun.DB, dbType string, connString string) (CheckNotifier, error) {
    switch dbType {
    case "postgres":
        return NewPgCheckNotifier(db, connString)
    default: // "sqlite" or any other
        return NewLocalCheckNotifier(), nil
    }
}
```
