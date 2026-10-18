---
model: opus
effort: low
---

# Plugin schemas must not load `$ref` targets: an agent-reported schema makes the API node open local files

## Problem

Found by the security scan of 2026-10-18 (finding I1). Severity: low. The trigger is the operator of a private-location agent, i.e. an org admin who enrolled one. On SaaS that is anyone, since any account can create its own org and enroll an agent.

- `server/internal/plugins/config.go:86` `compileSchema` builds `jsv.NewCompiler()` (`:94`) and compiles the schema without ever calling `UseLoader`.
  - In `santhosh-tekuri/jsonschema/v6` (v6.0.3, `roots.go` `newRoots`), the default URL loader is `FileLoader{}`.
  - During `Compile`, that loader `os.Open`s any `file://` URL the schema references and parses the file as JSON.
  - It refuses other schemes, so this is not a network SSRF.
- Agent-reported schemas are untrusted input that reaches this compiler:
  - `handlers/agentws/handler.go:570` `recordAgentPlugins` passes `frame.Plugins` to `plugintypes.Store.Report` (`plugintypes/store.go:72`), which calls `plugins.CompileRemote` (`plugins/remote.go:158`), which calls `compileSchema`.
  - The stored row is compiled again by `compileRow` (`store.go:171`) on every `Lookup` / `List`: each check-type listing (`handlers/checktypes/orgplugins.go`), schema read (`GetOrgConfigSchema`, `:76`) and check validation in that org (`handlers/checks/orgplugins.go`).
- Impact:
  - A schema with a `$ref` to a `file://` path makes the API process open that path on its own disk.
  - The type is stored and listed in the org's check types only when compiling succeeds. Its presence or absence tells the agent operator four things:
    - whether the path exists;
    - whether the server can read it;
    - whether it parses as JSON;
    - whether a given JSON-pointer key path inside it exists and holds an object or a boolean.
  - That is enough to map the node's filesystem and enumerate keys of JSON configuration or credential files.
  - File bytes are not echoed: the published schema is the raw document, and compile errors are only logged.
  - A reference to a slow or blocking special file stalls that org's own reads.
- Confirmed during the scan with a standalone copy of `compileSchema`'s compiler setup against v6.0.3:
  - an existing file and pointer compile;
  - a missing pointer fails with "json-pointer ... not found";
  - a missing file fails with "open ...: no such file or directory";
  - a non-object target fails metaschema validation;
  - an `http://` ref fails with "invalid file url".
- Operator-loaded schemas (`plugins/loader.go:219`) and HTTP plugin descriptors (`plugins/http.go:354`) use the same function. They are trusted, but they never need an external ref either: the resource is registered at `https://solidping.io/schemas/plugins/<type>.json`, so a relative ref resolves to an https URL that the file loader already refuses.

## Proposal

1. In `compileSchema` (`plugins/config.go:94`), call `compiler.UseLoader(...)` right after `jsv.NewCompiler()`.
   - Pass a loader whose `Load` always returns an error such as "plugin schemas cannot reference external documents".
   - Draft metaschemas (`$schema`) are resolved by the library before the URL loader (`loader.go` `defaultLoader.load` calls `loadMeta` first), so they keep working.
   - In-document refs (`#/$defs/...`) never reach the loader.
2. Leave the rest of `CompileRemote` unchanged. One change covers loaded, HTTP-descriptor and agent-reported schemas.

## Tests

- `server/internal/plugins/remote_test.go`, new `TestCompileRemoteRefusesExternalRefs`:
  - Write a valid JSON object to a file under `t.TempDir()`.
  - Report a type whose schema has a property with `$ref: "file://<that path>"`, and another with a `#/<pointer>` into it.
  - Both must make `CompileRemote` return an error. This fails before the fix, because both compile today.
  - Add a case where the `$ref` targets a file that does not exist: the error must be the same refusal, not "no such file". The outcome must not depend on the file.
- Same file, positive control `TestCompileRemoteKeepsLocalRefsAndMetaschema`: a schema with `"$schema": "https://json-schema.org/draft/2020-12/schema"`, a `$defs` entry and a property `$ref: "#/$defs/host"` still compiles, and validates a config as before.
- `server/internal/plugintypes/store_test.go`: a report carrying one type with a `file://` ref and one valid type stores only the valid one, and `List` returns only that one.
- `server/internal/plugins/loader_test.go`: an operator plugin whose schema has a `file://` ref is refused at load, with an error naming the plugin.

## To verify

- `jsv.Compiler.UseLoader` takes a `URLLoader` (an interface with one method, `Load(url string) (any, error)`). Confirm the signature in v6.0.3 `compiler.go:137` and `loader.go`.
- Check that no shipped plugin schema uses an external ref: `plugins/gopher/schema.json`, `server/internal/plugins/examples/finger/schema.json`, and the plugin docs examples in `web/docs/docs/features/plugins.md`.
