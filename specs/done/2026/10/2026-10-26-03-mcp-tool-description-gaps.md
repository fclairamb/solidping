---
model: haiku
effort: low
pr: batch
---

# MCP tool descriptions: close the few gaps an external TDQS report flags

## Problem
The third-party scorer Assay (`/v1/fixpack?repo=fclairamb/solidping`) grades our 23 MCP tools 4.83/5. Its deductions (Behavior: no side-effect/quota disclosure, Parameters: no cross-param prose, Conciseness: return fields duplicate outputSchema) are generic and partly stale: `delete_check` (`server/internal/mcp/tools_checks.go:429`) and `delete_status_page` (`tools_statuspages.go:340`) already state permanence, quota effect and required scope. Fix only the gaps that are real.

Do NOT adopt Assay's "compiled 5.0" rewrite: it appends boilerplate ("WHEN TO USE / WHEN NOT TO USE / PARAMETER INTERACTIONS"), deletes the sentence "Returns {data, pagination}" leaving a broken fragment, and adds `minLength`/`maxLength` we already control through `TestToolContract_StringInputsAreBounded`. The full pack is a paid upsell; buy nothing.

## Proposal
1. For each tool the report lists (`list_checks`, `get_check`, `create_check`, `update_check`, `list_incidents`, `get_incident`, `list_check_groups`, `probe`, `list_status_pages`, `get_status_page`, `create_status_page`, `update_status_page`), read its `*Def()` in `server/internal/mcp/tools_*.go`.
2. Add one sentence only where a real gap exists: a create/update side effect (quota, scheduling starts, notifications), or a filter interaction a caller would get wrong. Leave the rest alone.
3. `list_check_groups` (`tools_groups.go:31`): state that it takes no parameters and returns all groups unpaginated, if true.

## Tests
- `server/internal/mcp/tools_contract_test.go`: existing `TestToolContract_*` still pass (negative case: no description loses its reversibility wording).
- No new test unless a description gains a checkable claim.

## To verify
- Whether `list_check_groups` really is unpaginated (`tools_groups.go:44`).
- Whether `create_check` / `update_check` descriptions already mention quota and scheduling.
