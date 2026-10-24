---
model: sonnet
effort: medium
---

# Check the MCP tool surface against the Glama/registry audit in issue 587 and fix only the gaps that are real

## Problem

Issue [#587](https://github.com/fclairamb/solidping/issues/587) is a third-party scorer's report ("Glama TDQS & MCP Registry Audit", 68/100 in the title, 25/100 on the linked page). The linked page is also an upsell: a $9 "4-protocol pack", a README badge and generated A2A / x402 / Stripe MPP files. None of that is a request from this repo's owner. The substantive claims, for the 22 tools it lists (`list_checks` ... `manage_status_page_incident`), are the same boilerplate on every tool:

1. Missing `outputSchema`.
2. Unbounded `inputSchema` (`additionalProperties != false`).
3. Missing `annotations.title` / safety hints.
4. Tool descriptions: no sibling routing, no cross-parameter interaction prose, no side-effect/quota disclosure.
5. Missing `smithery.yaml` and `mcp-lock.json`.

Several of these look already done in the repo, so the report is probably stale (`server.json` still says 0.37.0) or was taken from an unauthenticated listing of an older deployment:

- Tools already carry `OutputSchema` and `Annotations` with a title, e.g. `server/internal/mcp/tools_statuspages.go:56-60`, `:94-95`, `:201-202`, `:352-356`. The annotation classes and their rationale are in `server/internal/mcp/annotations.go` (header comment). Done spec: `specs/done/2026/09/2026-09-28-01-mcp-annotations-output-schemas.md`.
- Smithery indexing was solved with a static server card instead of `smithery.yaml`: `ServerCardPath = /.well-known/mcp/server-card.json` in `server/internal/mcp/servercard.go`. Done spec: `specs/done/2026/09/2026-09-30-03-mcp-smithery-indexing.md`.
- `glama.json` and `server.json` (MCP registry) already exist at the repo root.

So "fix everything" means: reproduce each claim against the code, fix what is really missing, and write down what is not.

## Proposal

1. Reproduce. Dump `tools/list` from the repo itself (the stdio mode, `server/internal/mcp/stdio.go`, or a test server) and record, per tool: `outputSchema` present, `annotations.title` non-empty and the hint set, `inputSchema.additionalProperties`. Put the table in the PR description. Do not call the scorer's URL or buy its pack.
2. For each tool where the dump shows a real gap, fix it in its `server/internal/mcp/tools_*.go` file, using the existing helpers (`readOnlyAnnotations`, `createAnnotations`, `updateAnnotations`, `deleteAnnotations`, the `*OutputSchema()` builders). Do not add a second mechanism.
3. If `inputSchema` is unbounded on any tool, set `additionalProperties: false` in the shared schema builder (not tool by tool). First check how the handler treats unknown arguments (see To verify), because advertising `false` while the server still accepts extras is harmless, but making the server reject them is a behaviour change.
4. Descriptions: only where a real tool lacks it, add one sentence of sibling routing ("use `list_checks` to find a uid, then `get_check`") and, for writes, the actual side effects. Never write a quota or rate-limit claim that the code does not enforce. The server-wide routing text already lives in `serverInstructions` (`servercard.go`); do not duplicate it per tool.
5. Do not add `smithery.yaml`, `mcp-lock.json`, the badge or the generated A2A/x402/MPP files (Resolved open question 1).
6. If everything in 1 to 4 is already satisfied, the change is the test below plus a short note in `wiki/api-specification/` (the MCP page that lists tool conventions) saying the contract is enforced by that test, so the next scorer run can be answered with a link.
7. Bump `server.json` `version` only if the repo's release flow expects it in this change (release-please normally owns it); check before touching.

## Tests

- `server/internal/mcp/` (next to `handler_test.go` / `protocol_test.go`): a table test over every registered tool, for every toolset and access mode (anonymous, demo, authenticated) that exposes a different list, asserting a non-nil `OutputSchema`, `Annotations` with a non-empty `Title`, and `inputSchema.additionalProperties == false`. It must fail on a tool added without them (negative case: register a fixture tool without annotations and assert the test helper reports it). Extend the existing test if the 2026-09-28-01 spec already added one.
- If step 3 changes server behaviour: a handler test that a call with an unknown argument is rejected with a `VALIDATION_ERROR`-style tool error, and one that a call with only documented arguments still succeeds.
- `servercard_test.go` stays green (server card unchanged unless step 5 says otherwise).
- `make lint-back test` and `golangci-lint run ./...` (called directly) before pushing.

## To verify

- Do all 22 tools of the report, and every other registered tool, really have `OutputSchema` and `Annotations{Title}` at HEAD? My `grep -c` over `server/internal/mcp/*.go` failed (shell quoting), so only the `tools_statuspages.go` hits above are confirmed.
- Does any `inputSchema` set `additionalProperties`? Check `server/internal/mcp` for `additionalProperties` and for the schema builder (`objectSchema` or similar), and for how `handler.go` decodes tool arguments (strict or lenient).
- Are the three done specs named above on `main`? I only saw them listed under the `solidping-feat-mcp-auth-required` worktree.
- Does the live `https://solidping.io/api/v1/mcp` listing differ from the repo (older deployment)? If the gaps exist only there, the fix is a deploy, not code.
- Issue 587 has one comment (an autopilot skip note from 2026-10). Nothing else to read there.

## Resolved open questions

1. **Vendor-specific and paid items: add none of them.** No `smithery.yaml`, no `mcp-lock.json`, no Assay README badge, no generated A2A / x402 / Stripe MPP files, and do not buy the pack. The repo already uses a server card for Smithery, `mcp-lock.json` is not part of the MCP spec or the registry schema, and the badge is advertising for a third party. Fix only the schema/annotation/description gaps the code actually has.
2. **Close the issue once the PR is merged.** Comment on #587 with the verification table from step 1 (what was already in place, what this PR fixed) and close it, whatever the result. Do not leave the scorer thread open.
