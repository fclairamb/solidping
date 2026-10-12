---
model: opus   # the mutation gate (scope, role, demo) is keyed on tool names today; re-keying it is a security change
effort: high
---

# Bring the MCP tool surface from 46 tools to 25 or fewer without losing capability

## Problem

Glama scores the server's "Tool Count" 2/5: 42 tools "exceeds the 25-tool threshold where agent selection becomes difficult". `registerTools` (`server/internal/mcp/tools.go:3`) now registers **46** tools, and `tools/list` (`server/internal/mcp/handler.go:452`) returns all of them to every client. Context cost and selection errors grow with the list.

Current breakdown:

| Area | Tools | Count |
|---|---|---|
| Checks | list/get/create/update/delete_check, validate_check, run_check, diagnose_check | 8 |
| Results, regions, groups, check types | list_results, list_regions, list_check_groups, list_check_types, get_check_type_samples | 5 |
| Incidents | list_incidents, get_incident, incident_notifications_list | 3 |
| Probes (read-only, open-world) | run_js_script, fetch_page, browser_snapshot | 3 |
| Integrations | list_integrations, create_integration | 2 |
| Maintenance windows | list/get/create/update/delete, set_maintenance_window_checks | 6 |
| Status pages | list/get/create/update/delete_status_page | 5 |
| Status page sections | list/create/update/delete | 4 |
| Status page resources | list/create/update/delete | 4 |
| Status page incidents | list/create/update, create_update, create/delete_incident_publication | 6 |
| **Total** | | **46** |

Glama also flags a naming outlier: `incident_notifications_list` (`tools_incidents.go:143`) breaks the `verb_noun` pattern of the other 45.

Constraints that shape the solution:
- The scope gate is a **name-prefix deny-list** (`isMutationTool`, `server/internal/mcp/scope.go:67`: `create_`/`update_`/`delete_`/`set_` plus `run_check`). The role gate (`mutationRoleDenial`) and demo gate (`demoToolRefused`) are keyed on the tool name too, and all three run in `handleToolsCall` (`handler.go:457`). Merging write tools under a new name silently drops them out of all three gates unless the gate is rewritten. This is the main risk of the whole change.
- Each tool carries one annotation class (`server/internal/mcp/annotations.go`: readOnly, create, update, replace, delete). `TestEveryToolDeclaresAnnotationsAndOutputSchema` (`tools_test.go:~118`) pins hint semantics and checks `ReadOnlyHint` against `isMutationTool`. A tool that both reads and deletes can only honestly say `destructive: true, readOnly: false`.
- Every tool needs an object-rooted output schema and `structuredContent`.

## Options weighed

1. **Merge read tools by `kind`.** Safe: the merged tool stays `readOnly`. Candidates: the 3 status-page list tools + `get_status_page` (one `get_status_page` with `include: [sections, resources, incidents]`), `get_check_type_samples` into `list_check_types` (optional `type`), `incident_notifications_list` into `get_incident` (`include: [notifications]`), the 3 probes into one `probe` tool (`kind: script|fetch|browser`; all three share `probeAnnotations`). Saves about 10. No annotation cost.
2. **Merge writes by `action`** (`manage_status_page_section` with `action: create|update|delete`). Saves a lot, but the merged tool must be annotated for its worst action (`destructive: true`, `idempotent: false`), which makes agent clients prompt for confirmation on harmless creates. Acceptable only for rarely used sub-resources (sections, resources, incident overlay), never for `*_check`.
3. **Upsert for create/update.** Rejected. Create is non-idempotent and update is a PATCH with omitted-field-kept semantics (`updateAnnotations`); an upsert blurs both, and `create_check`/`update_check` have large, different schemas (`tools_checks.go:176`, `:305`). Keep them separate.
4. **Opt-in toolsets** (`core`, `statuspages`, `admin`). Lets the default list stay small while no capability is removed. Needs a selector and a compatibility story for clients that rely on the full list.
5. **One `admin` mega-tool.** Rejected: untyped schema, defeats the output-schema and annotation contracts.

## Proposal

Combine 1 + 2 (sub-resources only) + 4. Target: **default `tools/list` = 23 tools**, full capability reachable with opt-ins.

### Toolsets

| Toolset | In default list | Tools | Count |
|---|---|---|---|
| `core` | yes | list_checks, get_check, create_check, update_check, delete_check, validate_check, run_check, diagnose_check, list_results, list_incidents, get_incident, list_check_types, list_regions, list_check_groups, probe | 15 |
| `statuspages` | yes | list_status_pages, get_status_page, create_status_page, update_status_page, delete_status_page, manage_status_page_section, manage_status_page_resource, manage_status_page_incident | 8 |
| `admin` | no (opt-in) | list_integrations, create_integration, list_maintenance_windows, get_maintenance_window, create_maintenance_window, update_maintenance_window, delete_maintenance_window, set_maintenance_window_checks | 8 |

Default = 23, all toolsets (`toolsets=all`) = 31. If 31 is still too many for the `all` case, `admin` can shrink to 5 by merging the maintenance list/get (`uid` optional) and create/update; left to the implementer, not required to hit the 25 target.

What each merge replaces (old name stays callable, see compatibility):
- `probe(kind, ...)` replaces `run_js_script`, `fetch_page`, `browser_snapshot`.
- `list_check_types(type?)` replaces `get_check_type_samples` (with `type`, returns the samples).
- `get_incident(uid, include?)` replaces `incident_notifications_list`.
- `get_status_page(uid, include?)` replaces `list_status_page_sections`, `list_status_page_resources`, `list_status_page_incidents`.
- `manage_status_page_section(action, ...)`, `manage_status_page_resource(action, ...)` replace the create/update/delete trios.
- `manage_status_page_incident(action: create|update|add_update|publish|unpublish, ...)` replaces `create_status_page_incident`, `update_status_page_incident`, `create_status_page_incident_update`, `create_incident_publication`, `delete_incident_publication`.

### Steps

1. **Rewrite the mutation gate first, before any merge.** Replace the prefix deny-list in `scope.go:67` with an explicit `toolMutates(name string, args map[string]any) bool` backed by a table (`name -> read | write | actions-that-write`). Fail closed: an unknown tool, a missing/unknown `action`, or a non-string `action` on a `manage_*` tool counts as a write. `handleToolsCall` passes `params.Arguments`. `mutationRoleDenial` and `demoToolRefused` take the same decision. Every `manage_*` call is a write for scope purposes (a read-only token refused on all of them). Do this as its own commit with the old tool set, tests green, so the gate change is reviewable alone.
2. **Add a tool registry with a toolset tag.** `registerTools` (`tools.go:3`) gets a `toolset` field per entry. `h.tools` keeps only what the session selected; `h.toolMap` keeps **every** tool, including legacy aliases, so `tools/call` never loses a name.
3. **Selector.** `?toolsets=core,statuspages,admin` (or `all`) on the MCP endpoint URL, parsed where the handler builds the session (`handler.go`, `negotiate`-style helpers in `negotiate_test.go` show the pattern). Absent = `core,statuspages`. Unknown toolset name = JSON-RPC invalid-params error naming the valid values. The selector only controls **listing**, never authorization: scope, role and demo gates apply identically to a tool listed or called by alias. Advertise the selector in `serverInstructions` and in `servercard.go`.
4. **Implement the read merges** (options 1) with new `readOnlyAnnotations` / `probeAnnotations` definitions and output schemas. `include` values are validated against an enum, unknown value = tool error, not silently ignored.
5. **Implement the `manage_*` write tools** with `replaceAnnotations`-like worst-case hints (`readOnly: false`, `destructive: true`, `idempotent: false`) and a description that lists per-action effects. Each action reuses the existing service call from the tool it replaces; no new business logic.
6. **Legacy aliases.** Keep the 14 replaced tool names in `toolMap` (callable, not listed by default). Each alias delegates to the new implementation with the equivalent `kind`/`action`/`include`, so there is one code path. They are listed only with `?toolsets=legacy` (documented as deprecated). Remove in a later release, tracked as a follow-up spec once the release notes have shipped one version.
7. **Rename `incident_notifications_list`** to `list_incident_notifications` as an alias-backed rename, if the user prefers to keep it as a separate tool (see Open questions). Either way the old name stays callable.
8. **Docs.** Update `wiki/features/mcp.md`, `web/docs/` MCP page, `serverInstructions`, the server card (`servercard.go`), and add a `CHANGELOG.md` entry noting the default list shrank and how to restore it (`?toolsets=all,legacy`). Update the Glama listing metadata if it lives in the repo.

## Backward compatibility

- **Calls never break**: every existing tool name still resolves in `toolMap` with the same arguments and the same result shape. A client that hard-codes `create_status_page_section` keeps working.
- **Listings change**: a client that discovers tools through `tools/list` will see 23 instead of 46 by default. Clients that cache the list or whitelist tool names (Claude Desktop allow-lists, Cursor) keep the old names callable but will not see new ones until refreshed. Mitigation: `notifications/tools/list_changed` is not needed because the list is fixed per connection; document `?toolsets=all,legacy` for anyone who needs the old surface.
- **Existing stdio mode** (`stdio.go`): no URL, so expose the selector as a flag/env (`SP_MCP_TOOLSETS`), default same as above.
- **Tokens**: unchanged. `mcp` and `mcp:read` keep their meaning; no new scope is introduced (a token scope selecting toolsets was rejected: it would silently hide tools from tokens already minted, and scope should answer "may write", not "what is shown").

## Annotations

- Read merges (`probe`, `list_check_types`, `get_incident`, `get_status_page`) stay `readOnly: true, idempotent: true`; `probe` keeps `openWorld: true` (`probeAnnotations`).
- `manage_status_page_*` are annotated for their worst action: `readOnly: false, destructive: true, idempotent: false, openWorld: false`. This is deliberately pessimistic; a per-action annotation does not exist in MCP 2025-06-18.
- No tool may be advertised `readOnly: true` if any of its actions writes. `run_check` stays a mutation (current behaviour).
- `create_check`, `update_check`, `delete_check`, and the status-page/maintenance/integration CRUD tools that remain keep their current annotation class unchanged.

## Tests

- `server/internal/mcp/scope_test.go`: table test of `toolMutates` over every registered tool name and every `manage_*` action; **negative cases**: unknown tool, missing `action`, `action` of wrong type, unknown `action`, uppercase `Delete` all count as writes. An `mcp:read` token is refused on every `manage_*` call and on every legacy alias of a write tool.
- `write_role_test.go` and `demo_test.go`: a `viewer` and the demo user are refused on `manage_*` and on the legacy aliases (`delete_status_page_section` called by name); the demo user can still create/update/delete its own checks.
- `tools_test.go`: `TestEveryToolDeclaresAnnotationsAndOutputSchema` iterates the full `toolMap` (listed + alias), not only `h.tools`; replace its `isMutationTool` cross-check with `toolMutates`. Add: `readOnly: true` implies no `manage_*`, and every tool whose table entry has a write action has `readOnly: false`.
- New `tools_toolsets_test.go`: default list has exactly the 23 tools and `len <= 25`; `toolsets=all` lists 31; `toolsets=all,legacy` lists the 14 aliases too; unknown toolset name is an invalid-params error; a tool outside the selected toolset is still callable and still gated. The test fails if a new tool is added without a toolset tag.
- Alias parity: for each of the 14 replaced tools, calling the old name and the new `kind`/`action`/`include` form yields identical `structuredContent` (reuse the fixtures in `tools_statuspages_test.go`, `tools_incidents_test.go`, `tools_jsauthoring_test.go`, `tools_checktypes_test.go`).
- `tools_statuspages_cover_test.go` and `tools_maintenance_test.go`: update the sub-resource tests to the new tools; keep the old-name tests as alias tests.
- `include` enum: unknown value returns a tool error; `get_status_page` with `include: []` returns the page only.
- `servercard_test.go`, `negotiate_test.go`: server card and instructions mention the selector.
- `stdio_test.go`: `SP_MCP_TOOLSETS` honoured.

## To verify

- How `tools/list` is reached per session today (`h.tools` is a handler-wide slice, not per-session): confirm where to hang a per-request toolset selection without a data race (`handler.go:452`).
- Whether `web/docs/` and `servercard.go` hard-code a tool count or list.
- Whether Glama reads the default `tools/list` or a fixed list (check the listing after release; if it introspects with no query string, the default of 23 is what it scores).
- That nothing in `web/dash0` or the terraform provider (`wiki/terraform-provider-api-audit.md`) calls MCP tools by name.

## Resolved open questions

**Decision (user, 2026-10-12): adopt the recommended answer of every question below. Implement exactly those recommendations.**

1. **Default toolset contents.** Recommended: `core` + `statuspages` (23 tools), `admin` opt-in. Alternative: `core` only (15) with everything else opt-in, better score but status-page agents must opt in.
2. **Keep `incident_notifications_list` as its own tool (renamed `list_incident_notifications`) or fold it into `get_incident`?** Recommended: fold it in, to save a slot; the old name stays as an alias.
3. **Legacy alias lifetime.** Recommended: one release with aliases callable and listed under `legacy`, then remove from the listing and keep them callable for one more release before deleting.
4. **Pessimistic `destructive: true` on `manage_status_page_*`.** Recommended: accept it (honest, and these tools are rarely used). Alternative: keep delete tools separate (`delete_status_page_section`, `delete_status_page_resource`) and give create/update merged tools the non-destructive `update` class, at a cost of 2 more tools (25 in the default list).
