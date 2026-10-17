---
model: opus
effort: high
---

# Let operators and agent owners add check types without changing the core

## Problem

Every check type is compiled into the binary. Adding one means editing the `switch`
statements in `server/internal/checkers/registry/registry.go:75` (`GetChecker`) and `:184`
(`ParseConfig`), the `checkTypesRegistry` slice (`server/internal/checkers/checkerdef/types.go:424`),
the dash0 form registry (`web/dash0/src/components/checks/form/types/index.ts:158`) and the
closed TS union `CheckType` (`web/dash0/src/components/checks/form/types/common.ts:21`).
Niche protocols and in-house scripts cannot be monitored without a PR to the core, and a
private-location owner cannot run their own probe code.

The contract a plugin needs already exists: `checkerdef.Checker`
(`server/internal/checkers/checkerdef/interface.go:51`, `Type`/`Validate`/`Execute`) and a
generated JSON Schema per type, served at `GET /api/v1/checks/schema/:type`
(`server/internal/app/server.go:1156`), with `x-solidping-secret-fields` for encrypted keys.
A plugin is a check type whose schema and code come from outside the binary.

### Transports considered

- **Native `.so` (Go `plugin` package): rejected.** Builds use `CGO_ENABLED=0`
  (`Dockerfile:128`, `Dockerfile.sp:49`) with distroless images and cross-compilation
  without QEMU; `plugin.Open` needs cgo. A `.so` must be built with the exact Go version and
  dependency versions of the host, so every release breaks every plugin. No Windows, no
  unload, no isolation (a panic or leak takes down API and scheduler).
- **HTTP service: separate spec, `2026-10-17-05-http-plugins.md`.** The check runs from
  wherever the service is deployed, not from the worker's region, which makes regions, quorum
  and regional outage detection wrong unless the service is a `localhost` sidecar of each
  worker. It also bypasses the egress guard, SSH tunnels and `ipVersion`. It carries the same
  JSON messages as the exec transport.
- **JS on the existing goja runtime (`checkjs`): phase 1.** Sandboxed, inherits `http`, `tcp`,
  `udp`, `websocket`, `solidping.check()`, `secrets`, the egress guard, tunnels and the
  deadline. Works on cloud workers and agents with nothing to install.
- **Long-lived executable over JSON-RPC on stdio, plus a Nagios-compatible mode: phase 2.**
  Any language; Nagios mode opens the existing `check_*` plugin ecosystem.

## Decisions (2026-10-17)

1. Plugins serve the operator first (phase 1), then private-location owners (phase 2).
   Community distribution (catalog, signing, auto-update) is out of scope.
2. SaaS tenants cannot define their own plugin types in this spec. Plugin types come only from
   the operator's plugin directory and, in phase 2, from private-location agents.
3. No built-in checker is converted to a plugin. The reference plugin is a **new** check type,
   `x-gopher` (Gopher, RFC 1436), picked as a little-used, dated network protocol. It lives in
   the repo as the worked example and is not loaded by default.

## Proposal

### Phase 0: dynamic registry

1. Replace the `switch` statements in `registry.go:75` and `:184` with a map filled through
   `registry.Register(meta, checkerFactory, configFactory)`. Built-in checkers register the
   same way. `ListCheckTypeMetas()` (`checkerdef/types.go:504`) reads the registered metas
   instead of the static `checkTypesRegistry` slice.
2. Keep `checkjs.ResolveChecker` (`registry.go` `init`) working through the map.
3. Plugin metas are always `ExcludedFromTally`: `AdvertisedCheckTypeCount()`
   (`checkerdef/types.go:488`) never changes when plugins load.
4. `server/internal/checkers/AGENTS.md`: update the "Adding New Checkers" checklist.

### Phase 1: manifest, schema validation, JS plugins

5. Plugin directory: config key `plugins.dir`, env `SP_PLUGINS_DIR` (the env loader maps it to
   `plugins.dir` with no manual reader). Unset = feature off. One folder per plugin with a
   `manifest.json`:
   ```json
   {
     "name": "gopher",
     "version": "1.0.0",
     "protocol": "js",
     "entry": "check.js",
     "types": [{
       "type": "x-gopher",
       "description": "Check a Gopher server (RFC 1436)",
       "labels": ["safe", "category:network"],
       "schema": "schema.json",
       "minPeriod": "1m", "defaultPeriod": "5m"
     }]
   }
   ```
   Plugin types must start with `x-` so they never collide with a future built-in type.
   Loading rejects (logs and skips, never crashes the boot): a missing/invalid manifest, a
   type without the prefix, a type declared twice, a schema that does not compile, an entry
   file outside the plugin folder.
6. Generic plugin config: a `map[string]any` wrapper implementing `checkerdef.Config`. It
   implements `SecretFielder` from the schema's `x-solidping-secret-fields`, so encryption at
   rest and export redaction work unchanged.
7. Validation: for a plugin type the manifest schema **is** the validator (unlike built-ins,
   where it is only a description). Validate with `santhosh-tekuri/jsonschema/v6` (already in
   `server/go.mod`) on create/PATCH, without contacting the plugin. Errors map to the usual
   `VALIDATION_ERROR` 400.
8. `GET /api/v1/checks/schema/:type` and the check-types listing serve plugin types with their
   schema, plus `"plugin": {"name", "version"}`.
9. JS execution: the plugin's entry script runs in the `checkjs` runtime with the validated
   config exposed as a read-only `config` global (secret fields included after the
   dispatch-time merge). Same return shape as a `js` check.
10. Result limits for every plugin transport: status must be a known `checkerdef.Status`,
    `Output` capped (size), `Metrics` capped (count, numeric values only). Over a cap: the
    result is truncated and flagged, never rejected silently.
11. dash0: widen `CheckType` to accept unknown strings and add a fallback module in
    `checkTypeRegistry` that edits the config as JSON with CodeMirror (`@codemirror/lang-json`
    to add), linted against the type's schema. A schema-generated form is out of scope.
12. Reference plugin `plugins/gopher/` at the repo root (`manifest.json` above, `schema.json`,
    `check.js`), built on the `tcp.connect()` handle documented in
    `web/docs/docs/features/javascript-checks.md:467` (`write`, `read({until, timeout})`,
    failures returned as values with the partial data):
    - Config: `host` (string, required), `port` (integer 1–65535, default 70), `selector`
      (string, default `""` = root menu), `contains` (string, optional).
    - Run: connect to `host:port`, write `selector + "\r\n"`, read until the `"\r\n.\r\n"`
      terminator or until the server closes the connection (a text document may end either
      way).
    - `down` when the connection fails, the response is empty, the first menu line has item
      type `3` (Gopher error item), or `contains` is set and absent from the response.
      `timeout` when the check deadline expires (the runtime already reports it). `up`
      otherwise.
    - Metrics: `bytes` (response size), `items` (lines with at least 3 tab characters, i.e.
      menu entries; 0 for a text document). Output `message`: the first response line,
      trimmed.
    - Sample host in docs and fixtures: `gopher.acme.com`, never a real server.
13. `make dev` / `make dev-test` and the E2E server (local side-car and CI job) set
    `SP_PLUGINS_DIR` to the repo's `plugins/`, so the type shows up locally and in Playwright.
14. Docs page `web/docs/docs/features/plugins.md`: manifest format, the `config` global, limits,
    with the gopher plugin as the worked example.

### Phase 2: executable plugins and capability routing

15. `"protocol": "exec"`: one long-lived process per plugin, JSON-RPC 2.0 over stdin/stdout,
    methods `execute` (params: type, config, deadline) and optional `validate`. Restart with
    backoff on crash; requests in flight fail with `StatusError`. stderr goes to the server log.
    The executable path comes only from the manifest, resolved inside the plugin folder; a
    check config can never name a command. The `execute` and `validate` message bodies are Go
    types in `server/internal/plugins/protocol`, inside the JSON-RPC envelope; the HTTP
    transport (spec 2026-10-17-05) reuses them unchanged.
16. `"protocol": "nagios"`: fork per run with `args` built from the config through a template
    in the manifest. Exit code 0 → `Up`, 1 → `Warning`, 2 → `Down`, 3 or other → `Error`.
    First stdout line → `Output.message`; perfdata after `|`
    (`label=value[UOM];warn;crit;min;max`) → `Metrics`. Timeout kills the process group.
17. Capability routing: cloud workers advertise the plugin types they loaded; agents add them
    to their existing `Capabilities` (`server/internal/agents/protocol.go:89`).
    `ClaimJobs` (`server/internal/checkworker/checkjobsvc/service.go:236`) and
    `ClaimJobsForAgent` (`:523`) only hand a plugin-type job to a worker that advertises it.
    A check whose region has no capable worker shows that reason on the check page.
18. Agent-installed plugins: an agent sends its manifests on connect; the server stores them
    per org so the form and validation work while the agent is offline.
19. Exec and nagios plugin types are forced to the `unsafe` label (a manifest cannot declare
    them `safe`): disabled by default in SaaS mode on cloud workers, allowed on
    private-location agents.

## Tests

- `registry/registry_test.go`: every built-in type still resolves after the map refactor;
  registering a duplicate type panics at init.
- `registry/check_type_count_test.go`: loading the gopher plugin leaves
  `AdvertisedCheckTypeCount()` unchanged.
- Plugin loader: valid manifest loads; missing `x-` prefix, duplicate type, broken schema,
  missing entry file, entry path escaping the folder are each skipped with an error log and do
  not stop the other plugins.
- Plugin config: schema-valid config accepted, invalid one returns `VALIDATION_ERROR`; a field
  listed in `x-solidping-secret-fields` is encrypted at rest and redacted in exports.
- Gopher plugin, loaded from `plugins/gopher/` against a fake Gopher server on `127.0.0.1`
  (self-hosted mode, same setup as the `checkjs` socket tests):
  - menu terminated by `.` → `up`, `items` = number of menu lines, `bytes` > 0;
  - text document ended by the server closing (no `.`) → `up`, `items` = 0;
  - first line with item type `3` → `down`;
  - `contains` present → `up`; absent → `down`;
  - server accepts then closes without data → `down`;
  - server never answers, short deadline → `timeout`;
  - closed port → `down`;
  - schema: missing `host`, `port` 0 or 70000 → `VALIDATION_ERROR`;
  - SaaS mode: the loopback target is refused by the egress guard (positive control: the same
    target passes in self-hosted mode).
- Result caps: oversized `Output`, too many metrics, non-numeric metric, unknown status.
- Exec (helper-process pattern: the test binary acts as the plugin, no external interpreter):
  round trip; crash mid-request → `StatusError` then restart; deadline exceeded → timeout.
- Nagios (same helper-process pattern): each exit code mapping, perfdata parsing (with and
  without UOM/thresholds, malformed input ignored), timeout kills children.
- Claim: a worker without the plugin never claims the job (positive control: one with it does).
- dash0 Playwright: create an `x-gopher` check through the JSON editor; a schema error (missing
  `host`) is shown and nothing is saved.

## To verify

- How cloud workers currently identify themselves at claim time (where to add advertised
  plugin types) and whether `Capabilities` on agents is already used for routing or only as a
  region-picker hint (`protocol.go:87` says "hint").
- Whether the OpenAPI spec or generated clients (Terraform provider, MCP) enumerate check types
  and would reject an `x-` type.
- Whether `checkjs` can take the script from a plugin file instead of the check config without
  breaking the size/console caps.
- Where the E2E server environment is defined, locally (`wiki/testing/local-e2e.md`) and in CI.
