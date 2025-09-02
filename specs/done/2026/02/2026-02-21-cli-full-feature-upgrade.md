# CLI Full Feature Upgrade

## Overview

Upgrade the CLI (`sp` / `solidping client`) to support most features available in the web dashboard. The CLI currently has 13 commands across 6 groups. This adds 25 new commands across 7 groups, using the already-generated OpenAPI client methods. Also introduces a kubectl-style `-o format` output flag replacing the current `--json` boolean.

## Motivation

1. Users who prefer CLI workflows cannot manage tokens, members, jobs, or system parameters from the terminal.
2. Check management is limited to list/add/remove — no way to view details, update, or upsert.
3. Incident investigation requires switching to the web UI to see incident events.
4. The generated API client (`back/pkg/client/client_generated.go`) already has methods for all these operations — only the CLI command wiring is missing.
5. The current `--json` flag is limited — there's no way to get newline-delimited JSON for piping into tools like `jq`, `grep`, or streaming processors.

## Current State

**CLI commands** (in `back/pkg/cli/`):

| Group | Commands | Files |
|-------|----------|-------|
| auth | login, logout, me | `auth.go` |
| server | health, version | `server.go` |
| checks | list, add, remove | `checks.go` |
| results | list | `results.go` |
| incidents | list, get | `incidents.go` |
| events | list | `events.go` |

**Generated client methods available but unused by CLI**:
- `GetCheckWithResponse`, `UpdateCheckWithResponse`, `UpsertCheckWithResponse`, `ListCheckEventsWithResponse`
- `ListIncidentEventsWithResponse`
- `SwitchOrgWithResponse`
- `ListMembersWithResponse`, `AddMemberWithResponse`, `GetMemberWithResponse`, `UpdateMemberWithResponse`, `RemoveMemberWithResponse`
- `ListOrgTokensWithResponse`, `ListAllTokensWithResponse`, `CreateTokenWithResponse`, `RevokeTokenWithResponse`
- `ListJobsWithResponse`, `GetJobWithResponse`, `CreateJobWithResponse`, `CancelJobWithResponse`
- `ListSystemParametersWithResponse`, `GetSystemParameterWithResponse`, `SetSystemParameterWithResponse`, `DeleteSystemParameterWithResponse`

---

## Output Format Flag (`-o`)

### Change: Replace `--json` with `-o format`

Currently the CLI has a `--json` boolean flag in `GetGlobalFlags()` (`back/pkg/cli/flags.go`). Replace it with a kubectl-style output format flag:

```
-o, --output format    Output format: text, json, jsonl (default: text)
```

The `--json` flag is kept as a hidden alias for backward compatibility (equivalent to `-o json`).

### Output formats

| Format | Description | Use case |
|--------|-------------|----------|
| `text` | Human-readable tables and key-value pairs (default) | Interactive terminal use |
| `json` | Pretty-printed JSON (current `--json` behavior) | Scripting, single-object inspection |
| `jsonl` | One JSON object per line, no pretty-printing | Piping into `jq`, streaming, log processing |

### `jsonl` behavior for list commands
- Each item in a list response is printed as a separate JSON line
- Non-list commands (get, create, delete) print a single JSON line
- Pagination info is printed as a final line: `{"pagination":{"cursor":"...","hasMore":true}}`

### Implementation

**`back/pkg/cli/flags.go`** — Replace `--json` BoolFlag with:
```go
&cli.StringFlag{
    Name:    "output",
    Aliases: []string{"o"},
    Usage:   "Output format: text, json, jsonl",
    Value:   "text",
    Sources: cli.EnvVars("SOLIDPING_OUTPUT"),
},
// Keep --json as hidden alias for backward compatibility
&cli.BoolFlag{
    Name:   "json",
    Usage:  "Output in JSON format (alias for -o json)",
    Hidden: true,
},
```

**`back/pkg/cli/context.go`** — Resolve output format:
```go
// In NewCLIContext: determine format from -o flag or --json alias
outputFormat := cmd.String("output")
if cmd.Bool("json") && outputFormat == "text" {
    outputFormat = "json"
}
```

**`back/pkg/cli/output/output.go`** — Add `OutputFormat` type and `NewOutputter` accepting a format string:
```go
type OutputFormat string

const (
    FormatText  OutputFormat = "text"
    FormatJSON  OutputFormat = "json"
    FormatJSONL OutputFormat = "jsonl"
)

func NewOutputter(format OutputFormat, writer io.Writer) Outputter
```

**`back/pkg/cli/output/jsonl.go`** — New file implementing `JSONLOutputter`:
- `Print(data)` — marshals to single-line JSON + newline
- `PrintList(items)` — prints each item as a separate JSON line
- `PrintError(err)` — error as single JSON line
- `Success(msg)` — success message as single JSON line

**Existing commands** — All existing commands (`checks.go`, `incidents.go`, `events.go`, `results.go`, `auth.go`, `server.go`) need to be migrated from `cmd.Bool("json")` to using the outputter's format, but the behavior stays identical for `text` and `json`. The `jsonl` format is handled by the outputter.

---

## New Commands

### 1. Checks enhancements

Add to `checks` group (existing alias: `check`).

#### `checks get <uid|slug>`
- API: `GetCheckWithResponse(ctx, org, checkUid)`
- Text output: Key-value pairs (UID, slug, name, type, config URL, period, enabled, status, created/updated timestamps)
- Table fields: UID, SLUG, NAME, TYPE, PERIOD, ENABLED, STATUS

#### `checks update <uid|slug>`
- API: `UpdateCheckWithResponse(ctx, org, checkUid, body)`
- Flags:
  - `--name` — new name
  - `--enabled` — set enabled (true/false)
  - `--disabled` — shorthand for `--enabled=false`
  - `--interval` — new check interval (e.g., 5s, 1m; converted to HH:MM:SS)
  - `--timeout` — new timeout
  - `--slug` — new slug
- Only sends fields that are explicitly set
- Text output: success message with updated check summary

#### `checks upsert <slug> <url>`
- API: `UpsertCheckWithResponse(ctx, org, slug, body)`
- Flags: `--type` (default: http), `--name`, `--interval`, `--timeout`
- Text output: "Created" (201) or "Updated" (200) with check summary

#### `checks events <uid|slug>`
- API: `ListCheckEventsWithResponse(ctx, org, checkUid, params)`
- Flags: `--cursor`, `--size` (default: 20)
- Table columns: TIMESTAMP, TYPE, ACTOR

### 2. Auth enhancement

Add to existing `auth` group.

#### `auth switch-org <org>`
- API: `SwitchOrgWithResponse(ctx, body)`
- Saves returned tokens to token file (`~/.config/solidping/token.json`)
- Text output: "Switched to organization: {org}"

### 4. Members (new group)

New top-level group `members` (alias: `member`).

#### `members list`
- API: `ListMembersWithResponse(ctx, org)`
- Table columns: UID, EMAIL, NAME, ROLE

#### `members add <email>`
- API: `AddMemberWithResponse(ctx, org, body)`
- Flags: `--role` — role: `admin`, `user`, `viewer` (default: `user`)
- Text output: member details (UID, email, role)

#### `members get <uid>`
- API: `GetMemberWithResponse(ctx, org, uid)`
- Text output: key-value pairs (UID, email, display name, role, created)

#### `members update <uid>`
- API: `UpdateMemberWithResponse(ctx, org, uid, body)`
- Flags: `--role`, `--name`
- Text output: updated member details

#### `members remove <uid>`
- API: `RemoveMemberWithResponse(ctx, org, uid)`
- Text output: "Member removed successfully: {uid}"

### 5. Incidents enhancement

Add to existing `incidents` group.

#### `incidents events <uid>`
- API: `ListIncidentEventsWithResponse(ctx, org, uid, params)`
- Flags: `--cursor`, `--size` (default: 20)
- Table columns: TIMESTAMP, TYPE, ACTOR

---

## Implementation

### Pattern

Every command action function follows this pattern (updated for `-o format`):

```go
func xxxAction(ctx context.Context, cmd *cli.Command) error {
    cliCtx, err := NewCLIContext(cmd)
    if err != nil {
        return err
    }

    apiClient, err := cliCtx.APIHelper.GetClient(ctx)
    if err != nil {
        return cliCtx.HandleAuthError(err)
    }

    // Build params, call API, check status code
    // Non-text output: cliCtx.Outputter.Print(resp.JSONxxx)
    // Text output: output.NewTable + AppendHeader + AppendRow + Render
    // Errors: formatAPIError for API errors
}
```

The `CLIContext` gains a helper `IsText() bool` to branch on output format, and `HandleAuthError(err)` to unify auth error handling across formats.

### Files to modify

| File | Change |
|------|--------|
| `back/pkg/cli/flags.go` | Replace `--json` BoolFlag with `-o/--output` StringFlag, keep `--json` as hidden alias |
| `back/pkg/cli/context.go` | Resolve output format from `-o` flag or `--json` alias, pass format to `NewOutputter` |
| `back/pkg/cli/output/output.go` | Add `OutputFormat` type, update `NewOutputter` to accept format string |
| `back/pkg/cli/commands.go` | Register all new commands and subcommands |
| `back/pkg/cli/auth.go` | Add `switch-org` action, migrate from `cmd.Bool("json")` to outputter |
| `back/pkg/cli/checks.go` | Migrate from `cmd.Bool("json")` to outputter |
| `back/pkg/cli/incidents.go` | Add `incidents events` action, migrate from `cmd.Bool("json")` to outputter |
| `back/pkg/cli/events.go` | Migrate from `cmd.Bool("json")` to outputter |
| `back/pkg/cli/results.go` | Migrate from `cmd.Bool("json")` to outputter |
| `back/pkg/cli/server.go` | Migrate from `cmd.Bool("json")` to outputter |

### Files to create

| File | Contents |
|------|----------|
| `back/pkg/cli/output/jsonl.go` | `JSONLOutputter` — one JSON object per line, `PrintList` for item-by-item output |
| `back/pkg/cli/checks_detail.go` | `checksGetAction`, `checksUpdateAction`, `checksUpsertAction`, `checksEventsAction` |
| `back/pkg/cli/tokens.go` | `tokensListAction`, `tokensCreateAction`, `tokensRevokeAction` |
| `back/pkg/cli/members.go` | `membersListAction`, `membersAddAction`, `membersGetAction`, `membersUpdateAction`, `membersRemoveAction` |
| `back/pkg/cli/jobs.go` | `jobsListAction`, `jobsGetAction`, `jobsCreateAction`, `jobsCancelAction` |
| `back/pkg/cli/system.go` | `systemListAction`, `systemGetAction`, `systemSetAction`, `systemDeleteAction` |

### Reusable utilities from existing code

- `formatAPIError(err, statusCode)` — in `checks.go`
- `formatDuration(d)` — in `checks.go`
- `safeUUID(uid)` — in `incidents.go`
- `output.NewTable(os.Stdout)` — table creation
- `output.PrintSuccess/PrintError/PrintMessage` — text output
- `cli.Exit("", code)` — exit codes (0=success, 1=error, 3=auth error, 5=invalid args)

---

## Out of Scope

The following features exist in the backend but are **not in the generated API client** (`openapi.yaml`). They would need the OpenAPI spec to be updated and the client regenerated — this is left for a follow-up:

- Check groups CRUD
- Check connections CRUD
- Status pages + sections + resources
- Invitations CRUD
- Organization settings & creation
- Incident acknowledge/resolve
- Auth register, confirm-registration, accept-invite
- Profile update (PATCH /auth/me)

---

## Verification

### Build & Lint
```bash
make build-cli
make lint-back
make test
```

### Manual testing
```bash
# Start server
make dev-backend

# Login
sp auth login -e admin@solidping.com -p solidpass

# Checks
sp checks get <slug>
sp checks update <slug> --name "new name" --interval 30s
sp checks upsert my-check https://example.com --type http --interval 1m
sp checks events <slug>

# Auth
sp auth switch-org other-org

# Members
sp members list
sp members add user@example.com --role admin
sp members get <uid>
sp members update <uid> --role viewer
sp members remove <uid>

# Incidents
sp incidents events <uid>

# Output formats
sp checks list -o json          # Pretty-printed JSON
sp checks list -o jsonl         # One check per line
sp checks list -o jsonl | jq .slug  # Pipe into jq
sp members list -o json
sp checks get <slug> --json     # Backward compat alias
```

---

**Status**: Draft | **Created**: 2026-02-21
