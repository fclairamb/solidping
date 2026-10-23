---
model: sonnet
effort: medium
---

# MCP tool schemas: bound string inputs and disclose side effects

## Problem
A third-party scanner (Assay, `https://assay-207908449630.us-central1.run.app/v1/fixpack?repo=fclairamb/solidping`) grades the 23 MCP tools of the public server at 4.44/5 (Glama TDQS). Its findings, kept only where they are real:

1. "Unbounded inputSchema: `additionalProperties` != false" on every tool. Already fixed in the repo: `objectSchema` sets it (`server/internal/mcp/tools.go:165-176`) and `tools_contract_test.go:23` asserts it. The scanned server is probably older than HEAD. Do not redo this.
2. String properties have no `minLength` / `maxLength`.
3. Mutating tools (`create_check`, `update_check`, `delete_check`, `create_status_page`, `update_status_page`, `delete_status_page`) do not say in their description what they change: quota consumption, irreversibility, cascade (results, incidents, status page resources).
4. Tools with 2+ related params (`get_check`, `get_incident`, `get_status_page`, `probe`, `list_incidents`, `list_check_groups`, `list_status_pages`) do not say how params interact (UID or slug, which filters combine, which are mutually exclusive).
5. Some descriptions repeat return fields that `outputSchema` already lists (`list_*`, `delete_check`, `validate_check`, `run_check`, `diagnose_check`).

Ignore the scanner's "compiled" output and its paid pack: its rewritten descriptions drop real content (the `Returns {data, pagination}` sentence) and append generic boilerplate ("WHEN NOT TO USE: Do not call for unrelated mutations").

## Proposal
1. `stringProp` (`server/internal/mcp/tools.go:178`): add `maxLength` 2048 by default. Keep `minLength` 1 only for properties that are required or are identifiers (UID, slug, cursor). Do not put `minLength` on optional free text where empty means "clear the field" (check `update_*` handlers first).
2. For the six mutating tools above, add one sentence to the description: what it changes, whether it is reversible, and which cascade applies. Take the facts from the handlers (`tools_checks.go`, `tools_statuspages.go`), do not guess.
3. For the tools in point 4, add one sentence on param interaction, from the handler code.
4. Where a description restates the output fields listed in `outputSchema`, remove the restatement. Keep the pagination hint (`pass pagination.cursor back as cursor`).
5. Keep `server/internal/mcp/servercard.go` and any golden/snapshot of the tools list in sync.

## Tests
- `server/internal/mcp/tools_contract_test.go`: for every tool, each string property has `maxLength`; each required string property has `minLength` >= 1; every mutating tool's description is non-empty and mentions reversibility (assert a fixed keyword such as "permanent" / "reversible", chosen during implementation).
- Negative case: a tool call with a 5000-character `q` or `slug` is rejected with a `VALIDATION_ERROR`-style error if the handler enforces the bound, or at least does not crash (check what `handler.go` does with schema bounds first).
- Existing `additionalProperties: false` assertion keeps passing.

## To verify
- Whether `handler.go` validates arguments against `inputSchema` at all. If not, the bounds are advertised only (same stance as the `additionalProperties` comment at `tools.go:169`) and the negative test above reduces to "no crash".
- Whether the scanned server really predates `additionalProperties: false` (compare `GET /api/mgmt/version` of the scanned instance with `git log -S'"additionalProperties": false' -- server/internal/mcp/tools.go`).
