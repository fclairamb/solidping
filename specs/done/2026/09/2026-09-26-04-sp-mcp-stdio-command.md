---
effort: medium
---

# SolidPing has no stdio MCP command, so desktop agents and directories need a bridge

## Problem

SolidPing serves MCP only as streamable HTTP at `/api/v1/mcp`
(`server/internal/app/server.go:1042-1061`, entry point `Handle` at
`server/internal/mcp/handler.go:277`). Two kinds of consumers want a
stdin/stdout MCP server instead:

- **MCP directories.** Glama builds a container and runs
  `mcp-proxy -- <stdio command>`. Its config validator also rejects any config
  that uses `mcp-remote`. The Glama listing (released 2026-09-26) only works
  through a shell script that starts `solidping serve` in the background, logs
  in as the seeded `admin@solidping.io`, rotates its must-change password to a
  random one, and bridges stdio to HTTP with `supergateway`. That script is
  fragile: it depends on the seed password, the rotation endpoint and a third-
  party bridge. It is recorded in
  `solidping-business/memory/drafts/2026-09-26-glama-dockerfile.md`.
- **Desktop agent clients** (Claude Desktop, Cursor, and others) that are
  simplest to configure with a command rather than a URL plus OAuth.

## Proposal

Add `sp mcp`, a stdio front for a remote instance.

The `sp` client CLI already knows a server URL (`--url` / `SOLIDPING_URL`,
`server/pkg/cli/flags.go:22`) and holds a token from `sp auth login` or
`SP_TOKEN` (`server/pkg/cli/commands.go:13-41`, `apihelper.go:523`). `sp mcp`
reads JSON-RPC lines on stdin, POSTs each one to `<url>/api/v1/mcp` with the
bearer token and the `Mcp-Session-Id` it got back from `initialize`, and writes
responses to stdout.
- Small, with no new server surface. Every auth, role, scope and demo rule stays
  server-side, where it already is.
- Works against solidping.io or any self-hosted instance, which is what desktop
  users want.
- Does not solve Glama on its own: there is still no server in the container
  and no token to give it.

Implementation:

- New `mcp` command in `server/pkg/cli/commands.go`, available as `sp mcp` and
  `solidping client mcp` (`server/main.go:103-106`).
- Newline-delimited JSON-RPC on stdin/stdout; nothing else may be written to
  stdout. Logs go to stderr.
- Keep the `Mcp-Session-Id` from `initialize` and send it on later requests.
  Close the session with `DELETE /api/v1/mcp` on EOF.
- Handle both plain JSON and `text/event-stream` responses from the server.
- Refresh an expired session token the same way other `sp` commands do.
- Tests: a fake HTTP MCP server in-process, driving `sp mcp` through pipes for
  initialize, tools/list, a tool call, a 401, and EOF.
- Document the Claude Desktop config snippet in `wiki/features/mcp.md`.
- The in-process variant (`solidping mcp --stdio`, no remote server needed) is
  spec 2026-09-26-05, which has open questions. This spec does not depend on it.
