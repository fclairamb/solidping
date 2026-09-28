---
effort: medium
---

# Glama's TDQS review docks every MCP tool for missing annotations, output schemas and usage guidance

## Problem

The Glama listing (<https://glama.ai/mcp/servers/fclairamb/solidping>, scored
2026-09-26 across the 42 tools, server average **A3.5/5.0**) reports three
recurring gaps in the tool definitions, plus a server-level note:

1. **No `annotations` on any tool (42/42 called out).** Every Behavior critique
   opens with "no annotations are provided, so the description carries the full
   burden of behavioral disclosure". 25 tools score Behavior below 4, ten of
   them at 2. An agent cannot tell reads from writes, destructive calls from
   idempotent ones, before calling.
2. **No `outputSchema` on any tool (36/42 called out).** Completeness drops to
   2–3 on the tools whose return values would otherwise be obvious, because
   "with no output schema, the description leaves return values to inference".
3. **Weak usage/side-effect guidance (18 tools at Usage ≤ 3).** Recurring
   phrasing: "does not explicitly contrast with sibling tools", "the agent must
   infer usage from the name". The four worst tools — `create_status_page`,
   `create_status_page_section`, `update_status_page_resource`,
   `update_status_page_section` — all sit at **C2.9/5.0**, plus two at B3.4
   (`create_incident_publication`, `update_maintenance_window`).
4. Server-level: Tool Count 2/5 (42 tools > the 25-tool threshold) and
   Completeness 4/5 noting "integrations lack update/delete, and check groups
   are read-only".

Both gaps were structural: `ToolDefinition` (`server/internal/mcp/protocol.go`)
carried only `name`/`description`/`inputSchema`, and the protocol negotiation
listed only `2025-03-26` — the revision that introduced annotations,
`outputSchema` and `structuredContent` had deliberately been deferred
(spec `2026-05-03-33`, `handler.go` placeholder comment).

## Proposal

Address the three definition-level gaps (this spec). The server-level items are
deliberately out of scope: consolidating 42 CRUD tools under 25 is a breaking
rename for every existing MCP client, and the missing integration/check-group
write tools are new surface area, not definition quality.

1. **Bump the negotiated protocol to `2025-06-18`** (kept `2025-03-26` in the
   supported list; exact-match negotiation means old clients keep getting the
   version they ask for).
2. **Add `title`, `Annotations *ToolAnnotations` and `OutputSchema` to
   `ToolDefinition`**, with the four-hint `ToolAnnotations` struct emitting all
   hints explicitly (`omitempty` on a `false` would drop the key and let the
   client fall back to the spec defaults — `destructiveHint` and
   `openWorldHint` default to `true`).
3. **One annotation constructor per verb class** in the new
   `server/internal/mcp/annotations.go`: `readOnly…`, `create…`, `update…`,
   `replace…` (full-replacement writes that can clear a collection),
   `delete…`. `registerTools` mirrors `annotations.title` onto `Title`.
   `openWorldHint=false` everywhere — every tool only touches SolidPing data.
4. **An `outputSchema` on every tool**, object-rooted as the spec requires,
   with properties verified against the response DTO json tags. List tools that
   returned a **bare JSON array** now wrap rows as `{data: [...]}` — required
   for spec-conformant `structuredContent` and matching the REST convention —
   and the delete/replace tools that returned a plain sentence now return small
   objects (`{deleted: true, identifier: …}`), because an `outputSchema`
   obliges the server to return conforming structured content on success.
5. **Rewrite the flagged descriptions** with the four things every description
   must carry (return value, side effects + prerequisites, sibling routing,
   exact auth trailer), verified against the service code rather than asserted.

### Decisions

- **Annotations are advisory, never an authorization input.** The scope gate
  stays the name-prefix deny-list (`isMutationTool`); the spec requires clients
  to treat annotations as untrusted. A test asserts the two stay in sync so a
  new write tool cannot ship advertised as read-only.
- **`destructiveHint=true` only for `delete_*` and `set_*`** — the classes
  whose documented effect includes removing data (soft delete, clearing
  associations). Creates/updates are flagged non-destructive; `set_` is
  idempotent because full replacement lands in the same state.
- **Deletes stay `idempotentHint=true`** (HTTP DELETE semantics: a repeat has
  no *additional* effect even though it may answer not-found).
- **Output schemas document a verified subset**, not the whole DTO:
  undeclared properties remain allowed by JSON Schema's default, but every
  declared name/type is checked against the struct's json tags, and `required`
  is limited to fields that are never omitted.

## Result

- All 42 tools carry annotations, a title and an object-rooted output schema;
  verified on the wire (stdio probe against a scratch database): protocol
  `2025-06-18`, 42/42 complete, zero readOnly/prefix mismatches.
- New gates in `tools_test.go`
  (`TestEveryToolDeclaresAnnotationsAndOutputSchema`) pin the coverage, the
  hint-emission, the readOnly ↔ scope-gate sync and the per-class semantics.
- `wiki/features/mcp.md` and `web/docs/docs/features/mcp.md` updated
  (protocol version, annotation/output-schema rules, "writing the
  description" guidance, shape-change caveat).
- Deferred: tool-count consolidation and the integration/check-group write
  tools (server-level Completeness note).
