# Slack Commands Architecture

## Original Question
Do you think it makes sense to handle upcoming slack commands through the API like it was the front or the CLI? This would be strange since it means the service would be calling itself but it would make it easier to maintain and secure.

## Codebase Analysis

After analyzing the existing architecture, **the codebase already answers this question** - and it chose the shared service layer approach.

### Current Architecture

```
                      ┌─────────────────────────────────────────────┐
                      │                 SolidPing                   │
                      │                                             │
  ┌─────────────┐     │  ┌──────────────┐     ┌─────────────────┐  │
  │  Frontend   │────────▶  API Handler  │────▶│                 │  │
  └─────────────┘     │  └──────────────┘     │  Service Layer  │  │
                      │                        │                 │  │
  ┌─────────────┐     │  ┌──────────────┐     │  - checks.Svc   │  │
  │     CLI     │────────▶  API Handler  │────▶│  - auth.Svc    │  │
  └─────────────┘     │  └──────────────┘     │  - incidents.Svc│  │
                      │                        │  - etc.         │  │
  ┌─────────────┐     │  ┌──────────────┐     │                 │  │
  │    Slack    │────────▶ Slack Handler │────▶│                 │  │
  └─────────────┘     │  └──────────────┘     └────────┬────────┘  │
                      │                                 │           │
                      │                        ┌────────▼────────┐  │
                      │                        │   Database      │  │
                      │                        └─────────────────┘  │
                      └─────────────────────────────────────────────┘
```

### How the CLI Works (External Client)
The CLI calls the HTTP API, just like the frontend:

```go
// back/pkg/cli/checks.go:344
resp, err := apiClient.CreateCheckWithResponse(ctx, cliCtx.GetOrg(), req)
```

- Uses the generated OpenAPI client (`pkg/client/`)
- Makes HTTP requests to `/api/v1/orgs/{org}/checks`
- Is a true external client, no special database access

### How Slack Works (Internal Integration)
The Slack handler calls services directly:

```go
// back/internal/integrations/slack/service.go:41-46
type Service struct {
    db            db.Service
    cfg           *config.Config
    authService   *auth.Service
    checksService *checks.Service  // <-- Injected service
}

// back/internal/integrations/slack/service.go:516
checkResp, err := s.checksService.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
    Type: "http",
    Config: map[string]any{"url": url},
})
```

- Services are injected at startup in `server.go`
- No HTTP round-trip, direct function calls
- Shares the same business logic as the API

## Verdict: Keep the Current Approach

The existing pattern is correct and should be continued for new Slack commands.

### Why NOT to Self-Call the HTTP API

1. **Unnecessary overhead**: HTTP parsing, JSON serialization, network stack
2. **Complexity**: Need to manage tokens/auth for internal calls
3. **Failure modes**: HTTP layer could fail even when services work
4. **Debugging**: Stack traces cross HTTP boundaries, harder to trace

### Why the Service Layer Approach is Better

1. **Already implemented**: The Slack integration already uses it
2. **Consistent with the codebase**: Follows the handler-service separation
3. **Type-safe**: Go structs instead of HTTP/JSON
4. **Same authorization model**: Services handle authorization internally
5. **Transactional**: Can share database transactions across operations

## Adding New Slack Commands

To add a new command (e.g., `/incidents`):

### 1. Add routing in `commands.go`
```go
func (h *Handler) handleCommand(ctx context.Context, cmd *Command) (*MessageResponse, error) {
    switch cmd.Command {
    case "/check":
        return h.handleCheckCommand(ctx, cmd)
    case "/incidents":  // <-- New command
        return h.handleIncidentsCommand(ctx, cmd)
    default:
        return &MessageResponse{ResponseType: "ephemeral", Text: "Unknown command"}, nil
    }
}
```

### 2. Add the incidents service to `slack.Service`
```go
type Service struct {
    db              db.Service
    cfg             *config.Config
    authService     *auth.Service
    checksService   *checks.Service
    incidentsService *incidents.Service  // <-- Add this
}
```

### 3. Implement the command handler
```go
func (h *Handler) handleIncidentsCommand(ctx context.Context, cmd *Command) (*MessageResponse, error) {
    conn, err := h.svc.GetConnectionByTeamID(ctx, cmd.TeamID)
    if err != nil {
        return &MessageResponse{Text: "Workspace not connected"}, nil
    }

    org, _ := h.svc.db.GetOrganization(ctx, conn.OrganizationUID)

    // Call the incidents service directly
    incidents, err := h.svc.incidentsService.ListIncidents(ctx, org.Slug, incidents.ListOptions{
        State: "open",
    })

    // Format and return Slack blocks
    return formatIncidentsResponse(incidents), nil
}
```

### 4. Wire it up in `server.go`
```go
slackService := slack.NewService(
    s.dbService,
    s.config,
    authService,
    checksService,
    incidentsService,  // <-- Pass it here
)
```

## Summary

| Component | Calls API? | Why |
|-----------|------------|-----|
| Frontend  | Yes | External JS client, uses OpenAPI |
| CLI       | Yes | External process, uses generated client |
| Slack     | No  | Internal, uses injected services |

**The self-calling API approach was considered but rejected** - the codebase chose the service layer pattern, which is cleaner and more efficient. Continue using this pattern for all new Slack commands.
