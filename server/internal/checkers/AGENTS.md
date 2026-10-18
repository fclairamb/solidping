# Checkers Package

This package implements the protocol checker system for SolidPing monitoring.

## Package Structure

### `checkerdef/` - Core interfaces and types
- **`Checker` interface**: `Type()`, `Validate(config)`, `Execute(ctx, config)`
- **`Config` interface**: `FromMap(map)`, `GetConfig()`
- **Common types**: `CheckType`, `Status` (Up/Down/Timeout/Error), `Result`

### `check{protocol}/` - Protocol implementations
Each protocol has its own package (e.g., `checkhttp`, `checkicmp`, `checktcp`):
- **`{Protocol}Config` struct**: Protocol-specific configuration implementing `Config` interface
- **`{Protocol}Checker` struct**: Protocol checker implementing `Checker` interface
- **Validation**: Protocol-specific validation logic
- **Execution**: Protocol-specific check execution

### `registry/` - Factory pattern
- **`Register(meta, checkerFactory, configFactory)`**: fills the type map. Built-ins register at
  init from the `builtinCheckers` table; a duplicate panics.
- **`RegisterPlugin(...)`**: same for a plugin type loaded at boot (`internal/plugins`), returns an
  error instead of panicking and also registers the meta and the config in `configregistry`.
- **`GetChecker(checkType)`** / **`ParseConfig(checkType)`**: map lookups.

## Synthetic checkers

- **`checksleep` (`sleep`)** — a synthetic/testing check that sleeps for a
  configured `sleep_ms` (optional `jitter_ms`, forced `status`). It performs no
  network I/O; its cost equals `sleep_ms`, so it is a deterministic load
  generator for the scheduler (cost/delay EWMA, cost-aware timeout). It is
  registered like any other type (so `activation_test.go` sees its meta), but it
  is **not a customer-facing feature**: its meta carries `ExcludedFromTally`
  (see "Adding New Checkers"), as does `private-location`.

## Samples

`GetSampleConfigs` (`checkerdef.CheckerSamplesProvider`) feeds the create-check sample picker and
the seed of a fresh install. The `Default` samples all show in the picker, but a fresh install
only seeds the ones marked `SeedOnInstall: true` (`checkerdef.SamplesToSeed`); `Test` and `Demo`
seed everything. Mark a sample only if it works from any internet-connected host: public IPv4
target, no local service, no extra privilege, no flaky third-party test server.
`registry/seed_samples_test.go` enforces the type list, the 10 to 15 total and the targets.

## Adding New Checkers

1. Create `check{protocol}/` package
2. Define `{Protocol}Config` struct implementing `checkerdef.Config`
3. Implement `{Protocol}Checker` struct implementing `checkerdef.Checker`
4. Register it:
   - Add a row to `builtinCheckers` in `registry/registry.go`
   - Add the config to the `configregistry.ParseConfig()` and `ValidateSpec()` switches
   - Add its `CheckTypeMeta` to `checkTypesRegistry` in `checkerdef/types.go`
5. Add constant to `checkerdef.CheckType`

A check type that should not live in the core is a plugin instead (`x-` prefix, manifest in the
operator's `plugins.dir`): see `internal/plugins` and `web/docs/docs/features/plugins.md`.
6. Regenerate the config JSON Schemas: `go generate ./internal/checkers/schemas/...`, and commit
   `internal/checkers/schemas/<type>.json` (CI fails when they are stale). Never hand-edit one —
   a rule reflection cannot see goes on the config via `checkerdef.SchemaNoter` /
   `checkerdef.SchemaExclusiveGrouper`
7. A type users do not pick (synthetic, system-created) sets `ExcludedFromTally` on its
   `CheckTypeMeta`. Otherwise the advertised count (`checkerdef.AdvertisedCheckTypeCount()`)
   goes up by one: `registry/check_type_count_test.go` then fails and names every doc
   (README, `web/docs/docs/**`, `web/docs/docusaurus.config.ts`) to bump.
8. Run the lint and tests and fix any issues

## Config JSON Schemas

`schemas/` holds one generated JSON Schema (draft 2020-12) per check type,
produced by `server/gen/checkerschema` from the same `XConfig` structs the
validators run on, enumerated through `configregistry`. They are served at
`GET /api/v1/checks/schema/<type>` and attached to releases.

They are a **description for editors and tooling, never a validator** — validation
authority stays with the Go `Validate()`. Details, including what the
`x-solidping-*` keys mean and how `required` is derived from the validator rather
than from `omitempty`, are in `wiki/api-specification/checks.md`.
