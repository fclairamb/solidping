---
model: sonnet
effort: high
pr: batch
automerge: false
---

# An agent older than a check type claims its jobs and reports a false failure

## Problem

Private-location agents are customer-run and drift from the server's version (prod, 2026-11-26:
org agents on 0.52.0 and 0.49.3 while the server runs 0.54.1). When the server adds a built-in
check type, an older agent still claims its jobs, finds no parser for it and saves an error
result (`ErrUnknownCheckType`, `server/internal/checkworker/worker.go:1387`, and
`ErrCheckerNotFound` at `:1451`). The customer sees a check fail that nothing is wrong with.

Plugin types already avoid this: the agent reports them among its `capabilities` and the claim
only hands out plugin jobs of those types (`server/internal/handlers/agentws/handler.go:1283`,
`checkjobsvc.PluginTypesFromCapabilities`, spec 2026-10-17-05 §17). Built-in types have no such gate.

## Proposal

1. **Agent reports its built-in types.** On the claim and register frames, the agent adds one
   capability per built-in check type it can run, e.g. `check-type:http`, next to the plugin
   types (`checkworker/worker.go:653` and `:720`, `backend.Capabilities`). Source: the registry's
   type list (`server/internal/checkers/registry/registry.go:219`, `checkerdef.ListCheckTypes`),
   filtered by the worker's activation gate like `advertisedPluginTypes` (`worker.go:833`).
   Keep `Capabilities` three-state (`protocol.go:93-111`): absent means "not reported".
2. **Claim filters on it.** In `ClaimJobsForAgent` scope, add `CheckTypes` built from the frame
   (`checkjobsvc.CheckTypesFromCapabilities`). When the agent reported at least one `check-type:`
   capability, it only gets jobs of those types (plus its plugin types). Same predicate on
   SQLite and Postgres.
3. **Legacy agents** (no `check-type:` capability at all, i.e. built before this change): they
   get only the types in a frozen list `legacyCheckTypes`, the built-in types that exist in the
   last release without this change. A new built-in type is never added to that list. A unit
   test fails if the list contains a type that is not registered.
4. **Region view.** Aggregate the reported check types per region, like the IPv6 capability
   (`server/internal/regions/capabilities.go`). Expose them on `GET /private-regions`. In dash0,
   the region picker of the check form marks a private region whose live agents cannot run the
   check's type ("Agents in this region don't support <type>: upgrade them"), same pattern as
   `components/shared/ipv6-capability.tsx`. Saving is still allowed.
5. Docs: `wiki/features/deported-agents.md` (capabilities section and claim predicate in
   `wiki/conventions/regions.md`), `web/docs/docs/features/private-locations.md` one line.

## Tests

- `checkjobsvc` claim tests, SQLite and Postgres: an agent reporting `check-type:http` only gets
  http jobs; a job of another type stays queued for another worker (negative case).
- Legacy agent (capabilities without any `check-type:`): gets a `legacyCheckTypes` type, never a
  type outside the list.
- Agent reporting `capabilities: null`: treated as legacy, not as "supports nothing".
- `checkworker` test: the reported capabilities contain every registered built-in type and
  respect the activation gate.
- `regions` capability aggregation test for check types.
- dash0 Playwright (`check-region-ipv6-capability.spec.ts` style): a region without the type
  shows the warning, a region with it does not.

## To verify

- Where `ClaimJobsForAgent` builds its SQL from the scope, and whether a type filter can share
  the plugin-type clause.
- Whether internal (non-agent) workers go through the same scope: they must stay unfiltered or
  report the same capabilities.
- Exact release to freeze `legacyCheckTypes` at (the last tag before this spec ships).
