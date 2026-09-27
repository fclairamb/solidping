---
effort: high
---

# `solidping mcp --stdio`: an in-process MCP server on a local database

## Problem

MCP directories such as Glama build a container and run
`mcp-proxy -- <stdio command>`, and Glama rejects configs that use
`mcp-remote`. The current listing works through a shell script that starts
`solidping serve`, logs in as the seeded `admin@solidping.io`, rotates its
must-change password and bridges stdio to HTTP with `supergateway`
(`solidping-business/memory/drafts/2026-09-26-glama-dockerfile.md`). `sp mcp`
(spec 2026-09-26-04) does not remove that script: the container still needs a
running server and a token.

## Proposal

Add `solidping mcp --stdio`. It boots the same services as `serve` (database, jobs, workers) and feeds stdin
lines straight to the MCP dispatcher (`dispatch`, `handler.go:343`), skipping
HTTP.
- One command, and it runs from the image alone. That is exactly what Glama
  needs.
- It needs a principal. A fresh database has only the must-change-password
  admin (`jobs/jobtypes/job_startup.go`), and the MCP tools read claims from the
  request context. Stdio implies a local operator, so the natural choice is to
  act as the owner of the default org. That is a new trust path which bypasses
  token auth, and it needs an explicit decision.
- Checks only produce results if the workers run in the same process, so it is
  effectively `serve` without the HTTP listener.

With that, the Glama config becomes `["solidping", "mcp", "--stdio"]` with no
build-time token or bridge.

## Open questions

1. Is this wanted at all, or is the Glama listing good enough with the current
   script?
2. Which principal does a stdio session act as (the default org
   owner, a user named by flag, or a PAT passed in an environment variable), and
   is bypassing token auth on a local stdio channel acceptable?
3. Should it run the check workers (full `serve` minus HTTP) or
   only the MCP services, with checks never executing?

## Resolved open questions

1. **Build it.** `solidping mcp --stdio` is wanted. It replaces the Glama shell script
   (serve in the background, seed login, password rotation, supergateway bridge).
2. **Act as the owner of the default org, with `--org` / `--user` flags to pick another.**
   Bypassing token auth on the stdio channel is accepted: the process already holds the
   database credentials, so it has no more power than direct SQL access. The session
   ignores `must_change_password`. Fail with a clear error on stderr (non-zero exit) when
   the org or user does not exist or the user is not a member of the org.
3. **Run full `serve` minus the HTTP listener.** Database, migrations, jobs and check
   workers run in-process, so checks created over MCP produce results.
