# Slack Bot Commands

## Overview

The Slack bot shall answer commands similarly to the CLI client. Users mention the bot followed by a command, and the bot replies with the result.

## Threading Behavior

- If a message is sent in a thread, the bot replies in that same thread
- If a message is sent in a channel (not in a thread), the bot creates a new thread for its reply

## Supported Commands

### Checks

**Add a check (default settings):**
```
User: @solidping checks add https://one.one.one.one
Bot:  Check `http-one-one-one-one` added, 1 minute interval
```

**Add a check (custom slug and interval):**
```
User: @solidping checks add https://one.one.one.one -slug one -interval 5s
Bot:  Check `one` added, 5 seconds interval
```

**List all checks:**
```
User: @solidping checks list
Bot:  - `http-one-one-one-one`, every minute, OK for 10 minutes
```

**Remove a check:**
```
User: @solidping checks rm http-one-one-one-one
Bot:  Check `http-one-one-one-one` removed
```

### Results

**List recent results for a check:**
```
User: @solidping results -check http-one-one-one-one
Bot:  - `http-one-one-one-one`, OK, 120 ms
      - `http-one-one-one-one`, OK, 150 ms
      - `http-one-one-one-one`, OK, 180 ms
      - `http-one-one-one-one`, OK, 210 ms
      - `http-one-one-one-one`, OK, 240 ms
```

### Incidents

**List all incidents:**
```
User: @solidping incidents list
Bot:  - `http-one-one-one-one`, OK since 2 minutes ago, duration: 20 minutes
```

**List incidents for a specific check:**
```
User: @solidping incidents list -check http-one-one-one-one
Bot:  - `http-one-one-one-one`, OK since 2 minutes ago, duration: 20 minutes
```

---

# Implementation

## Approach

- **No SDK**: Use the existing HTTP client implementation in `internal/integrations/slack/client.go`
- **No new tables**: Leverage existing `integration_connections` table (stores bot access tokens via `SlackSettings`)
- **Extend existing code**: Modify `handleAppMention` in `events.go` to parse and route commands

## Existing Infrastructure

The following is already implemented and should be reused:

| Component | Location | Purpose |
|-----------|----------|---------|
| Slack Client | `internal/integrations/slack/client.go` | HTTP API calls (PostMessage, etc.) |
| Event Handler | `internal/integrations/slack/events.go` | `handleAppMention` receives bot mentions |
| Service | `internal/integrations/slack/service.go` | `GetClient()`, `GetConnectionByTeamID()`, `CreateCheck()` |
| Connection Lookup | `integration_connections` table | Stores `SlackSettings` with `access_token`, `team_id` |
| Checks Service | `internal/handlers/checks/service.go` | Create, list, delete checks |
| Results Service | `internal/handlers/results/service.go` | List results |
| Incidents Service | `internal/handlers/incidents/service.go` | List incidents |

## Implementation Steps

### 1. Create Command Parser (`internal/integrations/slack/parser.go`)

Parse the mention text to extract command, subcommand, and arguments:

```go
// ParsedCommand represents a parsed Slack bot command.
type ParsedCommand struct {
    Command    string            // "checks", "results", "incidents", "help"
    Subcommand string            // "add", "list", "rm" (for checks)
    Args       []string          // Positional arguments (e.g., URL, slug)
    Flags      map[string]string // Named flags (e.g., "-slug", "-interval", "-check")
}

// ParseMentionText extracts a command from a mention message.
// Input: "<@U123ABC> checks add https://example.com -slug mycheck"
// Output: ParsedCommand{Command: "checks", Subcommand: "add", Args: ["https://example.com"], Flags: {"slug": "mycheck"}}
func ParseMentionText(text string) (*ParsedCommand, error)
```

**Parsing rules:**
1. Strip the bot mention (`<@BOTID>`) from the start
2. Split remaining text by whitespace
3. First token = command (`checks`, `results`, `incidents`, `help`)
4. Second token = subcommand (if applicable)
5. Remaining tokens = args and flags
6. Flags start with `-` and consume the next token as value

### 2. Create Command Handlers (`internal/integrations/slack/mention_commands.go`)

Route parsed commands to handlers:

```go
// handleMentionCommand routes a parsed command to the appropriate handler.
func (h *Handler) handleMentionCommand(ctx context.Context, event *Event, cmd *ParsedCommand) error {
    switch cmd.Command {
    case "checks":
        return h.handleChecksCommand(ctx, event, cmd)
    case "results":
        return h.handleResultsCommand(ctx, event, cmd)
    case "incidents":
        return h.handleIncidentsCommand(ctx, event, cmd)
    case "help", "":
        return h.handleHelpCommand(ctx, event)
    default:
        return h.sendUnknownCommandResponse(ctx, event, cmd.Command)
    }
}
```

### 3. Implement Check Commands

```go
func (h *Handler) handleChecksCommand(ctx context.Context, event *Event, cmd *ParsedCommand) error {
    switch cmd.Subcommand {
    case "add":
        return h.handleChecksAdd(ctx, event, cmd)
    case "list":
        return h.handleChecksList(ctx, event, cmd)
    case "rm", "remove", "delete":
        return h.handleChecksRemove(ctx, event, cmd)
    default:
        return h.sendError(ctx, event, "Unknown checks subcommand: "+cmd.Subcommand)
    }
}
```

**checks add:**
- Extract URL from `cmd.Args[0]`
- Extract optional `-slug` and `-interval` flags
- Call `h.svc.CreateCheck()` (already exists)
- Reply with success/error message

**checks list:**
- Get organization from `integration_connections` via team ID
- Call checks service `ListChecks()`
- Format as Slack message with status and duration

**checks rm:**
- Extract slug/UID from `cmd.Args[0]`
- Call checks service `DeleteCheck()`
- Reply with confirmation

### 4. Implement Results Command

```go
func (h *Handler) handleResultsCommand(ctx context.Context, event *Event, cmd *ParsedCommand) error {
    checkSlug := cmd.Flags["check"]
    if checkSlug == "" {
        return h.sendError(ctx, event, "Missing required flag: -check")
    }

    // Get org from team ID
    conn, _ := h.svc.GetConnectionByTeamID(ctx, event.TeamID)
    org, _ := h.db.GetOrganization(ctx, conn.OrganizationUID)

    // List recent results (limit 5)
    results, _ := h.resultsService.ListResults(ctx, org.Slug, checkSlug, 5)

    // Format and send response
    return h.sendResultsResponse(ctx, event, results)
}
```

### 5. Implement Incidents Command

```go
func (h *Handler) handleIncidentsCommand(ctx context.Context, event *Event, cmd *ParsedCommand) error {
    conn, _ := h.svc.GetConnectionByTeamID(ctx, event.TeamID)
    org, _ := h.db.GetOrganization(ctx, conn.OrganizationUID)

    params := &incidents.ListParams{}
    if checkSlug := cmd.Flags["check"]; checkSlug != "" {
        params.CheckSlug = checkSlug
    }

    incidents, _ := h.incidentsService.ListIncidents(ctx, org.Slug, params)

    return h.sendIncidentsResponse(ctx, event, incidents)
}
```

### 6. Update `handleAppMention` in `events.go`

Modify the existing handler to parse and route commands:

```go
func (h *Handler) handleAppMention(ctx context.Context, event *Event) error {
    client, err := h.svc.GetClient(ctx, event.TeamID)
    if err != nil {
        return fmt.Errorf("failed to get client: %w", err)
    }

    // Parse the command from the mention text
    cmd, err := ParseMentionText(event.Event.Text)
    if err != nil {
        return h.sendError(ctx, event, "Failed to parse command: "+err.Error())
    }

    // Route to command handler
    return h.handleMentionCommand(ctx, event, cmd)
}
```

### 7. Response Formatting

All responses should:
- Reply in thread (use `event.Event.Ts` as `thread_ts` if no existing thread, else use `event.Event.ThreadTs`)
- Use Block Kit for rich formatting
- Include error handling with user-friendly messages

```go
func (h *Handler) sendResponse(ctx context.Context, event *Event, msg *MessageResponse) error {
    client, _ := h.svc.GetClient(ctx, event.TeamID)

    // Determine thread_ts: reply in existing thread or start new one
    threadTS := event.Event.ThreadTs
    if threadTS == "" {
        threadTS = event.Event.Ts // Start new thread from the mention message
    }

    _, err := client.PostMessage(ctx, PostMessageOptions{
        Channel:  event.Event.Channel,
        ThreadTS: threadTS,
        Message:  msg,
    })
    return err
}
```

## File Changes Summary

| File | Change |
|------|--------|
| `internal/integrations/slack/parser.go` | **New** - Command parser |
| `internal/integrations/slack/mention_commands.go` | **New** - Command handlers |
| `internal/integrations/slack/events.go` | **Modify** - Update `handleAppMention` to use parser |
| `internal/integrations/slack/service.go` | **Modify** - Add dependencies for results/incidents services |
| `internal/integrations/slack/handler.go` | **Modify** - Inject results/incidents services |

## Dependencies to Inject

The Slack handler will need access to:
- `checks.Service` (already available via `h.svc.checksService`)
- `results.Service` (new dependency)
- `incidents.Service` (new dependency)

These should be injected through the service registry in `internal/app/services/`.

## Error Handling

All errors should be returned as user-friendly Slack messages:
- Validation errors: "Invalid URL format"
- Not found: "Check `xyz` not found"
- Permission errors: "You don't have permission to delete checks"
- Internal errors: "Something went wrong. Please try again."

Errors should be logged server-side with full context for debugging.
