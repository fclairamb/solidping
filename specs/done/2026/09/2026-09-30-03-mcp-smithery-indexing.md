---
model: sonnet
effort: high
---

# Smithery lists the MCP server with no capabilities and a 60/100 quality score

## Problem
Smithery's indexer hits `/mcp` and reports (v0.36.1):

- `Server metadata discovered (OAuth required). Warning: No config schema provided.`
- `initialize` works (`name: solidping, version: v0.36.1`).
- `tools/list`, `resources/list`, `prompts/list` and triggers all fail with `-32001 "Authorization required"` and an `authorizationUrl` on `connect.smithery.ai/.../setup`. Smithery lists **no capabilities**, so the listing shows an empty server.

The listing's quality score on https://smithery.ai/servers/fclairamb/solidping/settings is **60/100**. The goal is 95/100: every dimension except dot-notation tool names (see Decisions).

Today only the two handshake methods are anonymous (`server/internal/mcp/anonymous.go:31`, `isAnonymousMethod`). Every other method gets `401 "Authentication required"` from `handleAnonymous` (`server/internal/mcp/handler.go:329-346`). This was a deliberate decision (the tool surface is called "a product fingerprint", `anonymous.go:27`), made for Glama in spec `2026-09-16-15-mcp-anonymous-initialize`. Glama got a different answer: a stdio in-process build, `wiki/features/mcp.md:265`. Smithery scans the live URL, so it cannot use that path.

The `-32001` envelope is Smithery's gateway wrapping our 401 (our code is `CodeInvalidRequest`). The fix is on our side: give Smithery something it can read without a token.

### Smithery quality rubric
Smithery doesn't publish its rubric. This one comes from two public write-ups, one of which reached 100/100 ([kooexperience.com](https://kooexperience.com/blog/posts/create-mcp.html), [dev.to](https://dev.to/francofuji/your-mcp-server-scores-60100-on-smithery-what-it-means-and-how-to-hit-100-1h2b)). Check it against the dashboard tooltips first (see To verify).

| Dimension | Points | What earns it | SolidPing today |
|---|---|---|---|
| Server metadata | 30 | name, description, homepage, icon, categories/tags on the listing | unknown, probably partial |
| Config UX | 25 | a `configSchema` exists, minimal, no needless required fields | missing (the warning) |
| Tool descriptions | 12 | verb-first, names the resource, says when to call it | good in code, invisible to Smithery |
| Parameter descriptions | 11 | every parameter has a description and type, with an example where useful | helpers force a `desc` (`server/internal/mcp/tools.go:89-125`), unverified for every tool |
| Annotations | 7 | `readOnlyHint`, `destructiveHint`, `idempotentHint`, `openWorldHint` | done (`annotations.go`), invisible to Smithery |
| Tool names | 5 | `domain.action` dot notation (`checks.list`) | snake_case (`list_checks`) |
| Prompts | 5 | at least one prompt | 3 prompts (`prompts.go:37-69`), invisible |
| Resources | 5 | always awarded | ok |
| Server instructions | varies | `instructions` string in the `initialize` result | missing (`InitializeResult`, `protocol.go:61-65`, has no field) |

Most of the missing points come from Smithery not being able to see what already exists. The server card fixes that.

## Proposal
1. **Static server card.** Add a public, unauthenticated `GET /.well-known/mcp/server-card.json` in a new file `server/internal/mcp/servercard.go`. Shape (from https://smithery.ai/docs/build/external):
   ```json
   {
     "serverInfo": {"name": "solidping", "version": "<version.Version>"},
     "authentication": {"required": true, "schemes": ["oauth2"]},
     "tools": [{"name", "title", "description", "inputSchema", "outputSchema", "annotations"}],
     "resources": [...],
     "prompts": [{"name", "description", "arguments"}]
   }
   ```
   - Build it from the same registry the handler serves (`h.tools` at `handler.go:431`, `resources.go`, `prompts.go`), so it can never drift from `tools/list`.
   - Register it where `/mcp` and the OAuth `.well-known` routes are wired (`grep -rn "well-known" server --include='*.go'`).
   - Keep `tools/list` etc. behind auth. Do NOT widen `isAnonymousMethod`. The card is the only new anonymous surface.
2. **Config schema (25 pts).** Add `"configSchema": {"type": "object", "properties": {}, "additionalProperties": false}` to the card, plus a `description` saying sign-in is OAuth and no key is needed. This clears the "No config schema provided" warning without inventing fields. If Smithery reads it from somewhere else (the deployment payload, `smithery.yaml`), put it there too.
3. **Server instructions.** Add `Instructions string `json:"instructions,omitempty"`` to `InitializeResult` (`protocol.go:61`). Set it in `handleInitialize` (`handler.go:384`), for the anonymous path too. It's static text, so it's safe there. Content: 5-10 lines of routing guidance.
   - Start with `list_checks` or `list_incidents`.
   - Use `diagnose_check` for "why is X down".
   - Run `validate_check` before `create_check`.
   - Use `get_check_type_samples` to learn a check type's config.
   - Status page writes need the `mcp` scope.
   Put the same text in the card under `instructions` if Smithery's card schema accepts it.
4. **Descriptions audit (12 + 11 pts).**
   - Make every tool description start with a verb, name the resource, and say when to call it rather than a neighbouring tool.
   - Give every `inputSchema` property a non-empty description. Add a concrete example for free-form strings: `url`, `host`, `period`, `cron`, uids.
   - Enforce both with a registry test, not by review. Follow the existing Glama TDQS guidance in `wiki/features/mcp.md:387`.
5. **Tool names (5 pts): don't rename them.** They stay snake_case (see Decisions). Don't add dotted aliases either.
6. **Listing metadata (30 pts).** Most of this is set in the Smithery settings UI, not in code. The implementer must:
   - check whether the card (or `smithery.yaml`) accepts `description`, `homepage`, `icon`, `repository`, `license`, `categories`, `tags`. If it does, add them to the card: homepage `https://www.solidping.io`, docs `https://docs.solidping.io/docs`, repo `https://github.com/fclairamb/solidping`, the license from the repo's `LICENSE`, and the icon from the existing brand asset under `web/`.
   - list whatever can only be set in the UI under a "Manual steps for Florent" section in `wiki/features/mcp.md`, with the exact values to paste in: a 1-2 sentence description, categories such as "monitoring", "devops", "observability", and tags.
7. **Docs.**
   - Add a "Smithery" subsection to `wiki/features/mcp.md`: what the card is, that it's generated from the live registry, how to re-scan after a release, the rubric table above with the score reached, and the manual steps.
   - Add a changelog entry per `wiki/conventions/changelog.md`.

## Tests
- `server/internal/mcp/servercard_test.go`:
  - Served without credentials: `200`, `application/json`.
  - `tools` has exactly the set of names from the registry. Positive control: a known tool (`list_checks`) is present. Negative: an unregistered name is absent.
  - Every card tool carries `annotations` and `inputSchema`. Tools that have an output schema carry `outputSchema`.
  - `configSchema` is present and has no `required` array.
  - Deterministic: built twice with different request contexts (authed, anonymous, another org), it gives identical bytes, so no org data or per-caller fields leak.
- `server/internal/mcp/tools_test.go` (registry lint, the enforcement for step 4):
  - Every tool description is non-empty, over N chars, and doesn't start with "This tool" or "A tool".
  - Every property in every `inputSchema`, including nested objects and array items, has a non-empty `description`.
  - Negative control: a hand-built bad tool fails the helper.
- `server/internal/mcp/handler_test.go`: `initialize` returns a non-empty `instructions`, both authed and anonymous. Existing initialize tests stay green.
- `anonymous_test.go`: `POST /mcp` `tools/list` with no token still returns `401`, and `isAnonymousMethod("tools/list")` is still false.
- Route test: `GET /.well-known/mcp/server-card.json` works on every host (like `/d`, `/s`), not only the API host.

## To verify
- The real rubric. Open https://smithery.ai/servers/fclairamb/solidping/settings logged in, for example through the Chrome MCP (an anonymous fetch shows only "60/100"). Record each dimension's points and tooltip in the wiki before coding, and correct the table above if it differs.
- The exact server-card keys Smithery reads (`instructions`, `configSchema`, `outputSchema`, `annotations`, metadata fields) at https://smithery.ai/docs/build/external. A wrong key is silently ignored.
- Whether `h.tools` is filtered per caller (demo/role). If it is, the card must list the full public set.
- Where the OAuth `.well-known` routes are wired.
- After deploy to prod, trigger a Smithery re-scan and record the new score. The spec is done when the score is 95, with only the tool-name dimension missing. If any other dimension is still short, fix it before archiving.

## Decisions
- **The full tool list (names, descriptions, schemas) is exposed anonymously through the server card.** The tools are already published in the docs and scored by Glama, and keeping them private leaves Smithery showing an empty server.
- **Tool names stay snake_case. Target score is 95/100.** Skip the dot-notation dimension (5 pts):
  - The rename breaks every existing client config, saved prompt and the Glama listing.
  - Dots aren't valid in Anthropic and OpenAI API tool names (`^[a-zA-Z0-9_-]{1,64}$`), so some clients would have to mangle them.
  - Listing both spellings doubles the tool list and hurts the description scores.
