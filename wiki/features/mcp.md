# Model Context Protocol (MCP)

A first-party MCP server lets an LLM client (Claude Desktop, the
Anthropic SDK, custom agents) talk to SolidPing as a *tool*. The LLM
can list checks, read incidents, propose changes, and run diagnostic
prompts — all over a single HTTP endpoint with the same auth and org
isolation as the REST API.

## Endpoint

Mounted at `/api/v1/mcp`
([`server/internal/app/server.go`](../../server/internal/app/server.go)).
JSON-RPC traffic goes over **POST**, behind `RequireMCPAuth` — with one hole
punched in it for the handshake, see *Anonymous handshake* below. The transport
is **MCP Streamable HTTP** (the spec's HTTP+SSE binding); requests carry a
JSON-RPC envelope and the server responds with either a single JSON-RPC
reply or an SSE stream depending on the request type.

The other methods are handled per the transport spec
([`mcp/handler.go`](../../server/internal/mcp/handler.go)):

- **GET** (unauthenticated): a client probing with
  `Accept: text/event-stream` gets `405` + `Allow` (we don't serve
  server-initiated SSE streams); anything else — a human in a browser —
  gets a `302` to the dashboard's MCP setup page (`/d/mcp?from=get`),
  which shows a "you opened the API endpoint in a browser" hint.
- **DELETE** (behind `RequireMCPAuth`): explicit session termination —
  deletes the session named by `Mcp-Session-Id` (404 if unknown or
  belonging to another org, 204 on success).

Unmatched paths under `/api/` never fall through to the SPA shell — they
answer the standard JSON error shape (`NOT_FOUND`).

The supported protocol versions are **`2025-06-18`** (latest — the
revision that introduced tool `annotations`, `outputSchema` and
`structuredContent`, all of which this server now provides) and
**`2025-03-26`** ([`mcp/handler.go`](../../server/internal/mcp/handler.go)).
Newer versions get added to the front of the supported list as they're
adopted.

Protocol negotiation per MCP spec: if the client requests a version we
support, we echo it back; otherwise we return our latest. The client
is responsible for disconnecting if it can't speak what we returned.

## Anonymous handshake

`initialize` and `notifications/initialized` are served with **no credentials**
(spec 2026-09-16-15). MCP directories probe a server by sending `initialize`;
answering 401 made the listing fail, and handing a directory a real bearer token
was the worse trade. What the handshake returns — the negotiated protocol
version, three static capability flags, `ServerInfo{"solidping", …}` — is public
and identical for every caller, so it fingerprints no tenant.

The hole is deliberately narrow
([`mcp/anonymous.go`](../../server/internal/mcp/anonymous.go)):

- The gate wraps `RequireMCPAuth` rather than replacing it. It peeks the body
  (capped at 64 KiB, then restored byte-for-byte) and lets the request through
  only when the body is a **single JSON-RPC object** whose `method` matches one
  of the two handshake names **exactly** — no prefix, no case folding. A batch
  (`[{initialize},{tools/list}]`), two concatenated objects, an oversized body,
  or `initialize_and_dump` all fall through to `RequireMCPAuth` and get the
  usual 401 + `WWW-Authenticate` challenge.
- A caller that presents any credential always goes through `RequireMCPAuth`,
  so a stale token still gets the challenge that starts OAuth discovery instead
  of silently degrading to an anonymous answer.
- The anonymous handshake mints **no session** and sets no `Mcp-Session-Id`:
  nothing is left behind for a later unauthenticated call to reuse, and a probe
  loop cannot grow the session map.
- `Handler.handleAnonymous` re-checks the method independently of the routing,
  so a method added to `dispatch` later cannot become anonymously reachable by
  accident.

Tests: [`internal/mcp/anonymous_test.go`](../../server/internal/mcp/anonymous_test.go)
(gate + handler) and
[`internal/app/mcp_anonymous_test.go`](../../server/internal/app/mcp_anonymous_test.go)
(the real router end to end).

## Authentication & scopes

Every other MCP request carries the same JWT or PAT as any other API call. Sessions
inherit the org from the token; the `orgSlug` is implicit.

Two custom scopes gate access:

| Scope | Permission |
|---|---|
| `mcp` | All tools (read + write). |
| `mcp:read` | Read-only tools only. The server denies any tool name beginning with `create_`, `update_`, `delete_`, or `set_` ([`scope.go:67`](../../server/internal/mcp/scope.go)). |

A token with **no scopes at all** (a full dashboard JWT) is treated as
having full access — back-compat for the dashboard's own MCP-over-PAT
flow that pre-dates the scope split. The deny-list (rather than
trusting per-tool annotations) stays the enforcement mechanism: every
new write tool naturally falls under one of the four prefixes, and a
stray miss is a reviewable oversight rather than a silent privilege
escalation. The MCP 2025-06-18 `annotations` every tool now carries
(`readOnlyHint`, `destructiveHint`, `idempotentHint`, `openWorldHint`,
mirrored from the same prefixes in [`annotations.go`](../../server/internal/mcp/annotations.go))
are advisory metadata for the *client*, not an authorization input —
the spec requires clients to treat them as untrusted, so the server
never makes a gate decision from them.

PAT tokens can be issued with `mcp:read` for safer agent embedding —
"let the assistant browse our infra without letting it create or
delete anything".

## Available tools

Tools are static, registered once at startup
([`mcp/tools.go`](../../server/internal/mcp/tools.go)). Current
inventory (this list goes stale; the source of truth is `registerTools()`):

**Checks**
- `list_checks`, `get_check`, `create_check`, `update_check`,
  `delete_check`
- `validate_check` — dry-run a config without persisting
- `diagnose_check` — fetch recent results, dependencies, recent
  incidents in one call for an LLM to reason over
- `list_check_types`, `get_check_type_samples` — discoverability
  surface so an LLM can pick a type and request a starter config

**Results**
- `list_results` — time-bounded; the server forces a default window
  rather than letting an LLM ask for "all results" and pull a year of
  rows

**Incidents**
- `list_incidents`, `get_incident`

**Connections (channels)**
- `list_connections`, `create_connection`

**Check groups**
- `list_check_groups`

**Regions**
- `list_regions`

**Status pages** (full CRUD)
- pages: `list_status_pages`, `get_status_page`, `create_status_page`,
  `update_status_page`, `delete_status_page`
- sections: `list_status_page_sections`, `create_status_page_section`,
  `update_status_page_section`, `delete_status_page_section`
- resources: `list_status_page_resources`,
  `create_status_page_resource`, `update_status_page_resource`,
  `delete_status_page_resource`

**Maintenance windows**
- `list_maintenance_windows`, `get_maintenance_window`,
  `create_maintenance_window`, `update_maintenance_window`,
  `delete_maintenance_window`, `set_maintenance_window_checks`

Tool descriptions are LLM-facing prose — they explain *when* to pick a
tool, not just what it does. Pagination (`limit` / `cursor`) is
exposed where relevant; `with` flags surface eager-load options.

## Smithery server card

Smithery scans the live URL. `tools/list` and friends answer 401, so its
indexer saw an empty server ("No capabilities", "No config schema provided",
quality score 60/100). The fix is a static server card (spec 2026-09-30-03):
`GET /.well-known/mcp/server-card.json`, public, served on every host like the
OAuth `.well-known` routes ([`mcp/servercard.go`](../../server/internal/mcp/servercard.go),
wired next to `/mcp` in `internal/app/server.go`).

- It is **generated from the live registry** (`h.tools`, `getResourceDefinitions`,
  `listPrompts`), so it cannot drift from `tools/list`. The registry is not
  filtered per caller, so the card lists the full set and is byte-identical for
  every caller (no org data).
- Keys read by Smithery (https://smithery.ai/docs/build/external): `serverInfo`,
  `authentication` (`required: true`, `oauth2`), `tools`, `resources`,
  `prompts`, and `configSchema` (empty object, no `required`: sign-in is OAuth,
  no key to paste). `instructions`, `description`, `iconUrl`,
  `documentationUrl` and `serverInfo.{title,websiteUrl,repository,license}` are
  SEP-1649 metadata: Smithery's page does not list them, so they may be ignored.
- `initialize` also returns `instructions` (routing guidance), authed and
  anonymous. The same text is on the card.
- `isAnonymousMethod` is unchanged: `tools/list`, `resources/list` and every
  `tools/call` still need a token. The card is the only new anonymous surface.
- Tool names stay snake_case (a dotted name is invalid for Anthropic/OpenAI
  tool APIs and would break existing clients), so the target is 95/100.
- The registry lint (`TestEveryToolPassesTheRegistryLint`) enforces verb-first
  descriptions and a description on every input property, nested and array
  items included.

Re-scan after a release: Smithery dashboard, server settings, "Rescan".

Rubric as documented in the spec (from two public write-ups, NOT yet checked
against the dashboard tooltips): server metadata 30, config UX 25, tool
descriptions 12, parameter descriptions 11, annotations 7, tool names 5 (skipped
on purpose), prompts 5, resources 5, instructions varies. Score reached: not
measured yet (needs a prod deploy).

### Manual steps for Florent

1. After the next release is deployed to prod, open
   https://smithery.ai/servers/fclairamb/solidping/settings logged in, trigger a
   re-scan, and check the scan finds the tools, prompts and resources and no
   longer warns "No config schema provided".
2. Record each rubric dimension's points and tooltip here, correct the table
   above if it differs, and write down the score reached. Done at 95/100 (only
   tool names missing); if another dimension is short, fix it first.
3. Set in the settings UI whatever the card does not carry:
   - Description: `Uptime and incident monitoring. List, create and diagnose
     checks, read incidents and results, manage status pages and maintenance
     windows.`
   - Homepage `https://www.solidping.io`, docs `https://docs.solidping.io/docs`,
     repository `https://github.com/fclairamb/solidping`, license AGPL-3.0,
     icon `web/dash0/public/logo.png`.
   - Categories: `monitoring`, `devops`, `observability`.
   - Tags: `uptime`, `incident-management`, `status-page`, `alerting`,
     `synthetic-monitoring`.

## Prompts

The server registers three named prompts
([`mcp/prompts.go:34`](../../server/internal/mcp/prompts.go)). A prompt is
a parameterized message-template the client can request by name and
hand to the LLM; they're shortcuts for common multi-tool workflows.

| Prompt | Args | What it produces |
|---|---|---|
| `triage_incident` | `incidentUid` | Pulls the incident, its event timeline, and the affected check's recent results. Asks the model to summarize what's happening, the likely cause, and what to check next. |
| `summarize_org_health` | (none) | One-paragraph summary of the org's monitoring posture. |
| `draft_status_update` | `incidentUid`, optional `tone` | Drafts a customer-facing status-page update for human review. Tone is `technical` or `non-technical`. |

Prompts are not auto-published — they're seeds for a conversation
between the client and the LLM. The output never bypasses human
review.

## Sessions

Each successful **authenticated** `initialize` call mints a session ID
(`session.id`). Subsequent calls echo it in the `Mcp-Session-Id`
header. Sessions carry the negotiated protocol version, the client
info, the org slug, and timestamps.

The in-process stdio server mints no session: the process is the session
(see below).

Sessions expire after **1 hour of inactivity** (`sessionTTL`,
[`handler.go:33`](../../server/internal/mcp/handler.go)) and a cleanup
loop sweeps every 5 minutes. After expiry the client must re-initialize.

## stdio bridge (`sp mcp`)

Some consumers want a command that speaks MCP on stdin/stdout rather than a
URL: desktop agents (Claude Desktop, Cursor) that are simplest to configure
with a command, and MCP directories that run `mcp-proxy -- <command>`.
`sp mcp` (also reachable as `solidping client mcp`) is that command, in front
of a remote instance (spec 2026-09-26-04,
[`pkg/cli/mcp.go`](../../server/pkg/cli/mcp.go)).

It adds no server surface: each stdin line is POSTed to
`<url>/api/v1/mcp` with the CLI's bearer credential, so every auth, role,
scope and demo rule above applies unchanged.

- **Framing.** Newline-delimited JSON-RPC both ways. Nothing but JSON-RPC
  ever reaches stdout; logs go to stderr (`-v` for per-request debug lines).
  Multi-line JSON from the server is compacted to one line, and both
  `application/json` and `text/event-stream` replies are relayed.
- **Session.** The `Mcp-Session-Id` returned by `initialize` is sent on every
  later request. EOF on stdin, SIGINT or SIGTERM closes it with
  `DELETE /api/v1/mcp`.
- **Credential.** `--token` / `SP_TOKEN` uses a PAT verbatim and never writes
  it to disk. Otherwise the credential is resolved like every other `sp`
  command (token file from `sp auth login`, PAT in `settings.json`,
  auto-login). On a 401 the bridge renews it without prompting (a newer
  token file, then the refresh grant, then auto-login; stdin is the protocol
  stream, so it can never ask for a password) and replays the request once.
  A failed renewal is not retried for 30 s.
- **Errors.** A reply that is not JSON-RPC (the auth middleware's REST error
  shape, a proxy's HTML page, an unreachable server) becomes a JSON-RPC error
  for the request's id: `-32001` for a 401 (the message names `sp auth login`
  and `SP_TOKEN`), `-32603` otherwise, with `data.httpStatus` and the REST
  `code`. A notification never gets a reply, not even the server's own
  error for it (the id-less 403 for a token without the `mcp` scope); it
  only gets a stderr line. A server error that answers a request without an
  id gets the request's id stamped on, so the client can match it.
- **Batches.** The endpoint takes one message per POST, so a JSON array is
  relayed member by member and answered with one array of the requests'
  replies (nothing for an all-notification batch; a single null-id
  `Invalid Request` for an empty one, as JSON-RPC mandates).
- **Exit.** `sp mcp` never hands an error back to the binary's `main`: it
  logs to stderr and exits 1. The `solidping client` subtree also points
  slog at stderr, because the server binary's default logger writes to
  stdout.
- **Order.** Messages are relayed one at a time, so replies come back in
  request order and `initialize` always lands its session id first. A long
  `tools/call` therefore delays the next request.
- **No credential at all** still starts: the anonymous handshake works, and
  every other call gets the error above.

Claude Desktop (`claude_desktop_config.json`):

```json
{
  "mcpServers": {
    "solidping": {
      "command": "sp",
      "args": ["mcp"],
      "env": {
        "SOLIDPING_URL": "https://solidping.acme.com",
        "SP_TOKEN": "pat_..."
      }
    }
  }
}
```

Drop `SP_TOKEN` to use the login saved by `sp auth login` instead.

This does not by itself solve directories that build a container with no
server in it: there is still no instance and no token to give the
bridge. That is the in-process variant below, which lives on the server
binary's top-level `mcp` command. The two cannot clash: the bridge sits under
`solidping client`, the in-process server at the root.

## In-process stdio server (`solidping mcp --stdio`)

MCP directories such as Glama build a container and run
`mcp-proxy -- <stdio command>`, and refuse `mcp-remote`. `solidping mcp --stdio`
(spec 2026-09-26-05, [`cmd_mcp.go`](../../server/cmd_mcp.go),
[`mcp/stdio.go`](../../server/internal/mcp/stdio.go)) runs from the image
alone: no running server, no token, no bridge. It replaces the old Glama
script (serve in the background, log in as the seeded admin, rotate its
password, bridge with supergateway).

```json
["solidping", "mcp", "--stdio"]
```

- **What runs.** Everything `serve` runs except the HTTP listener: database,
  migrations, system-config overlay, startup seeds, the startup job, the job
  worker and the check workers. Checks created over MCP therefore produce
  results. `app.Server.SetHeadless` also drops the other inbound surfaces of
  the api role: the TLS edge, the heartbeat beat listeners, the Telegram
  webhook self-heal, Slack Socket Mode and the Discord Gateway. `SP_NODE_ROLE`
  still picks jobs and checks (`jobs` alone runs no check). `agent` is refused:
  it has no database.
- **Principal.** No token: the process already holds the database
  credentials, so acting as a member grants nothing SQL access would not.
  The session acts as the **owner of `--org`** (the global flag, default
  `default`, env `SOLIDPING_ORG`): the oldest live owner membership when there
  are several, ties on `created_at` broken by membership uid, soft-deleted
  memberships and users skipped. `--user <email|uid>` picks another member
  instead. A value with no `@` must be a canonical uuid, or it is "user not
  found" without a query (Postgres would answer a uuid syntax error). An unknown
  org, an unknown user, a user who is not a member, or an org with no owner
  (and no `--user`) stops the command with a message on stderr and exit
  code 1. The principal is resolved once, after the startup job, so on a
  fresh database it is the seeded `admin@solidping.io`.
- **must_change_password is ignored.** That rotation protects a password, and
  none is involved. The seeded admin works as is.
- **Claims.** The principal's claims are those of a full-scope (`mcp`) PAT of
  the same user in the same org: role from the membership row (`superadmin`
  for a super admin). They go on the context through
  `middleware.WithMCPPrincipal`, the same function `RequireMCPAuth` ends with,
  so `checks.Service` stamps `created_by`, the demo flag is re-derived from
  the user row, and the scope, demo and role gates of `tools/call` run as
  over HTTP. A `viewer` principal can read and cannot write.
- **Framing.** Newline-delimited JSON-RPC, answered one message at a time, in
  order. Ids are echoed byte for byte. A notification (no id, or a null id)
  is never answered, not even with an error. Batches are answered with one
  array (nothing for an all-notification batch). An unparseable line gets a
  null-id parse error. A panicking tool costs one `-32603`, not the process.
- **Stdout is JSON-RPC only.** The command's `Before` hook points slog at
  stderr, the config's logger goes to stderr, and `os.Stdout` is swapped for
  `os.Stderr` for the whole run, so anything printing to stdout on its own
  (embedded Postgres, a stray `fmt.Print`) lands on stderr. A bad flag is
  reported on stderr too (`OnUsageError`), since urfave would print the help
  on stdout. `TestMCPStdioCommandFreshDatabase` runs the real command in a
  child process and fails on any non-JSON-RPC stdout line.
- **Session.** The process is the session: `initialize` mints no
  `Mcp-Session-Id` and adds nothing to the HTTP session map.
- **Exit.** EOF on stdin, SIGINT or SIGTERM stops the session and shuts the
  server down through the normal path (exit 0). A database fault that stops
  the server ends the session too. SIGPIPE is notified, so a client that
  closes its end of stdout makes the next reply fail with EPIPE and the
  command shuts down gracefully with exit 1, instead of the Go runtime
  killing it mid-flight.

A directory that builds its own image around the binary (Glama) uses the
command above as is. With the published image, override its entrypoint (it
is `solidping serve`) and its HTTP healthcheck, which has nothing to probe
here. The image already defaults to SQLite under `/data`:

```json
{
  "mcpServers": {
    "solidping": {
      "command": "docker",
      "args": ["run", "-i", "--rm", "--no-healthcheck",
               "--entrypoint", "/app/solidping", "-v", "solidping-data:/data",
               "ghcr.io/fclairamb/solidping", "mcp", "--stdio"]
    }
  }
}
```

## Adding a tool

1. **Define the tool**: add a `…Def()` function in the matching
   `tools_<area>.go` file returning a `ToolDefinition` with `Name`,
   `Description`, and `InputSchema`. Use the schema helpers in
   `tools.go` (`objectSchema`, `stringProp`, `intProp`,
   `objectProp`).
2. **Declare its annotations**: pick the constructor in
   `annotations.go` matching the verb class — `readOnlyAnnotations`,
   `createAnnotations`, `updateAnnotations`, `replaceAnnotations`
   (full-replacement writes that can clear a collection),
   `deleteAnnotations` — with a sentence-case title. The title is
   mirrored onto the definition automatically. The
   `TestEveryToolDeclaresAnnotationsAndOutputSchema` gate fails the
   build if the hints drift from the name-prefix scope gate, so the
   class you pick must match the prefix you pick in step 6.
3. **Declare an output schema**: `OutputSchema` with an object root
   (`objectSchema(...)` / `dataOutputSchema(...)`), matching what
   `marshalResult` will actually return. If the handler returns a bare
   slice, wrap it as `{data: [...]}` first — `structuredContent` must
   be a JSON object, and once an `outputSchema` is declared the server
   MUST return conforming structured content (MCP 2025-06-18). Never
   pair an output schema with `textResult`.
4. **Implement the handler**: a method on `*Handler` with the
   signature `func(ctx context.Context, orgSlug string, args map[string]any) ToolCallResult`.
5. **Register it**: add a `{def, fn}` entry to the slice in
   `registerTools()` ([`tools.go:9`](../../server/internal/mcp/tools.go)).
6. **Pick the right name prefix** for the scope gate:
   - `list_…` / `get_…` / read verbs → mutation prefix detection skips it; `mcp:read` allowed.
   - `create_…` / `update_…` / `delete_…` / `set_…` → blocked from `mcp:read`.
7. **Use `objectProp("description")` for nested-object args** — the
   LLM will pass JSON; `stringProp` would force the model to stringify
   first.
8. **Limit response size**. Tools that can return many rows must accept
   `limit` (and ideally `cursor`) and clamp to a reasonable max
   (existing tools cap at 100). LLMs degrade fast on huge responses.
9. **Test**: add an entry to the matching `tools_<area>_test.go`. The
   test framework spins up the handler with a fake DB and invokes the
   tool over the JSON-RPC surface — same shape the LLM client uses.

### Writing the description

Descriptions are graded (Glama TDQS re-scores the published server on
Behavior / Conciseness / Completeness / Parameters / Purpose / Usage
Guidelines), and the four things every description must carry are:

1. **What it returns** — one sentence, e.g. `Returns the updated section.`
   or `Returns {data: [...]}.`
2. **Side effects and prerequisites** — what a successful call does to
   the world, what must already exist, what is refused.
3. **Sibling routing** — `Use X instead of Y when Z.`, explicit, so an
   agent never has to infer usage from the tool name.
4. **The exact auth trailer** — reads end with
   `Read-only: works with mcp:read tokens.`; writes end with
   `Requires the mcp scope (mcp:read tokens are refused) and at least
   the user role in the organization.`

Keep it front-loaded and tight (~4–6 sentences); never restate what
the input schema already documents parameter-by-parameter — that earns
no points and costs tokens. `TestAllToolDescriptionsMeetMinimum`
enforces the floor.

## Caveats

- **Org isolation is per-token**. There is no "list other orgs" tool;
  the session's org is fixed at initialize time. To work in two orgs,
  open two sessions with two PATs.
- **No streaming partial results** today. A `list_*` tool returns the
  full slice (capped by `limit`); the SSE binding is used for the
  protocol envelope, not for chunked tool output.
- **`structuredContent` and `outputSchema` are wired (2025-06-18).**
  Every tool declares an object-rooted `outputSchema` and returns its
  payload both as `structuredContent` and as stringified JSON in
  `Content` (the dual-form transition pattern). Two shape changes came
  with it: list tools that used to return a bare JSON array now wrap
  rows as `{data: [...]}` (what MCP requires for `structuredContent`,
  and what the REST API already does), and the delete / replace tools
  that used to return a plain confirmation sentence now return a small
  object (`{deleted: true, identifier: ...}` etc.).

## Where to look in the code

| Concern | File |
|---|---|
| HTTP entrypoint, session lifecycle | [`server/internal/mcp/handler.go`](../../server/internal/mcp/handler.go) |
| JSON-RPC envelope and protocol types | [`server/internal/mcp/protocol.go`](../../server/internal/mcp/protocol.go) |
| Protocol version negotiation | [`server/internal/mcp/handler.go:45`](../../server/internal/mcp/handler.go) |
| Scope gating | [`server/internal/mcp/scope.go`](../../server/internal/mcp/scope.go) |
| Tool registry | [`server/internal/mcp/tools.go`](../../server/internal/mcp/tools.go) |
| Tool annotation constructors | [`server/internal/mcp/annotations.go`](../../server/internal/mcp/annotations.go) |
| Tool implementations | [`server/internal/mcp/tools_*.go`](../../server/internal/mcp/) |
| Prompts | [`server/internal/mcp/prompts.go`](../../server/internal/mcp/prompts.go) |
| stdio bridge (`sp mcp`) | [`server/pkg/cli/mcp.go`](../../server/pkg/cli/mcp.go) |
| In-process stdio server (`solidping mcp --stdio`) | [`server/cmd_mcp.go`](../../server/cmd_mcp.go), [`server/internal/mcp/stdio.go`](../../server/internal/mcp/stdio.go) |

## Origin

The MCP surface shipped across more than a dozen specs in May 2026.
Highlights:

- [`2026-05-03-25-mcp-diagnose-check.md`](../../specs/done/2026/05/2026-05-03-25-mcp-diagnose-check.md) — the diagnose tool
- [`2026-05-03-26-mcp-incident-events.md`](../../specs/done/2026/05/2026-05-03-26-mcp-incident-events.md) — incident timeline access
- [`2026-05-03-27-mcp-status-pages.md`](../../specs/done/2026/05/2026-05-03-27-mcp-status-pages.md) — status-page CRUD
- [`2026-05-03-28-mcp-maintenance-windows.md`](../../specs/done/2026/05/2026-05-03-28-mcp-maintenance-windows.md)
- [`2026-05-03-29-mcp-check-types-samples-validate.md`](../../specs/done/2026/05/2026-05-03-29-mcp-check-types-samples-validate.md)
- [`2026-05-03-30-mcp-tighten-tool-descriptions.md`](../../specs/done/2026/05/2026-05-03-30-mcp-tighten-tool-descriptions.md)
- [`2026-05-03-34-mcp-prompts.md`](../../specs/done/2026/05/2026-05-03-34-mcp-prompts.md)
- [`2026-05-03-38-mcp-scoped-tokens.md`](../../specs/done/2026/05/2026-05-03-38-mcp-scoped-tokens.md) — `mcp:read` scope
- [`2026-05-03-39-mcp-protocol-version-negotiation.md`](../../specs/done/2026/05/2026-05-03-39-mcp-protocol-version-negotiation.md)
