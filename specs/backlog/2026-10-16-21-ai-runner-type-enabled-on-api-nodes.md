---
model: sonnet
effort: medium
---

# The AI script runner ignores the enabled check types on an API-only node

**Status:** backlog. Found by the audit of `specs/done/2026/10/2026-10-16-09-js-check-nested-docker-guard.md`.

## Problem
`checkjs.TypeEnabled` (the operator's `checkers.enabled` / `checkers.disabled` list) is installed by the check worker. On an API-only node no check worker is built (`ShouldRunChecks` is false), so `TypeEnabled` stays nil, and the AI/MCP script runner (`server/internal/app/aichecks.go`, `aichecks.Runner`) lets a JS script run any sub-check type, including ones the operator disabled. Spec 2026-10-16-09 already installs `checkjs.DeploymentGate` there; `TypeEnabled` has the same gap. Both are package-level hooks that fail open when nil.

## Proposal
1. Install `checkjs.TypeEnabled` in `buildAIChecks` from the server config, as the worker does.
2. Consider making both hooks fail closed (nil means "deny sub-checks") or replacing the globals with an explicit dependency passed to the JS checker, so a new process that runs JS cannot forget them.

## Tests
- On an API-only node config with `docker` disabled, an AI script calling `solidping.check("docker", ...)` is refused with the "disabled on this server" message; an enabled type still runs.
